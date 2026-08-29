import { status } from '@grpc/grpc-js'
import { describe, expect, it } from 'vitest'

import {
  assertSafeEnvironmentName,
  detectRuntimeCapabilities,
  guardedEnvironment,
  inheritedEnvironment,
} from '../src/core/guards'

describe('runtime guards', () => {
  it('advertises ACP adapters found on the narrowed POSIX PATH', async () => {
    const probed: string[] = []
    const capabilities = await detectRuntimeCapabilities({
      PATH: '/safe/bin:relative:/other/bin:/safe/bin',
    }, 'linux', async candidate => {
      probed.push(candidate)
      return candidate === '/safe/bin/codex-acp'
        || candidate === '/other/bin/claude-agent-acp'
    })

    expect(capabilities).toEqual([
      'fs',
      'exec',
      'host_fs',
      'acp_codex',
      'acp_claude_code',
    ])
    expect(probed).not.toContain('relative/codex-acp')
    expect(probed).not.toContain('relative/claude-agent-acp')
  })

  it('does not advertise missing ACP adapters or probe them on Windows', async () => {
    const missing = await detectRuntimeCapabilities({ PATH: '/safe/bin' }, 'darwin', async () => false)
    expect(missing).toEqual(['fs', 'exec', 'host_fs'])

    let probes = 0
    const windows = await detectRuntimeCapabilities({
      Path: String.raw`C:\Tools;C:\Windows`,
      PATHEXT: '.COM;.EXE;.BAT;.CMD',
    }, 'win32', async () => {
      probes++
      return true
    })
    expect(windows).toEqual(['fs', 'exec', 'host_fs'])
    expect(probes).toBe(0)
  })

  it('uses trusted adapter availability instead of ambient PATH for configured aliases', async () => {
    const probed: string[] = []
    const capabilities = await detectRuntimeCapabilities(
      { PATH: '/ambient/bin' },
      'linux',
      async candidate => {
        probed.push(candidate)
        return true
      },
      {
        configuredAdapters: ['codex-acp', 'claude-agent-acp'],
        availableAdapters: ['codex-acp'],
      },
    )

    expect(capabilities).toContain('acp_codex')
    expect(capabilities).not.toContain('acp_claude_code')
    expect(probed).toEqual([])
  })

  it('keeps a Runtime-owned executable path in clean and unset environments', () => {
    if (process.platform === 'win32') return
    expect(guardedEnvironment([], {
      clean: true,
      unset: ['PATH'],
      trustedExecutableDirectories: ['/private/runtime-shims'],
    })).toEqual({ PATH: '/private/runtime-shims' })
  })

  it.each([
    'LD_PRELOAD',
    'DYLD_INSERT_LIBRARIES',
    'NODE_OPTIONS',
    'BASH_ENV',
    'ENV',
    'PATH',
    'SHELL',
    'COMSPEC',
    'SYSTEMROOT',
    'WINDIR',
    'PATHEXT',
    'IFS',
    'MEMOH_RUNTIME_KEY',
    'ELECTRON_RUN_AS_NODE',
    'CODEX_PATH',
    'CLAUDE_CODE_EXECUTABLE',
    'NODE_PATH',
    'NODE_DEBUG',
    'ELECTRON_NO_ATTACH_CONSOLE',
  ])(
    'rejects dangerous environment variable %s',
    name => {
      expect(() => assertSafeEnvironmentName(name)).toThrow(expect.objectContaining({ code: status.PERMISSION_DENIED }))
    },
  )

  it('inherits only the explicit shell allowlist and strips secrets case-insensitively', () => {
    const environment = inheritedEnvironment({
      HOME: '/home/alice',
      USER: 'alice',
      LANG: 'en_US.UTF-8',
      LC_TIME: 'C',
      TMPDIR: '/tmp/alice',
      PATH: '/custom/bin::relative:/usr/bin',
      MEMOH_RUNTIME_KEY: 'mrk_secret',
      node_options: '--require malware.js',
      Bash_Env: '/tmp/startup.sh',
      LD_PRELOAD: '/tmp/inject.so',
      DYLD_INSERT_LIBRARIES: '/tmp/inject.dylib',
      AWS_SECRET_ACCESS_KEY: 'secret',
      GITHUB_TOKEN: 'secret',
      OPENAI_API_KEY: 'secret',
      DATABASE_URL: 'postgres://secret',
      UNLISTED_VALUE: 'must not leak',
    }, 'linux')

    expect(environment).toMatchObject({
      HOME: '/home/alice',
      USER: 'alice',
      LANG: 'en_US.UTF-8',
      LC_TIME: 'C',
      TMPDIR: '/tmp/alice',
      SHELL: '/bin/sh',
    })
    expect(environment.PATH).toContain('/custom/bin')
    expect(environment.PATH).not.toContain('relative')
    for (const name of [
      'MEMOH_RUNTIME_KEY',
      'node_options',
      'Bash_Env',
      'LD_PRELOAD',
      'DYLD_INSERT_LIBRARIES',
      'AWS_SECRET_ACCESS_KEY',
      'GITHUB_TOKEN',
      'OPENAI_API_KEY',
      'DATABASE_URL',
      'UNLISTED_VALUE',
    ]) {
      expect(environment).not.toHaveProperty(name)
      expect(environment).not.toHaveProperty(name.toUpperCase())
    }
  })

  it('preserves only the Windows environment needed to resolve native commands', () => {
    const environment = inheritedEnvironment({
      Path: String.raw`C:\Tools;;relative;D:\Node`,
      SystemRoot: String.raw`C:\Windows`,
      ComSpec: String.raw`C:\Windows\System32\cmd.exe`,
      PATHEXT: '.COM;.EXE;.BAT;.CMD',
      USERPROFILE: String.raw`C:\Users\alice`,
      MEMOH_RUNTIME_KEY: 'mrk_secret',
      GITHUB_TOKEN: 'secret',
    }, 'win32')

    expect(environment).toMatchObject({
      PATH: String.raw`C:\Tools;D:\Node`,
      SYSTEMROOT: String.raw`C:\Windows`,
      COMSPEC: String.raw`C:\Windows\System32\cmd.exe`,
      PATHEXT: '.COM;.EXE;.BAT;.CMD',
      USERPROFILE: String.raw`C:\Users\alice`,
      SHELL: 'cmd.exe',
    })
    expect(environment).not.toHaveProperty('MEMOH_RUNTIME_KEY')
    expect(environment).not.toHaveProperty('GITHUB_TOKEN')
  })
})
