import { execFileSync } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  applyEnvFile,
  buildElectronBuilderArgs,
  hasMacNotarizationEnv,
  isEnabled,
  missingRequiredMacSigningEnv,
  stripPublishArgs,
} from './build-env.mjs'

const desktopRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const envPath = resolve(desktopRoot, '.env')

if (existsSync(envPath)) {
  applyEnvFile(readFileSync(envPath, 'utf8'))
}

const xcodeDeveloperDirCandidates = [
  process.env.DEVELOPER_DIR,
  '/Applications/Xcode_26.4.1.app/Contents/Developer',
  '/Applications/Xcode_26.4.app/Contents/Developer',
  '/Applications/Xcode_26.3.app/Contents/Developer',
  '/Applications/Xcode_26.2.app/Contents/Developer',
  '/Applications/Xcode_26.1.1.app/Contents/Developer',
  '/Applications/Xcode_26.1.app/Contents/Developer',
  '/Applications/Xcode_26.0.1.app/Contents/Developer',
  '/Applications/Xcode_26.0.app/Contents/Developer',
  '/Applications/Xcode.app/Contents/Developer',
].filter(Boolean)

const xcodeDeveloperDir = xcodeDeveloperDirCandidates.find(candidate => (
  existsSync(resolve(candidate, 'usr/bin/actool'))
))

const rawArgs = process.argv.slice(2)
const marker = rawArgs.indexOf('--')
const target = marker >= 0 ? rawArgs[0] : rawArgs.find(arg => arg === 'current' || /^(darwin|linux|win32)(?:-|$)/.test(arg))
const forwardedBuilderArgs = marker >= 0
  ? rawArgs.slice(marker + 1)
  : rawArgs.filter(arg => arg !== 'current' && !/^(darwin|linux|win32)(?:-|$)/.test(arg))
const builderArgs = [
  ...stripPublishArgs(forwardedBuilderArgs),
  ...buildElectronBuilderArgs(process.env),
]
const isMacBuild = process.platform === 'darwin'
  && (target === 'current' || target?.startsWith('darwin') || builderArgs.includes('--mac'))
const macToolchainEnv = isMacBuild && xcodeDeveloperDir
  ? { DEVELOPER_DIR: xcodeDeveloperDir }
  : {}

let temporarySecretsDirectory = ''

function quoteWindowsArg(value) {
  if (/^[A-Za-z0-9_/:=.,+\-]+$/.test(value)) {
    return value
  }
  return `"${value.replaceAll('"', '\\"')}"`
}

function runPnpm(args, extraEnv = {}) {
  if (process.platform === 'win32') {
    run('cmd.exe', ['/d', '/s', '/c', ['pnpm', ...args].map(quoteWindowsArg).join(' ')], extraEnv)
    return
  }
  run('pnpm', args, extraEnv)
}

function run(command, args, extraEnv = {}) {
  execFileSync(command, args, {
    cwd: desktopRoot,
    stdio: 'inherit',
    env: {
      ...process.env,
      ...extraEnv,
    },
  })
}

function materializeAppleApiKey() {
  const rawKey = process.env.APPLE_API_KEY?.trim()
  if (!rawKey || existsSync(rawKey)) return

  const keyBody = rawKey.includes('BEGIN PRIVATE KEY')
    ? rawKey.replaceAll('\\n', '\n')
    : Buffer.from(rawKey, 'base64').toString('utf8')
  if (!keyBody.includes('BEGIN PRIVATE KEY')) {
    throw new Error('APPLE_API_KEY must be a .p8 path, PEM text, or base64-encoded PEM')
  }

  temporarySecretsDirectory = mkdtempSync(resolve(tmpdir(), 'memoh-desktop-signing-'))
  const keyPath = resolve(
    temporarySecretsDirectory,
    `AuthKey_${process.env.APPLE_API_KEY_ID || 'desktop'}.p8`,
  )
  writeFileSync(keyPath, keyBody, { mode: 0o600 })
  process.env.APPLE_API_KEY = keyPath
}

function prepareSigningEnvironment() {
  if (!isMacBuild) return

  if (!process.env.CSC_LINK && process.env.APPLE_CERTIFICATE) {
    process.env.CSC_LINK = process.env.APPLE_CERTIFICATE
  }
  if (!process.env.CSC_KEY_PASSWORD && process.env.APPLE_CERTIFICATE_PASSWORD) {
    process.env.CSC_KEY_PASSWORD = process.env.APPLE_CERTIFICATE_PASSWORD
  }

  if (!process.env.CSC_LINK) {
    process.env.CSC_IDENTITY_AUTO_DISCOVERY = 'false'
  }

  const missing = missingRequiredMacSigningEnv(process.env)
  if (isEnabled(process.env.MEMOH_DESKTOP_REQUIRE_MAC_SIGNING) && missing.length > 0) {
    throw new Error(`Missing required macOS signing/notarization environment variables:\n- ${missing.join('\n- ')}`)
  }

  materializeAppleApiKey()
  if (hasMacNotarizationEnv(process.env)) {
    builderArgs.push('-c.mac.notarize=true')
  }
}

try {
  prepareSigningEnvironment()
  runPnpm(['--filter', '@memohai/runtime', 'build'])
  runPnpm(['exec', 'electron-vite', 'build'])
  runPnpm(['exec', 'electron-builder', ...builderArgs], macToolchainEnv)
} finally {
  if (temporarySecretsDirectory) {
    rmSync(temporarySecretsDirectory, { recursive: true, force: true })
  }
}
