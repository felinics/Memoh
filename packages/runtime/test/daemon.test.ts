import { mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises'
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { findNodeExecutable, type CommandRunner, type RuntimeServiceSpec } from '../src/daemon'
import { createLaunchdServiceManager, renderLaunchdPlist } from '../src/daemon/launchd'
import { createSystemdServiceManager, renderSystemdUnit } from '../src/daemon/systemd'
import { createWindowsTaskServiceManager, renderWindowsTaskXML } from '../src/daemon/windows'
import { resolveRuntimePaths } from '../src/runtime-config'

const roots: string[] = []
afterEach(async () => {
  vi.unstubAllEnvs()
  await Promise.all(roots.splice(0).map(path => rm(path, { recursive: true, force: true })))
})

describe('native service definitions', () => {
  it('runs the existing CLI with saved config and OS supervision on all platforms', () => {
    const spec = serviceSpec('/home/a &b')
    const unit = renderSystemdUnit(spec)
    expect(unit).toContain(`ExecStart="${spec.nodePath}" "${spec.entryPath}" run --config "${spec.configPath}"`)
    expect(unit).toContain('WorkingDirectory=/home/a &b\n')
    expect(unit).toContain('Restart=always')
    const plist = renderLaunchdPlist(spec)
    expect(plist).toContain('<string>/home/a &amp;b/node</string>')
    expect(plist).toContain('<string>/home/a &amp;b/cli.mjs</string>')
    expect(plist).toContain('<string>run</string>')
    expect(plist).toContain('<key>KeepAlive</key>')
    if (process.platform === 'darwin') {
      const result = spawnSync('/usr/bin/plutil', ['-lint', '-'], { input: plist, encoding: 'utf8' })
      expect(result.status, result.stderr).toBe(0)
    }
    const xml = renderWindowsTaskXML({ ...spec, nodePath: 'C:\\Program Files\\nodejs\\node.exe', entryPath: 'C:\\Memoh Runtime\\cli.mjs' }, 'S-1-5-21-123')
    expect(xml).toContain('<Command>C:\\Program Files\\nodejs\\node.exe</Command>')
    expect(xml).toContain('&quot;C:\\Memoh Runtime\\cli.mjs&quot; run --config')
    expect(xml).toContain('--log')
    expect(xml).toContain('<LogonType>InteractiveToken</LogonType>')
    expect(xml).toContain('<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>')
    for (const definition of [unit, plist, xml]) expect(definition).not.toContain('--key')
  })

  it('escapes systemd arguments without quoting path directives', () => {
    const unit = renderSystemdUnit({ ...serviceSpec('/home/a $b'), configPath: '/home/a %b/runtime.json' })
    expect(unit).toContain('"/home/a $$b/cli.mjs" run --config "/home/a %%b/runtime.json"')
    expect(unit).toContain('WorkingDirectory=/home/a $b\n')
  })

  it.runIf(process.platform !== 'win32')('keeps a stable Node symlink when registering the service', async () => {
    const { root } = await fixture()
    await writeFile(join(root, 'node-v1'), '', { mode: 0o755 })
    await symlink(join(root, 'node-v1'), join(root, 'node'))
    expect(await findNodeExecutable(root, process.platform)).toBe(join(root, 'node'))
    await expect(findNodeExecutable('', process.platform)).rejects.toThrow('Node was not found')
  })
})

describe('native service lifecycle', () => {
  it('registers, starts, stops and removes the launchd user agent', async () => {
    const { root, paths } = await fixture()
    let loaded = false
    const runner = vi.fn<CommandRunner>(async (_command, args) => {
      if (args[0] === 'bootstrap') loaded = true
      if (args[0] === 'bootout') loaded = false
      return { code: args[0] === 'print' && !loaded ? 3 : 0, stdout: loaded ? 'state = running' : '', stderr: '' }
    })
    const manager = createLaunchdServiceManager(paths, runner, 501)
    await manager.register({ ...serviceSpec(root), logsDir: paths.logsDir })
    expect((await manager.status()).state).toBe('stopped')
    await manager.start()
    expect((await manager.status()).state).toBe('running')
    await manager.stop()
    expect((await manager.status()).state).toBe('stopped')
    await manager.uninstall()
    expect((await manager.status()).state).toBe('not-installed')
    expect(runner).toHaveBeenCalledWith('/bin/launchctl', ['bootstrap', 'gui/501', paths.launchdPlistPath], expect.any(Object))
  })

  it('registers and controls a systemd user unit and propagates native failures', async () => {
    const { paths } = await fixture()
    const runner = vi.fn<CommandRunner>(async () => ({ code: 0, stdout: '', stderr: '' }))
    const manager = createSystemdServiceManager(paths, runner)
    await manager.register(serviceSpec('/home/alice'))
    expect(await readFile(paths.systemdUnitPath, 'utf8')).toContain('run --config')
    await manager.start()
    await manager.stop()
    await manager.uninstall()
    expect(runner.mock.calls.map(([command, args]) => [command, args])).toEqual([
      ['systemd-analyze', ['--user', 'verify', paths.systemdUnitPath]],
      ['systemctl', ['--user', 'daemon-reload']],
      ['systemctl', ['--user', 'enable', paths.systemdUnitPath]],
      ['systemctl', ['--user', 'start', 'memoh-runtime.service']],
      ['systemctl', ['--user', 'stop', 'memoh-runtime.service']],
      ['systemctl', ['--user', 'disable', '--now', 'memoh-runtime.service']],
      ['systemctl', ['--user', 'daemon-reload']],
      ['systemctl', ['--user', 'reset-failed', 'memoh-runtime.service']],
    ])
    runner.mockResolvedValueOnce({ code: 1, stdout: '', stderr: 'Failed to connect to bus' })
    await expect(manager.start()).rejects.toThrow('Failed to connect to bus')
  })

  it('registers a UTF-16 Windows logon task and scopes lifecycle commands to the user', async () => {
    const { paths } = await fixture()
    vi.stubEnv('PSModulePath', 'incompatible PowerShell 7 modules')
    const runner = vi.fn<CommandRunner>(async command => ({
      code: 0, stdout: command === 'whoami.exe' ? '"alice","S-1-5-21-123"' : command === 'powershell.exe' ? 'Running' : '', stderr: '',
    }))
    const manager = createWindowsTaskServiceManager(paths, runner)
    await manager.register({ ...serviceSpec('C:\\Memoh'), logsDir: paths.logsDir })
    expect((await readFile(paths.windowsTaskXMLPath)).subarray(0, 2)).toEqual(Buffer.from([0xff, 0xfe]))
    await manager.start()
    expect((await manager.status()).state).toBe('running')
    await manager.stop()
    await manager.uninstall()
    const commands = runner.mock.calls.filter(([command]) => command === 'schtasks.exe')
    expect(commands.map(([, args]) => args[0])).toEqual(['/create', '/run', '/end', '/end', '/delete'])
    for (const [, args] of commands) expect(args.at(-1)).toBe('\\Memoh\\Runtime-S-1-5-21-123')
    for (const [, , options] of runner.mock.calls.filter(([command]) => command === 'powershell.exe')) {
      expect(options?.env).not.toHaveProperty('PSModulePath')
    }
  })
})

function serviceSpec(home: string): RuntimeServiceSpec {
  return { nodePath: `${home}/node`, entryPath: `${home}/cli.mjs`, configPath: `${home}/.memoh/runtime.json`,
    logsDir: `${home}/.memoh/runtime/logs`, workingDirectory: home, servicePath: '/usr/local/bin:/usr/bin:/bin' }
}

async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'memoh-daemon-'))
  roots.push(root)
  return { root, paths: resolveRuntimePaths({ home: root }) }
}
