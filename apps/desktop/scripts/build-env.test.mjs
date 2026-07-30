import assert from 'node:assert/strict'
import test from 'node:test'
import {
  applyEnvFile,
  buildElectronBuilderArgs,
  hasMacNotarizationEnv,
  isEnabled,
  missingRequiredMacSigningEnv,
  normalizeDesktopAppId,
  normalizeDesktopVersion,
  normalizeUpdateBaseUrl,
  stripPublishArgs,
} from './build-env.mjs'

test('applyEnvFile fills gaps without overriding the invoking environment', () => {
  const env = { PRESENT: 'ci' }
  applyEnvFile(`
    # comment
    PRESENT=local
    export BASE_URL="https://downloads.example.com/path?a=b"
    SINGLE='value'
  `, env)
  assert.deepEqual(env, {
    PRESENT: 'ci',
    BASE_URL: 'https://downloads.example.com/path?a=b',
    SINGLE: 'value',
  })
})

test('buildElectronBuilderArgs validates and maps desktop build settings', () => {
  assert.deepEqual(buildElectronBuilderArgs({
    MEMOH_DESKTOP_VERSION: 'v1.2.3',
    MEMOH_DESKTOP_APP_ID: 'ai.memoh.cloud.desktop',
    MEMOH_DESKTOP_UPDATE_BASE_URL: 'https://downloads.example.com/memoh/',
  }), [
    '-c.extraMetadata.version=1.2.3',
    '-c.appId=ai.memoh.cloud.desktop',
    '-c.publish.provider=generic',
    '-c.publish.url=https://downloads.example.com/memoh',
    '--publish',
    'never',
  ])
})

test('normalizers reject malformed release metadata', () => {
  assert.equal(normalizeDesktopVersion('v2.0.1-beta.1'), '2.0.1-beta.1')
  assert.throws(() => normalizeDesktopVersion('release-2'), /semantic version/)
  assert.equal(normalizeDesktopAppId('ai.memoh.desktop'), 'ai.memoh.desktop')
  assert.throws(() => normalizeDesktopAppId('Memoh Desktop'), /reverse-DNS/)
  assert.equal(normalizeUpdateBaseUrl('https://cdn.example.com/releases///'), 'https://cdn.example.com/releases')
  assert.throws(() => normalizeUpdateBaseUrl('s3://bucket/releases'), /HTTP\(S\)/)
  assert.throws(() => normalizeUpdateBaseUrl('https://token@example.com/releases'), /without credentials/)
})

test('stripPublishArgs prevents callers from changing build-owned publication policy', () => {
  assert.deepEqual(
    stripPublishArgs(['--mac', '-p', 'always', '--arm64', '--publish=onTagOrDraft', '-p=never']),
    ['--mac', '--arm64'],
  )
})

test('mac signing checks accept electron-builder names and Apple aliases', () => {
  const aliases = {
    APPLE_CERTIFICATE: 'certificate',
    APPLE_CERTIFICATE_PASSWORD: 'password',
    APPLE_API_KEY: 'api-key',
    APPLE_API_KEY_ID: 'key-id',
    APPLE_API_ISSUER: 'issuer',
  }
  assert.deepEqual(missingRequiredMacSigningEnv(aliases), [])
  assert.equal(hasMacNotarizationEnv(aliases), true)

  const electronBuilderNames = {
    CSC_LINK: 'certificate',
    CSC_KEY_PASSWORD: 'password',
    APPLE_API_KEY: 'api-key',
    APPLE_API_KEY_ID: 'key-id',
    APPLE_API_ISSUER: 'issuer',
  }
  assert.deepEqual(missingRequiredMacSigningEnv(electronBuilderNames), [])
  assert.equal(hasMacNotarizationEnv(electronBuilderNames), true)
  assert.deepEqual(missingRequiredMacSigningEnv({}), [
    'APPLE_CERTIFICATE or CSC_LINK',
    'APPLE_CERTIFICATE_PASSWORD or CSC_KEY_PASSWORD',
    'APPLE_API_KEY',
    'APPLE_API_KEY_ID',
    'APPLE_API_ISSUER',
  ])
})

test('isEnabled accepts conventional opt-in values only', () => {
  assert.equal(isEnabled('true'), true)
  assert.equal(isEnabled('YES'), true)
  assert.equal(isEnabled('0'), false)
  assert.equal(isEnabled(undefined), false)
})
