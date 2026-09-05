import { mkdtemp, readFile, readlink, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'

import { afterEach, describe, expect, it } from 'vitest'

import {
  renderLaunchdPlist,
  renderSystemdUnit,
  renderWindowsTaskXML,
  stageRuntimeArtifacts,
  type RuntimeServiceSpec,
} from '../src/daemon'
import { createLaunchdServiceManager } from '../src/daemon/launchd'
import type { CommandResult } from '../src/daemon/types'
import { createWindowsTaskServiceManager } from '../src/daemon/windows'
import { resolveRuntimePaths } from '../src/runtime-config'

const temporaryDirectories: string[] = []

afterEach(async () => {
  await Promise.all(temporaryDirectories.splice(0).map(path => rm(path, { recursive: true, force: true })))
})

describe('runtime background service definitions', () => {
  it('renders a foreground worker under systemd without credentials', () => {
    const unit = renderSystemdUnit(serviceSpec('/home/alice/Memoh Runtime/cli.mjs'))
    expect(unit).toContain('ExecStart="/home/alice/Memoh Runtime/cli.mjs" run --config "/home/alice/.memoh/runtime/config.json"')
    expect(unit).toContain('Restart=always')
    expect(unit).toContain('UMask=0077')
    expect(unit).not.toContain('mrk_')
  })

  it('renders an escaped per-user launchd agent with native logs', () => {
    const plist = renderLaunchdPlist(serviceSpec('/Users/a&b/runtime/cli.mjs'))
    expect(plist).toContain('<string>/Users/a&amp;b/runtime/cli.mjs</string>')
    expect(plist).toContain('<key>KeepAlive</key>')
    expect(plist).toContain('<key>StandardErrorPath</key>')
    // ProcessType Background makes node hang inside dyld under launchd on
    // macOS 27 (user lookup never returns), so the agent runs as Standard.
    expect(plist).not.toContain('ProcessType')
    expect(plist).not.toContain('mrk_')
  })

  it.runIf(process.platform === 'darwin')('emits a plist accepted by the platform validator', () => {
    const plist = renderLaunchdPlist(serviceSpec('/Users/alice/.memoh/runtime/cli.mjs'))
    const result = spawnSync('/usr/bin/plutil', ['-lint', '-'], {
      input: plist,
      encoding: 'utf8',
    })
    expect(result.status, result.stderr).toBe(0)
  })

  it('renders a no-time-limit Windows logon task with failure recovery', () => {
    const task = renderWindowsTaskXML(serviceSpec('C:\\Users\\Alice\\Memoh\\cli.mjs'), 'S-1-5-21-123')
    expect(task).toContain('<LogonType>InteractiveToken</LogonType>')
    expect(task).toContain('<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>')
    expect(task).toContain('<Count>255</Count>')
    expect(task).toContain('<Command>C:\\Program Files\\nodejs\\node.exe</Command>')
    expect(task).not.toContain('mrk_')
  })

  it('stages the bundled CLI and bridge contract outside the npm cache', async () => {
    const root = await temporaryDirectory()
    const sourceEntry = join(root, 'source-cli.mjs')
    const sourceProto = join(root, 'source-bridge.proto')
    await writeFile(sourceEntry, '#!/usr/bin/env node\nconsole.log("runtime")\n')
    await writeFile(sourceProto, 'syntax = "proto3";\n')
    const paths = resolveRuntimePaths({
      home: root,
      env: { MEMOH_RUNTIME_HOME: join(root, 'installed') },
    })

    const staged = await stageRuntimeArtifacts(paths, '1.2.3', {
      entryPath: sourceEntry,
      protoPath: sourceProto,
    })

    expect(staged.entryPath).toBe(join(root, 'installed', 'versions', '1.2.3', 'cli.mjs'))
    expect(await readFile(staged.entryPath, 'utf8')).toContain('console.log')
    expect(await readFile(staged.protoPath, 'utf8')).toContain('proto3')
  })

  it.runIf(process.platform !== 'win32')('launches through a "Memoh Runtime" link so Login Items shows the product name', async () => {
    const root = await temporaryDirectory()
    const sourceEntry = join(root, 'source-cli.mjs')
    const sourceProto = join(root, 'source-bridge.proto')
    await writeFile(sourceEntry, 'export const ok = true\n')
    await writeFile(sourceProto, 'syntax = "proto3";\n')
    const paths = resolveRuntimePaths({ home: root, env: { MEMOH_RUNTIME_HOME: join(root, 'installed') } })

    const first = await stageRuntimeArtifacts(paths, '1.2.3', { entryPath: sourceEntry, protoPath: sourceProto })
    const second = await stageRuntimeArtifacts(paths, '1.2.3', { entryPath: sourceEntry, protoPath: sourceProto })

    expect(second.launcherPath).toBe(join(root, 'installed', 'versions', '1.2.3', 'Memoh Runtime'))
    expect(first.launcherPath).toBe(second.launcherPath)
    expect(await readlink(second.launcherPath)).toBe('cli.mjs')
    expect(await readFile(second.launcherPath, 'utf8')).toContain('ok = true')
  })

  it('launches the entry file directly on Windows', async () => {
    const root = await temporaryDirectory()
    const sourceEntry = join(root, 'source-cli.mjs')
    const sourceProto = join(root, 'source-bridge.proto')
    await writeFile(sourceEntry, 'export const ok = true\n')
    await writeFile(sourceProto, 'syntax = "proto3";\n')
    const paths = resolveRuntimePaths({ home: root, env: { MEMOH_RUNTIME_HOME: join(root, 'installed') } })

    const staged = await stageRuntimeArtifacts(paths, '1.2.3', { entryPath: sourceEntry, protoPath: sourceProto }, 'win32')

    expect(staged.launcherPath).toBe(staged.entryPath)
  })

  it('replaces and bootstraps one launchd job during an idempotent install', async () => {
    const root = await temporaryDirectory()
    const paths = resolveRuntimePaths({ home: root, env: { MEMOH_RUNTIME_HOME: join(root, 'runtime') } })
    const calls: string[][] = []
    let printCount = 0
    const runner = async (_command: string, args: string[]): Promise<CommandResult> => {
      calls.push(args)
      if (args[0] === 'print') return { code: printCount++ === 0 ? 0 : 1, stdout: '', stderr: '' }
      return { code: 0, stdout: '', stderr: '' }
    }

    await createLaunchdServiceManager(paths, runner, 501).install({
      ...serviceSpec(join(root, 'cli.mjs')),
      logsDir: paths.logsDir,
    })

    expect(calls).toContainEqual(['bootout', 'gui/501/ai.memoh.runtime'])
    expect(calls).toContainEqual(['bootstrap', 'gui/501', paths.launchdPlistPath])
    expect(await readFile(paths.launchdPlistPath, 'utf8')).toContain('ai.memoh.runtime')
  })

  it('retries launchd bootstrap while a booted-out job is still being torn down', async () => {
    const root = await temporaryDirectory()
    const paths = resolveRuntimePaths({ home: root, env: { MEMOH_RUNTIME_HOME: join(root, 'runtime') } })
    let bootstrapCalls = 0
    const runner = async (_command: string, args: string[]): Promise<CommandResult> => {
      if (args[0] === 'print') return { code: 0, stdout: '', stderr: '' }
      if (args[0] === 'bootstrap') {
        bootstrapCalls++
        return bootstrapCalls < 3
          ? { code: 5, stdout: '', stderr: 'Bootstrap failed: 5: Input/output error' }
          : { code: 0, stdout: '', stderr: '' }
      }
      return { code: 0, stdout: '', stderr: '' }
    }

    await createLaunchdServiceManager(paths, runner, 501, { bootstrapRetryDelayMs: 0 }).install({
      ...serviceSpec(join(root, 'cli.mjs')),
      logsDir: paths.logsDir,
    })

    expect(bootstrapCalls).toBe(3)
  })

  it('waits for launchd to finish tearing the job down before uninstall returns', async () => {
    const root = await temporaryDirectory()
    const paths = resolveRuntimePaths({ home: root, env: { MEMOH_RUNTIME_HOME: join(root, 'runtime') } })
    const calls: string[] = []
    let printsAfterBootout = 0
    let bootedOut = false
    const runner = async (_command: string, args: string[]): Promise<CommandResult> => {
      calls.push(args[0])
      if (args[0] === 'bootout') bootedOut = true
      if (args[0] === 'print') {
        if (!bootedOut) return { code: 0, stdout: 'state = running', stderr: '' }
        printsAfterBootout++
        return { code: printsAfterBootout <= 2 ? 0 : 113, stdout: '', stderr: '' }
      }
      return { code: 0, stdout: '', stderr: '' }
    }

    await createLaunchdServiceManager(paths, runner, 501, { bootstrapRetryDelayMs: 0 }).uninstall()

    expect(printsAfterBootout).toBe(3)
    expect(calls.indexOf('bootout')).toBeLessThan(calls.lastIndexOf('print'))
  })

  it('gives up launchd bootstrap after repeated failures with the original error', async () => {
    const root = await temporaryDirectory()
    const paths = resolveRuntimePaths({ home: root, env: { MEMOH_RUNTIME_HOME: join(root, 'runtime') } })
    let bootstrapCalls = 0
    const runner = async (_command: string, args: string[]): Promise<CommandResult> => {
      if (args[0] === 'bootstrap') {
        bootstrapCalls++
        return { code: 5, stdout: '', stderr: 'Bootstrap failed: 5: Input/output error' }
      }
      return { code: 1, stdout: '', stderr: '' }
    }

    await expect(createLaunchdServiceManager(paths, runner, 501, { bootstrapRetryDelayMs: 0 }).start())
      .rejects.toThrow('Bootstrap failed: 5: Input/output error')
    expect(bootstrapCalls).toBe(5)
  })

  it('locks the credential ACL before registering and starting a Windows task', async () => {
    const root = await temporaryDirectory()
    const paths = resolveRuntimePaths({ home: root, env: { MEMOH_RUNTIME_HOME: join(root, 'runtime') } })
    const calls: Array<[string, string[]]> = []
    const runner = async (command: string, args: string[]): Promise<CommandResult> => {
      calls.push([command, args])
      if (command === 'whoami.exe') {
        return { code: 0, stdout: '"DESKTOP\\alice","S-1-5-21-123"\r\n', stderr: '' }
      }
      return { code: 0, stdout: '', stderr: '' }
    }

    await createWindowsTaskServiceManager(paths, runner).install(serviceSpec('C:\\Memoh\\cli.mjs'))

    expect(calls).toContainEqual([
      'icacls.exe',
      [expect.any(String), '/inheritance:r', '/grant:r', '*S-1-5-21-123:(F)'],
    ])
    expect(calls).toContainEqual([
      'schtasks.exe',
      ['/create', '/tn', '\\Memoh\\Runtime', '/xml', paths.windowsTaskXMLPath, '/f'],
    ])
    expect(calls).toContainEqual(['schtasks.exe', ['/run', '/tn', '\\Memoh\\Runtime']])
  })
})

function serviceSpec(entryPath: string): RuntimeServiceSpec {
  return {
    entryPath,
    configPath: entryPath.startsWith('C:')
      ? 'C:\\Users\\Alice\\.memoh\\runtime\\config.json'
      : entryPath.startsWith('/Users')
        ? '/Users/a&b/.memoh/runtime/config.json'
        : '/home/alice/.memoh/runtime/config.json',
    nodePath: entryPath.startsWith('C:') ? 'C:\\Program Files\\nodejs\\node.exe' : '/usr/local/bin/node',
    runtimeHome: '/runtime',
    logsDir: entryPath.startsWith('/Users') ? '/Users/a&b/.memoh/runtime/logs' : '/runtime/logs',
    workingDirectory: entryPath.startsWith('C:') ? 'C:\\Users\\Alice' : '/home/alice',
    servicePath: '/usr/local/bin:/usr/bin:/bin',
  }
}

async function temporaryDirectory(): Promise<string> {
  const path = await mkdtemp(join(tmpdir(), 'memoh-runtime-daemon-'))
  temporaryDirectories.push(path)
  return path
}
