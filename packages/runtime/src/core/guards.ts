import { constants } from 'node:fs'
import { access, stat } from 'node:fs/promises'
import { posix, win32 } from 'node:path'

import { status } from '@grpc/grpc-js'

import { rpcError } from '../rpc'
import type { TrustedACPAdapterName } from './acp-launchers.js'

const blockedNames = new Set([
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
])

const inheritedExactNames = new Set([
  'HOME',
  'USER',
  'LOGNAME',
  'TMPDIR',
  'TMP',
  'TEMP',
  'LANG',
  'LANGUAGE',
  'LC_ALL',
  'LC_CTYPE',
  'LC_MESSAGES',
  'TERM',
  'COLORTERM',
  'TZ',
  'USERNAME',
  'USERPROFILE',
  'HOMEDRIVE',
  'HOMEPATH',
  'APPDATA',
  'LOCALAPPDATA',
  'PROGRAMDATA',
  'SYSTEMDRIVE',
  'SYSTEMROOT',
  'WINDIR',
  'COMSPEC',
  'PATHEXT',
])

const validEnvironmentName = /^[A-Za-z_][A-Za-z0-9_]*$/

const baseRuntimeCapabilities = ['fs', 'exec', 'host_fs'] as const

const acpAdapterCapabilities = [
  { command: 'codex-acp', capability: 'acp_codex' },
  { command: 'claude-agent-acp', capability: 'acp_claude_code' },
] as const

export type RuntimeCapability =
  | typeof baseRuntimeCapabilities[number]
  | typeof acpAdapterCapabilities[number]['capability']

type ACPAdapterProbe = (
  candidate: string,
  adapter: TrustedACPAdapterName,
) => Promise<boolean>

export interface ACPAdapterAvailability {
  configuredAdapters: readonly TrustedACPAdapterName[]
  availableAdapters: readonly TrustedACPAdapterName[]
}

export function runtimeCapabilities(): RuntimeCapability[] {
  return [...baseRuntimeCapabilities]
}

// ACP capabilities are advisory. The Server still rechecks the adapter when
// it starts a session, while this probe lets it avoid offering agents that are
// absent from the connected computer. Reuse the same narrowed PATH exposed to
// Remote Runtime commands rather than inspecting arbitrary process entries.
export async function detectRuntimeCapabilities(
  source: NodeJS.ProcessEnv = process.env,
  os: NodeJS.Platform = process.platform,
  isAvailable: ACPAdapterProbe = isAvailableACPAdapter,
  trustedAvailability?: ACPAdapterAvailability,
): Promise<RuntimeCapability[]> {
  const capabilities = runtimeCapabilities()
  if (os !== 'darwin' && os !== 'linux') {
    return capabilities
  }

  const safePath = inheritedEnvironment(source, os).PATH ?? ''
  const directories = safePath.split(posix.delimiter).filter(Boolean)
  const configured = new Set(trustedAvailability?.configuredAdapters ?? [])
  const available = new Set(trustedAvailability?.availableAdapters ?? [])
  for (const adapter of acpAdapterCapabilities) {
    if (configured.has(adapter.command)) {
      if (available.has(adapter.command)) {
        capabilities.push(adapter.capability)
      }
      continue
    }
    for (const directory of directories) {
      if (await isAvailable(posix.join(directory, adapter.command), adapter.command)) {
        capabilities.push(adapter.capability)
        break
      }
    }
  }
  return capabilities
}

export interface GuardedEnvironmentOptions {
  clean?: boolean
  unset?: readonly string[]
  // Trusted launcher directories are prepended to PATH so the fixed ACP
  // aliases resolve to Runtime-owned shims. This is name resolution, not
  // confinement: Exec still runs any server-supplied command as the user, so
  // Remote ACP inherits the full-shell trust model of the exec capability
  // (see docs/design/remote-acp.md, "Trust model").
  trustedExecutableDirectories?: readonly string[]
}

export function guardedEnvironment(
  requested: readonly string[] = [],
  options: GuardedEnvironmentOptions = {},
): NodeJS.ProcessEnv {
  const environment = options.clean ? {} : inheritedEnvironment(process.env)
  unsetEnvironment(environment, options.unset ?? [])
  for (const assignment of requested) {
    const separator = assignment.indexOf('=')
    if (separator <= 0 || assignment.includes('\0')) {
      throw rpcError(status.INVALID_ARGUMENT, 'environment entries must use NAME=value')
    }
    const name = assignment.slice(0, separator)
    const value = assignment.slice(separator + 1)
    if (!validEnvironmentName.test(name)) {
      throw rpcError(status.INVALID_ARGUMENT, `invalid environment variable name: ${name}`)
    }
    assertSafeEnvironmentName(name)
    environment[process.platform === 'win32' ? name.toUpperCase() : name] = value
  }
  prependTrustedExecutableDirectories(
    environment,
    options.trustedExecutableDirectories ?? [],
    process.platform,
  )
  return environment
}

function unsetEnvironment(environment: NodeJS.ProcessEnv, requested: readonly string[]): void {
  const exact = new Set<string>()
  const prefixes: string[] = []
  for (const item of requested) {
    const name = item.trim()
    const wildcard = name.endsWith('*')
    const value = wildcard ? name.slice(0, -1) : name
    if (!value || !validEnvironmentName.test(value) || (name.includes('*') && !wildcard)) {
      throw rpcError(status.INVALID_ARGUMENT, `invalid environment variable name: ${item}`)
    }
    if (wildcard) {
      prefixes.push(value)
    } else {
      exact.add(value)
    }
  }
  for (const name of Object.keys(environment)) {
    if (exact.has(name) || prefixes.some(prefix => name.startsWith(prefix))) {
      delete environment[name]
    }
  }
}

export function inheritedEnvironment(
  source: NodeJS.ProcessEnv,
  os: NodeJS.Platform = process.platform,
): NodeJS.ProcessEnv {
  const environment: NodeJS.ProcessEnv = {}
  let inheritedPath: string | undefined
  for (const [name, value] of Object.entries(source)) {
    if (value === undefined || value.includes('\0')) {
      continue
    }
    const normalized = name.toUpperCase()
    if (normalized === 'PATH') {
      inheritedPath = value
      continue
    }
    if (inheritedExactNames.has(normalized) || normalized.startsWith('LC_')) {
      environment[normalized] = value
    }
  }
  environment.PATH = safeInheritedPath(inheritedPath, os)
  environment.SHELL = os === 'win32' ? 'cmd.exe' : '/bin/sh'
  return environment
}

export function assertSafeEnvironmentName(name: string): void {
  const normalized = name.toUpperCase()
  if (
    blockedNames.has(normalized)
    || normalized.startsWith('NODE_')
    || normalized.startsWith('ELECTRON_')
    || normalized.startsWith('LD_')
    || normalized.startsWith('DYLD_')
  ) {
    throw rpcError(status.PERMISSION_DENIED, `environment variable ${name} is not allowed`)
  }
}

function safeInheritedPath(value: string | undefined, os: NodeJS.Platform): string {
  if (!value || value.includes('\0')) {
    return defaultPath(os)
  }
  const paths = os === 'win32' ? win32 : posix
  const entries = value
    .split(paths.delimiter)
    .filter(entry => entry.length > 0 && paths.isAbsolute(entry))
  return entries.length > 0 ? [...new Set(entries)].join(paths.delimiter) : defaultPath(os)
}

function prependTrustedExecutableDirectories(
  environment: NodeJS.ProcessEnv,
  directories: readonly string[],
  os: NodeJS.Platform,
): void {
  if (directories.length === 0) {
    return
  }
  const paths = os === 'win32' ? win32 : posix
  const trusted: string[] = []
  for (const directory of directories) {
    if (
      !directory
      || directory.includes('\0')
      || directory.includes('\r')
      || directory.includes('\n')
      || !paths.isAbsolute(directory)
    ) {
      throw new Error('trusted executable directories must be safe absolute paths')
    }
    trusted.push(directory)
  }
  const inherited = (environment.PATH ?? '')
    .split(paths.delimiter)
    .filter(Boolean)
  environment.PATH = [...new Set([...trusted, ...inherited])].join(paths.delimiter)
}

function defaultPath(os: NodeJS.Platform): string {
  if (os === 'win32') {
    return String.raw`C:\Windows\System32;C:\Windows`
  }
  return os === 'darwin'
    ? '/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin'
    : '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
}

async function isExecutableFile(candidate: string): Promise<boolean> {
  try {
    const entry = await stat(candidate)
    if (!entry.isFile()) {
      return false
    }
    await access(candidate, constants.X_OK)
    return true
  } catch {
    return false
  }
}

async function isAvailableACPAdapter(
  candidate: string,
  _adapter: TrustedACPAdapterName,
): Promise<boolean> {
  return await isExecutableFile(candidate)
}
