import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { join, resolve } from 'node:path'
import { test } from 'node:test'

import {
  packagedApplicationPaths,
  shouldVerifyMacSignature,
  smokeRuntimeExecutable,
} from './acp-packaging.mjs'

const desktopRoot = resolve(import.meta.dirname, '..')

test('release pins both Remote ACP adapters exactly', async () => {
  const packageJson = JSON.parse(await readFile(join(desktopRoot, 'package.json'), 'utf8'))
  assert.equal(packageJson.dependencies['@agentclientprotocol/codex-acp'], '1.2.0')
  assert.equal(packageJson.dependencies['@agentclientprotocol/claude-agent-acp'], '0.66.0')
})

test('packaging keeps RUN_AS_NODE enabled and excludes native agent packages', async () => {
  const config = await readFile(join(desktopRoot, 'electron-builder.yml'), 'utf8')
  assert.match(config, /electronFuses:\s*[\s\S]*?runAsNode: true/)
  assert.match(config, /afterPack: scripts\/acp-packaging\.mjs/)
  assert.match(config, /afterSign: scripts\/acp-packaging\.mjs/)
  assert.match(config, /codex-\{darwin,linux,win32\}-\*\{,\/\*\*\/\*\}/)
  assert.match(config, /claude-agent-sdk-\{darwin,linux,win32\}-\*\{,\/\*\*\/\*\}/)
})

test('packaged paths address the real platform executable and resources', () => {
  const appInfo = { productFilename: 'Memoh', sanitizedName: 'memoh' }
  assert.deepEqual(packagedApplicationPaths({
    electronPlatformName: 'darwin',
    appOutDir: '/build/mac-arm64',
    packager: { appInfo },
  }), {
    executable: join('/build/mac-arm64', 'Memoh.app', 'Contents', 'MacOS', 'Memoh'),
    resources: join('/build/mac-arm64', 'Memoh.app', 'Contents', 'Resources'),
  })
  assert.deepEqual(packagedApplicationPaths({
    electronPlatformName: 'linux',
    appOutDir: '/build/linux-unpacked',
    packager: { appInfo, executableName: 'memoh' },
  }), {
    executable: join('/build/linux-unpacked', 'memoh'),
    resources: join('/build/linux-unpacked', 'resources'),
  })
})

test('cross-build smoke uses host Electron while native builds use the target executable', () => {
  const context = {
    electronPlatformName: 'linux',
    appOutDir: '/build/linux-unpacked',
    packager: {
      appInfo: { productFilename: 'Memoh', sanitizedName: 'memoh' },
      executableName: 'memoh',
    },
  }
  assert.equal(smokeRuntimeExecutable(context, 'linux', '/host/electron'), join('/build/linux-unpacked', 'memoh'))
  assert.equal(smokeRuntimeExecutable(context, 'darwin', '/host/electron'), '/host/electron')
})

test('signature verification runs only for an explicitly configured macOS identity', () => {
  assert.equal(shouldVerifyMacSignature({}), false)
  assert.equal(shouldVerifyMacSignature({ CSC_LINK: '  ' }), false)
  assert.equal(shouldVerifyMacSignature({ CSC_LINK: 'certificate.p12' }), true)
})

test('third-party notices state the transitive versions the lockfile installs', async () => {
  const notices = await readFile(join(desktopRoot, 'resources', 'THIRD_PARTY_NOTICES.md'), 'utf8')
  const lockfile = await readFile(join(desktopRoot, '..', '..', 'pnpm-lock.yaml'), 'utf8')
  for (const name of ['@openai/codex', '@anthropic-ai/claude-agent-sdk']) {
    const versions = new Set(
      [...lockfile.matchAll(new RegExp(`'${name}@(\\d+\\.\\d+\\.\\d+)['(]`, 'g'))]
        .map(match => match[1]),
    )
    assert.equal(versions.size, 1, `expected exactly one locked version of ${name}, saw ${[...versions].join(', ')}`)
    const [version] = versions
    assert.ok(
      notices.includes(`\`${name}\` ${version}`),
      `THIRD_PARTY_NOTICES.md must state ${name} ${version}; a lockfile refresh changed the installed version without updating the notice`,
    )
  }
})
