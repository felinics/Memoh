import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { afterEach, describe, expect, it, vi } from 'vitest'

import { runCLI, type CLIContext } from '../src/cli-main'
import type { CommandResult } from '../src/daemon'
import { readRuntimeEnrollment, resolveRuntimePaths } from '../src/runtime-config'

const runtimeKey = `mrk_${'a'.repeat(64)}`
const replacementKey = `mrk_${'b'.repeat(64)}`
const runtimeID = '11111111-1111-4111-8111-111111111111'
const replacementRuntimeID = '22222222-2222-4222-8222-222222222222'
const temporaryDirectories: string[] = []

afterEach(async () => {
  await Promise.all(temporaryDirectories.splice(0).map(path => rm(path, { recursive: true, force: true })))
})

describe('runtime CLI', () => {
  it('keeps the legacy foreground invocation, saves it, and allows a bare restart', async () => {
    const fixture = await cliFixture()
    const factory = vi.fn(() => resolvedSession())

    await expect(runCLI([
      '--server', 'https://memoh.example/api',
      '--key', runtimeKey,
      '--runtime-id', runtimeID,
    ], { ...fixture.context, createSession: factory })).resolves.toBe(0)

    expect(factory).toHaveBeenLastCalledWith(
      expect.objectContaining({ serverUrl: 'https://memoh.example/api', key: runtimeKey }),
      expect.objectContaining({ onStatus: expect.any(Function) }),
    )
    expect(await readRuntimeEnrollment(fixture.paths.configPath, fixture.root)).toMatchObject({
      runtimeId: runtimeID,
      key: runtimeKey,
    })

    factory.mockClear()
    await expect(runCLI([], { ...fixture.context, createSession: factory })).resolves.toBe(0)
    expect(factory).toHaveBeenCalledWith(
      expect.objectContaining({ key: runtimeKey }),
      expect.any(Object),
    )
  })

  it('does not persist environment-only credentials unless requested', async () => {
    const fixture = await cliFixture({
      MEMOH_RUNTIME_SERVER: 'https://memoh.example',
      MEMOH_RUNTIME_KEY: runtimeKey,
    })
    await runCLI([], { ...fixture.context, createSession: () => resolvedSession() })
    await expect(readFile(fixture.paths.configPath, 'utf8')).rejects.toMatchObject({ code: 'ENOENT' })
  })

  it('installs one idempotent systemd user service from staged package assets', async () => {
    const fixture = await cliFixture()
    const commands: Array<[string, string[]]> = []
    const runner = vi.fn(async (command: string, args: string[]): Promise<CommandResult> => {
      commands.push([command, args])
      return { code: 0, stdout: args.includes('is-active') ? 'active\n' : '', stderr: '' }
    })

    await expect(runCLI([
      'service', 'install',
      '--server', 'https://memoh.example/api',
      '--key', runtimeKey,
      '--runtime-id', runtimeID,
    ], { ...fixture.context, runner })).resolves.toBe(0)

    const unit = await readFile(fixture.paths.systemdUnitPath, 'utf8')
    const manifest = await readFile(fixture.paths.manifestPath, 'utf8')
    expect(unit).toContain('Restart=always')
    expect(unit).not.toContain(runtimeKey)
    expect(manifest).not.toContain(runtimeKey)
    expect(commands).toContainEqual(['systemctl', ['--user', 'daemon-reload']])
    expect(commands).toContainEqual(['systemctl', ['--user', 'enable', '--now', 'memoh-runtime.service']])
    expect(fixture.output.join('\n')).toContain('installed and started')
  })

  it('does not replace a different enrollment without an explicit flag', async () => {
    const fixture = await cliFixture()
    await runCLI([
      'service', 'install', '--server', 'https://one.example', '--key', runtimeKey, '--runtime-id', runtimeID,
    ], { ...fixture.context, runner: successfulRunner })

    await expect(runCLI([
      'service', 'install', '--server', 'https://two.example', '--key', replacementKey,
      '--runtime-id', replacementRuntimeID,
    ], { ...fixture.context, runner: successfulRunner })).rejects.toThrow('--replace')
    expect((await readRuntimeEnrollment(fixture.paths.configPath, fixture.root)).runtimeId).toBe(runtimeID)
  })

  it('refuses recursive purge when the runtime home was overridden', async () => {
    const fixture = await cliFixture()
    await expect(runCLI(
      ['service', 'uninstall', '--purge'],
      { ...fixture.context, runner: successfulRunner },
    )).rejects.toThrow('remove custom paths manually')
  })
})

async function cliFixture(extraEnv: NodeJS.ProcessEnv = {}) {
  const root = await temporaryDirectory()
  const runtimeHome = join(root, 'runtime-home')
  const entryPath = join(root, 'package', 'cli.mjs')
  const protoPath = join(root, 'package', 'bridge.proto')
  await mkdir(join(root, 'package'))
  await writeFile(entryPath, '#!/usr/bin/env node\n')
  await writeFile(protoPath, 'syntax = "proto3";\n')
  const env = {
    PATH: '/usr/local/bin:/usr/bin:/bin',
    MEMOH_RUNTIME_HOME: runtimeHome,
    XDG_CONFIG_HOME: join(root, 'config'),
    ...extraEnv,
  }
  const output: string[] = []
  const context: Partial<CLIContext> = {
    platform: 'linux',
    env,
    home: root,
    nodePath: '/usr/local/bin/node',
    entryPath,
    protoPath,
    uid: 501,
    stdout: message => output.push(message),
    stderr: message => output.push(message),
  }
  return {
    root,
    output,
    context,
    paths: resolveRuntimePaths({ home: root, env }),
  }
}

function resolvedSession() {
  return {
    start: vi.fn(async () => undefined),
    stop: vi.fn(),
  }
}

async function successfulRunner(_command: string, _args: string[]): Promise<CommandResult> {
  return { code: 0, stdout: '', stderr: '' }
}

async function temporaryDirectory(): Promise<string> {
  const path = await mkdtemp(join(tmpdir(), 'memoh-runtime-cli-'))
  temporaryDirectories.push(path)
  return path
}
