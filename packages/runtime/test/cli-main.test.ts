import { mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { runCLI, type CLIContext } from '../src/cli-main'
import { createRuntimeServiceManager, type RuntimeServiceManager } from '../src/daemon'
import { readRuntimeEnrollment, resolveRuntimePaths } from '../src/runtime-config'

vi.mock('../src/daemon', async importOriginal => ({
  ...await importOriginal<typeof import('../src/daemon')>(),
  createRuntimeServiceManager: vi.fn(),
}))

const keyA = `mrk_${'a'.repeat(64)}`
const flagsA = ['--server', 'https://one.example', '--key', keyA]
const flagsB = ['--server', 'https://two.example', '--key', `mrk_${'b'.repeat(64)}`]
const roots: string[] = []
afterEach(async () => {
  vi.clearAllMocks()
  await Promise.all(roots.splice(0).map(path => rm(path, { recursive: true, force: true })))
})

describe('configuration and service commands', () => {
  it('persists enrollment, loads it on the next run, and keeps temporary runs read-only', async () => {
    const f = await fixture()
    await runCLI(flagsA, f.context)
    await expect(readFile(f.paths.configPath)).rejects.toMatchObject({ code: 'ENOENT' })
    await runCLI(['enroll', ...flagsA], f.context)
    await runCLI(['run', ...flagsB], f.context)
    await runCLI([], f.context)
    expect(f.context.createSession).toHaveBeenLastCalledWith(expect.objectContaining({ key: keyA }), expect.any(Object))
    await runCLI(['run', '--config', f.paths.configPath], {
      ...f.context, env: { MEMOH_RUNTIME_SERVER: 'https://two.example', MEMOH_RUNTIME_KEY: flagsB[3] },
    })
    expect(f.context.createSession).toHaveBeenLastCalledWith(expect.objectContaining({ key: keyA }), expect.any(Object))
    expect((await readRuntimeEnrollment(f.paths.configPath)).key).toBe(keyA)
    expect(createRuntimeServiceManager).not.toHaveBeenCalled()
  })

  it('requires explicit replacement for a different or malformed saved configuration', async () => {
    const f = await fixture()
    await runCLI(['enroll', ...flagsA], f.context)
    await expect(runCLI(['enroll', ...flagsB], f.context)).rejects.toThrow('--replace')
    expect((await readRuntimeEnrollment(f.paths.configPath)).key).toBe(keyA)
    await runCLI(['enroll', ...flagsB, '--replace'], f.context)
    expect((await readRuntimeEnrollment(f.paths.configPath)).key).toBe(flagsB[3])
    await writeFile(f.paths.configPath, '{broken')
    await expect(runCLI(['enroll', ...flagsA], f.context)).rejects.toThrow('--replace')
    await runCLI(['enroll', ...flagsA, '--replace'], f.context)
    expect((await readRuntimeEnrollment(f.paths.configPath)).key).toBe(keyA)
  })

  it('registers the existing CLI without copying programs or saving credentials', async () => {
    const f = await fixture()
    await runCLI(['service', 'install'], f.context)
    expect(f.manager.register).toHaveBeenCalledWith(expect.objectContaining({
      entryPath: f.context.entryPath, nodePath: join(f.root, process.platform === 'win32' ? 'node.exe' : 'node'),
      configPath: f.paths.configPath,
    }))
    expect(f.manager.start).not.toHaveBeenCalled()
    expect((await readdir(f.root)).sort()).toEqual(['bridge.proto', 'cli.mjs', process.platform === 'win32' ? 'node.exe' : 'node'].sort())
    vi.mocked(f.manager.register).mockRejectedValueOnce(new Error('registration failed'))
    await expect(runCLI(['service', 'install'], f.context)).rejects.toThrow('registration failed')
  })

  it('delegates lifecycle commands and retains configuration on uninstall', async () => {
    const f = await fixture()
    await runCLI(['enroll', ...flagsA], f.context)
    await runCLI(['service', 'start'], f.context)
    expect(await runCLI(['service', 'status'], f.context)).toBe(0)
    vi.mocked(f.manager.stop).mockClear()
    vi.mocked(f.manager.start).mockClear()
    await runCLI(['service', 'restart'], f.context)
    expect(f.manager.stop).toHaveBeenCalledOnce()
    expect(f.manager.start).toHaveBeenCalledOnce()
    expect(vi.mocked(f.manager.stop).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(f.manager.start).mock.invocationCallOrder[0])
    await writeFile(f.paths.configPath, '{broken')
    vi.mocked(f.manager.stop).mockClear()
    await expect(runCLI(['service', 'restart'], f.context)).rejects.toThrow('not valid JSON')
    expect(f.manager.stop).not.toHaveBeenCalled()
    await runCLI(['service', 'stop'], f.context)
    expect(await runCLI(['service', 'status'], f.context)).toBe(1)
    await runCLI(['service', 'uninstall'], f.context)
    expect(f.manager.uninstall).toHaveBeenCalledOnce()
    expect(await readFile(f.paths.configPath, 'utf8')).toBe('{broken')
  })

  it('writes status and startup errors to the log used by Windows tasks', async () => {
    const f = await fixture()
    const log = join(f.root, 'runtime.log')
    await expect(runCLI(['run', '--log', log], f.context)).rejects.toThrow('not found')
    expect(await readFile(log, 'utf8')).toContain('runtime configuration was not found')
    vi.mocked(f.context.createSession).mockImplementationOnce((_config, options) => ({
      start: async () => { options.onStatus?.('connected') }, stop: () => {},
    }))
    await runCLI(['run', ...flagsA, '--log', log], f.context)
    expect(await readFile(log, 'utf8')).toContain(' connected\n')
  })
})

async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'memoh-cli-'))
  roots.push(root)
  await writeFile(join(root, 'cli.mjs'), '')
  await writeFile(join(root, 'bridge.proto'), '')
  await writeFile(join(root, process.platform === 'win32' ? 'node.exe' : 'node'), '', { mode: 0o755 })
  let state: 'running' | 'stopped' | 'not-installed' = 'stopped'
  const manager: RuntimeServiceManager = {
    backend: 'test', register: vi.fn(async () => {}),
    start: vi.fn(async () => { state = 'running' }),
    stop: vi.fn(async () => { state = 'stopped' }),
    uninstall: vi.fn(async () => { state = 'not-installed' }),
    status: vi.fn(async () => ({ backend: 'test', state })),
  }
  vi.mocked(createRuntimeServiceManager).mockReturnValue(manager)
  const context: CLIContext = {
    platform: process.platform, home: root, env: { PATH: root }, entryPath: join(root, 'cli.mjs'),
    createSession: vi.fn(() => ({ start: async () => {}, stop: () => {} })),
    runner: vi.fn(), stdout: () => {}, stderr: () => {},
  }
  return { root, paths: resolveRuntimePaths({ home: root }), context, manager }
}
