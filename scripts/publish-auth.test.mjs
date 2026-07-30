import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

import { detectPublishAuthMode } from './publish-auth.mjs'

const ROOT_DIR = dirname(dirname(fileURLToPath(import.meta.url)))

test('prefers GitHub OIDC over the setup-node token placeholder', () => {
  assert.equal(detectPublishAuthMode({
    ACTIONS_ID_TOKEN_REQUEST_URL: 'https://github.example.test/oidc',
    ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'github-oidc-request-token',
    NODE_AUTH_TOKEN: 'XXXXX-XXXXX-XXXXX-XXXXX',
  }), 'oidc')
})

test('prefers GitHub OIDC when a token is also present', () => {
  assert.equal(detectPublishAuthMode({
    ACTIONS_ID_TOKEN_REQUEST_URL: 'https://github.example.test/oidc',
    ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'github-oidc-request-token',
    NODE_AUTH_TOKEN: 'manual-publish-token',
  }), 'oidc')
})

test('uses token mode outside an OIDC environment', () => {
  assert.equal(detectPublishAuthMode({
    NODE_AUTH_TOKEN: 'manual-publish-token',
  }), 'token')
})

test('does not accept an incomplete OIDC environment', () => {
  assert.equal(detectPublishAuthMode({
    ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'github-oidc-request-token',
  }), 'none')
})

test('publish script skips token preflight when setup-node placeholder and OIDC coexist', () => {
  const tempDir = mkdtempSync(join(tmpdir(), 'memoh-publish-auth-'))
  const fakeBin = join(tempDir, 'bin')
  const npmLog = join(tempDir, 'npm.log')
  const pnpmLog = join(tempDir, 'pnpm.log')

  try {
    mkdirSync(fakeBin)

    const fakeNpm = join(fakeBin, 'npm')
    writeFileSync(fakeNpm, `#!/bin/sh
printf '%s\\n' "$*" >> "$PUBLISH_TEST_NPM_LOG"
[ "$1" = "view" ] && exit 1
exit 91
`)
    chmodSync(fakeNpm, 0o755)

    const fakePnpm = join(fakeBin, 'pnpm')
    writeFileSync(fakePnpm, `#!/bin/sh
printf '%s\\n' "$*" >> "$PUBLISH_TEST_PNPM_LOG"
exit 0
`)
    chmodSync(fakePnpm, 0o755)

    const result = spawnSync(process.execPath, ['scripts/publish-packages.mjs'], {
      cwd: ROOT_DIR,
      encoding: 'utf8',
      env: {
        ...process.env,
        ACTIONS_ID_TOKEN_REQUEST_URL: 'https://github.example.test/oidc',
        ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'github-oidc-request-token',
        NODE_AUTH_TOKEN: 'XXXXX-XXXXX-XXXXX-XXXXX',
        NPM_PUBLISH_SCOPE: '@memohai',
        PATH: `${fakeBin}:${process.env.PATH}`,
        PUBLISH_TEST_NPM_LOG: npmLog,
        PUBLISH_TEST_PNPM_LOG: pnpmLog,
      },
    })

    assert.equal(result.status, 0, result.stderr || result.stdout)
    assert.match(result.stdout, /OIDC trusted publishing: skipping token preflight/)

    const npmCalls = readFileSync(npmLog, 'utf8')
    assert.doesNotMatch(npmCalls, /^whoami$/m)
    assert.doesNotMatch(npmCalls, /^access /m)

    const pnpmCalls = readFileSync(pnpmLog, 'utf8')
    assert.match(pnpmCalls, /publish apps\/desktop --access public --no-git-checks --provenance/)
  } finally {
    rmSync(tempDir, { recursive: true, force: true })
  }
})
