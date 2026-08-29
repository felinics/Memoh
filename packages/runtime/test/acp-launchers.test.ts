import { execFile } from 'node:child_process'
import { constants } from 'node:fs'
import {
  access,
  chmod,
  mkdtemp,
  rm,
  stat,
  writeFile,
} from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { promisify } from 'node:util'

import { afterEach, describe, expect, it } from 'vitest'

import {
  cleanupStaleTrustedACPLaunchers,
  normalizeTrustedACPLaunchers,
  prepareTrustedACPLaunchers,
} from '../src/core/acp-launchers'

const execFileAsync = promisify(execFile)
const roots: string[] = []

afterEach(async () => {
  await Promise.all(roots.splice(0).map(root => rm(root, { recursive: true, force: true })))
})

describe('trusted ACP launchers', () => {
  it('starts the fixed adapter with only its local CLI path and caller argv', async () => {
    if (process.platform === 'win32') return
    const root = await temporaryDirectory()
    const adapter = join(root, 'adapter.cjs')
    await writeFile(adapter, `
process.stdout.write(JSON.stringify({
  argv: process.argv.slice(2),
  electronRunAsNode: process.env.ELECTRON_RUN_AS_NODE,
  codexPath: process.env.CODEX_PATH,
  claudePath: process.env.CLAUDE_CODE_EXECUTABLE,
}))
`)
    const codex = await executableFixture(root, 'codex')
    const prepared = await prepareTrustedACPLaunchers({
      'codex-acp': {
        nodeExecutable: process.execPath,
        adapterEntry: adapter,
        codexExecutable: codex,
      },
    })
    try {
      expect(prepared.configuredAdapters).toEqual(['codex-acp'])
      expect(prepared.availableAdapters).toEqual(['codex-acp'])
      const directory = prepared.executableDirectories[0]
      expect((await stat(directory)).mode & 0o777).toBe(0o700)
      const shim = join(directory, 'codex-acp')
      await expect(access(shim, constants.X_OK)).resolves.toBeUndefined()

      const result = await execFileAsync(shim, ['argument from server'], {
        env: {
          ...process.env,
          ELECTRON_RUN_AS_NODE: 'ambient',
          CODEX_PATH: '/ambient/codex',
          CLAUDE_CODE_EXECUTABLE: '/ambient/claude',
        },
      })
      expect(JSON.parse(result.stdout)).toEqual({
        argv: ['argument from server'],
        codexPath: codex,
      })
    } finally {
      const directory = prepared.executableDirectories[0]
      await prepared.close()
      await prepared.close()
      await expect(access(directory)).rejects.toMatchObject({ code: 'ENOENT' })
    }
  })

  it('runs an npm-style local CLI entry with Runtime-owned Node', async () => {
    if (process.platform === 'win32') return
    const root = await temporaryDirectory()
    const adapter = join(root, 'adapter.cjs')
    await writeFile(adapter, 'process.stdout.write(process.env.CODEX_PATH ?? \'\')\n')
    const codex = join(root, 'codex.js')
    await writeFile(codex, `#!/usr/bin/env node
process.stdout.write(JSON.stringify({
  argv: process.argv.slice(2),
  electronRunAsNode: process.env.ELECTRON_RUN_AS_NODE,
  codexPath: process.env.CODEX_PATH,
  claudePath: process.env.CLAUDE_CODE_EXECUTABLE,
}))
`, { mode: 0o700 })
    await chmod(codex, 0o700)

    const prepared = await prepareTrustedACPLaunchers({
      'codex-acp': {
        nodeExecutable: process.execPath,
        adapterEntry: adapter,
        codexExecutable: codex,
      },
    })
    try {
      const adapterResult = await execFileAsync(join(prepared.executableDirectories[0], 'codex-acp'))
      const localCLI = adapterResult.stdout.trim()
      const cliResult = await execFileAsync(localCLI, ['app-server'], {
        env: {
          ...process.env,
          ELECTRON_RUN_AS_NODE: 'ambient',
          CODEX_PATH: '/ambient/codex',
          CLAUDE_CODE_EXECUTABLE: '/ambient/claude',
        },
      })
      expect(JSON.parse(cliResult.stdout)).toEqual({ argv: ['app-server'] })
    } finally {
      await prepared.close()
    }
  })

  it('rejects arbitrary aliases, fields, environment, and relative paths', () => {
    const absolute = process.execPath
    const valid = {
      nodeExecutable: absolute,
      adapterEntry: absolute,
      codexExecutable: absolute,
    }
    const invalid = [
      { 'other-acp': valid },
      { 'codex-acp': { ...valid, nodeExecutable: 'relative-command' } },
      { 'codex-acp': { ...valid, claudeCodeExecutable: absolute } },
      { 'codex-acp': { ...valid, env: { CODEX_PATH: '/tmp/codex' } } },
    ]
    for (const launchers of invalid) {
      expect(() => normalizeTrustedACPLaunchers(launchers as never)).toThrow()
    }
  })

  it('cleans only launcher directories owned by dead Runtime processes', async () => {
    const stale = await mkdtemp(join(tmpdir(), 'memoh-runtime-acp-99999999-'))
    const live = await mkdtemp(join(tmpdir(), `memoh-runtime-acp-${process.pid}-`))
    const unrelated = await mkdtemp(join(tmpdir(), 'memoh-runtime-other-'))
    roots.push(stale, live, unrelated)

    await cleanupStaleTrustedACPLaunchers()

    await expect(access(stale)).rejects.toMatchObject({ code: 'ENOENT' })
    await expect(access(live)).resolves.toBeUndefined()
    await expect(access(unrelated)).resolves.toBeUndefined()
  })
})

async function temporaryDirectory(): Promise<string> {
  const root = await mkdtemp(join(tmpdir(), 'memoh-runtime-launcher-test-'))
  roots.push(root)
  await chmod(root, 0o700)
  return root
}

async function executableFixture(root: string, name: string): Promise<string> {
  const path = join(root, name)
  await writeFile(path, '#!/bin/sh\nexit 0\n', { mode: 0o700 })
  await chmod(path, 0o700)
  return path
}
