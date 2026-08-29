import { chmod, mkdir, mkdtemp, realpath, rm, symlink, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { afterEach, describe, expect, it } from 'vitest'

import { discoverBundledACPLaunchers } from './acp-adapters'

const temporaryDirectories: string[] = []

afterEach(async () => {
  await Promise.all(temporaryDirectories.splice(0).map(path => rm(path, { recursive: true, force: true })))
})

describe('discoverBundledACPLaunchers', () => {
  it.each(['darwin', 'linux'] as const)('uses fixed adapters and local Agent entries on %s', async (platform) => {
    const fixture = await executableFixture(['codex', 'claude'])
    const launchers = await discoverBundledACPLaunchers({
      platform,
      electronExecutable: '/Applications/Memoh.app/Contents/MacOS/Memoh',
      appPath: '/workspace/apps/desktop',
      resourcesPath: '/Applications/Memoh.app/Contents/Resources',
      isPackaged: false,
      homeDirectory: fixture.home,
      pathValue: fixture.bin,
      loginShell: false,
    })

    expect(launchers).toEqual({
      'codex-acp': {
        nodeExecutable: '/Applications/Memoh.app/Contents/MacOS/Memoh',
        adapterEntry: join(
          '/workspace/apps/desktop/node_modules',
          '@agentclientprotocol/codex-acp/dist/index.js',
        ),
        codexExecutable: join(fixture.bin, 'codex'),
      },
      'claude-agent-acp': {
        nodeExecutable: '/Applications/Memoh.app/Contents/MacOS/Memoh',
        adapterEntry: join(
          '/workspace/apps/desktop/node_modules',
          '@agentclientprotocol/claude-agent-acp/dist/index.js',
        ),
        claudeCodeExecutable: join(fixture.bin, 'claude'),
      },
    })
  })

  it('uses app.asar for packaged adapters and disables a missing local Agent', async () => {
    const fixture = await executableFixture(['codex'])
    const launchers = await discoverBundledACPLaunchers({
      platform: 'darwin',
      electronExecutable: '/Applications/Memoh.app/Contents/MacOS/Memoh',
      appPath: '/unused',
      resourcesPath: '/Applications/Memoh.app/Contents/Resources',
      isPackaged: true,
      homeDirectory: fixture.home,
      pathValue: fixture.bin,
      loginShell: false,
    })

    expect(launchers['codex-acp']).toEqual(expect.objectContaining({
      adapterEntry: join(
        '/Applications/Memoh.app/Contents/Resources/app.asar/node_modules',
        '@agentclientprotocol/codex-acp/dist/index.js',
      ),
    }))
    expect(launchers['claude-agent-acp']).toBe(false)
  })

  it('explicitly disables both aliases on Windows', async () => {
    await expect(discoverBundledACPLaunchers({
      platform: 'win32',
      electronExecutable: String.raw`C:\Program Files\Memoh\Memoh.exe`,
      appPath: String.raw`C:\Program Files\Memoh\resources\app.asar`,
      resourcesPath: String.raw`C:\Program Files\Memoh\resources`,
      isPackaged: true,
      homeDirectory: String.raw`C:\Users\memoh`,
    })).resolves.toEqual({
      'codex-acp': false,
      'claude-agent-acp': false,
    })
  })

  it('prefers the login-shell PATH and keeps symlinked entries unresolved', async () => {
    const fixture = await executableFixture(['codex'])
    // A fake login shell that reports a managed claude the fallback
    // directories cannot see, plus no codex, exercising both probe branches.
    const managedBin = join(fixture.home, 'managed', 'bin')
    await executable(join(managedBin, 'claude-real'))
    await symlink(join(managedBin, 'claude-real'), join(managedBin, 'claude'))
    const shell = join(fixture.home, 'fake-shell')
    await writeFile(shell, [
      '#!/bin/sh',
      `printf 'codex:%s\\nclaude:%s\\n' '' '${join(managedBin, 'claude')}'`,
      '',
    ].join('\n'), { mode: 0o700 })
    await chmod(shell, 0o700)

    const launchers = await discoverBundledACPLaunchers({
      platform: 'linux',
      electronExecutable: '/opt/Memoh/memoh',
      appPath: '/opt/Memoh/resources/app',
      resourcesPath: '/opt/Memoh/resources',
      isPackaged: false,
      homeDirectory: fixture.home,
      pathValue: fixture.bin,
      loginShell: shell,
    })

    expect(launchers['claude-agent-acp']).toEqual(expect.objectContaining({
      claudeCodeExecutable: join(managedBin, 'claude'),
    }))
    // codex missed the probe but the fallback PATH directory still finds it.
    expect(launchers['codex-acp']).toEqual(expect.objectContaining({
      codexExecutable: join(fixture.bin, 'codex'),
    }))
  })

  it('ignores a login shell that reports garbage or fails', async () => {
    const fixture = await executableFixture([])
    const shell = join(fixture.home, 'broken-shell')
    await writeFile(shell, '#!/bin/sh\necho nonsense\nexit 1\n', { mode: 0o700 })
    await chmod(shell, 0o700)

    await expect(discoverBundledACPLaunchers({
      platform: 'linux',
      electronExecutable: '/opt/Memoh/memoh',
      appPath: '/opt/Memoh/resources/app',
      resourcesPath: '/opt/Memoh/resources',
      isPackaged: false,
      homeDirectory: fixture.home,
      pathValue: fixture.bin,
      loginShell: shell,
    })).resolves.toEqual({ 'codex-acp': false, 'claude-agent-acp': false })
  })

  it('finds CLIs installed under nvm-managed node without a login shell', async () => {
    const fixture = await executableFixture([])
    const nvmBin = join(fixture.home, '.nvm', 'versions', 'node', 'v22.11.0', 'bin')
    const olderBin = join(fixture.home, '.nvm', 'versions', 'node', 'v20.9.0', 'bin')
    await executable(join(olderBin, 'codex'))
    await executable(join(nvmBin, 'codex'))

    const launchers = await discoverBundledACPLaunchers({
      platform: 'linux',
      electronExecutable: '/opt/Memoh/memoh',
      appPath: '/opt/Memoh/resources/app',
      resourcesPath: '/opt/Memoh/resources',
      isPackaged: false,
      homeDirectory: fixture.home,
      loginShell: false,
    })

    // The newest node version wins, matching the shell's likely resolution.
    expect(launchers['codex-acp']).toEqual(expect.objectContaining({
      codexExecutable: join(nvmBin, 'codex'),
    }))
  })

  it('ignores relative PATH entries', async () => {
    const fixture = await executableFixture([])
    await expect(discoverBundledACPLaunchers({
      platform: 'linux',
      electronExecutable: '/opt/Memoh/memoh',
      appPath: '/opt/Memoh/resources/app',
      resourcesPath: '/opt/Memoh/resources',
      isPackaged: false,
      homeDirectory: fixture.home,
      pathValue: 'relative-bin',
      loginShell: false,
    })).resolves.toEqual({ 'codex-acp': false, 'claude-agent-acp': false })
  })
})

async function executableFixture(names: Array<'codex' | 'claude'>) {
  const home = await realpath(await mkdtemp(join(tmpdir(), 'memoh-desktop-acp-')))
  temporaryDirectories.push(home)
  const bin = join(home, 'custom-bin')
  for (const name of names) {
    await executable(join(bin, name))
  }
  return { home, bin }
}

async function executable(path: string): Promise<void> {
  await mkdir(join(path, '..'), { recursive: true })
  await writeFile(path, '#!/bin/sh\nexit 0\n', { mode: 0o700 })
  await chmod(path, 0o700)
}
