import { randomUUID } from 'node:crypto'
import {
  chmod,
  lstat,
  mkdir,
  open,
  readFile,
  rename,
  rm,
} from 'node:fs/promises'
import { homedir } from 'node:os'
import { dirname, join, resolve } from 'node:path'

import { normalizeRuntimeTeamId, validateConfig } from './config'
import { normalizeRuntimeServerUrl } from './server-url'

export const runtimeEnrollmentSchemaVersion = 1

const runtimeIDPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i

export interface RuntimeEnrollment {
  schemaVersion: typeof runtimeEnrollmentSchemaVersion
  runtimeId?: string
  serverUrl: string
  key: string
  teamId?: string
  insecureLocalhost: boolean
}

export interface RuntimeInstallManifest {
  schemaVersion: 1
  packageVersion: string
  backend: string
  entryPath: string
  configPath: string
  nodePath: string
  installedAt: string
}

export interface RuntimePaths {
  home: string
  runtimeHome: string
  configPath: string
  versionsDir: string
  manifestPath: string
  logsDir: string
  launchdPlistPath: string
  systemdUnitPath: string
  windowsTaskXMLPath: string
}

export interface RuntimePathOptions {
  home?: string
  env?: NodeJS.ProcessEnv
}

export function resolveRuntimePaths(options: RuntimePathOptions = {}): RuntimePaths {
  const home = resolve(options.home ?? homedir())
  const env = options.env ?? process.env
  const runtimeHome = resolve(nonEmpty(env.MEMOH_RUNTIME_HOME) ?? join(home, '.memoh', 'runtime'))
  const configPath = resolve(nonEmpty(env.MEMOH_RUNTIME_CONFIG) ?? join(home, '.memoh', 'runtime.json'))
  const xdgConfigHome = resolve(nonEmpty(env.XDG_CONFIG_HOME) ?? join(home, '.config'))
  return {
    home,
    runtimeHome,
    configPath,
    versionsDir: join(runtimeHome, 'versions'),
    manifestPath: join(runtimeHome, 'install.json'),
    logsDir: join(runtimeHome, 'logs'),
    launchdPlistPath: join(home, 'Library', 'LaunchAgents', 'ai.memoh.runtime.plist'),
    systemdUnitPath: join(xdgConfigHome, 'systemd', 'user', 'memoh-runtime.service'),
    windowsTaskXMLPath: join(runtimeHome, 'service', 'memoh-runtime-task.xml'),
  }
}

export function normalizeRuntimeEnrollment(input: {
  runtimeId?: string
  serverUrl: string
  key: string
  teamId?: string
  insecureLocalhost?: boolean
}, workspaceBase = homedir()): RuntimeEnrollment {
  const runtimeId = normalizeRuntimeID(input.runtimeId)
  const rawServerUrl = input.serverUrl.trim()
  const key = input.key.trim()
  const teamId = input.teamId === undefined
    ? undefined
    : normalizeRuntimeTeamId(input.teamId)
  const insecureLocalhost = input.insecureLocalhost === true
  validateConfig({
    serverUrl: rawServerUrl,
    key,
    teamId,
    workspaceBase,
    insecureLocalhost,
  })
  const serverUrl = normalizeRuntimeServerUrl(rawServerUrl)
  return {
    schemaVersion: runtimeEnrollmentSchemaVersion,
    runtimeId,
    serverUrl,
    key,
    teamId,
    insecureLocalhost,
  }
}

export async function readRuntimeEnrollment(path: string, workspaceBase = homedir()): Promise<RuntimeEnrollment> {
  let raw: string
  try {
    const info = await lstat(path)
    if (info.isSymbolicLink()) throw new Error('symbolic link')
    raw = await readFile(path, 'utf8')
  } catch (error) {
    if (nodeErrorCode(error) === 'ENOENT') {
      throw new Error(`runtime configuration was not found at ${path}`)
    }
    throw new Error(`runtime configuration could not be read safely at ${path}`)
  }
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    throw new Error(`runtime configuration at ${path} is not valid JSON`)
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error(`runtime configuration at ${path} must be an object`)
  }
  const record = parsed as Record<string, unknown>
  if (record.schemaVersion !== runtimeEnrollmentSchemaVersion) {
    throw new Error(`runtime configuration at ${path} has an unsupported schema version`)
  }
  if (typeof record.serverUrl !== 'string' || typeof record.key !== 'string') {
    throw new Error(`runtime configuration at ${path} is missing serverUrl or key`)
  }
  if (record.runtimeId !== undefined && typeof record.runtimeId !== 'string') {
    throw new Error(`runtime configuration at ${path} has an invalid runtimeId`)
  }
  if (record.teamId !== undefined && typeof record.teamId !== 'string') {
    throw new Error(`runtime configuration at ${path} has an invalid teamId`)
  }
  if (record.insecureLocalhost !== undefined && typeof record.insecureLocalhost !== 'boolean') {
    throw new Error(`runtime configuration at ${path} has an invalid insecureLocalhost value`)
  }
  try {
    return normalizeRuntimeEnrollment({
      runtimeId: record.runtimeId as string | undefined,
      serverUrl: record.serverUrl,
      key: record.key,
      teamId: record.teamId as string | undefined,
      insecureLocalhost: record.insecureLocalhost as boolean | undefined,
    }, workspaceBase)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    throw new Error(`runtime configuration at ${path} is invalid: ${message}`)
  }
}

export async function readRuntimeEnrollmentIfExists(path: string, workspaceBase = homedir()): Promise<RuntimeEnrollment | undefined> {
  try {
    await lstat(path)
  } catch (error) {
    if (nodeErrorCode(error) === 'ENOENT') return undefined
    throw error
  }
  return readRuntimeEnrollment(path, workspaceBase)
}

export async function writeRuntimeEnrollment(path: string, enrollment: RuntimeEnrollment): Promise<void> {
  const normalized = normalizeRuntimeEnrollment(enrollment)
  await ensureDirectory(dirname(path))
  await writeFileAtomic(path, `${JSON.stringify(normalized, null, 2)}\n`, 0o600)
}

export async function readInstallManifest(path: string): Promise<RuntimeInstallManifest | undefined> {
  let raw: string
  try {
    raw = await readFile(path, 'utf8')
  } catch (error) {
    if (nodeErrorCode(error) === 'ENOENT') return undefined
    throw new Error(`runtime install manifest could not be read at ${path}`)
  }
  try {
    const parsed = JSON.parse(raw) as Partial<RuntimeInstallManifest> | null
    if (!parsed || parsed.schemaVersion !== 1 || typeof parsed.packageVersion !== 'string'
      || typeof parsed.backend !== 'string' || typeof parsed.entryPath !== 'string'
      || typeof parsed.configPath !== 'string' || typeof parsed.nodePath !== 'string'
      || typeof parsed.installedAt !== 'string') {
      throw new Error('invalid manifest')
    }
    return parsed as RuntimeInstallManifest
  } catch {
    throw new Error(`runtime install manifest at ${path} is invalid`)
  }
}

export async function writeInstallManifest(path: string, manifest: RuntimeInstallManifest): Promise<void> {
  await ensureDirectory(dirname(path))
  await writeFileAtomic(path, `${JSON.stringify(manifest, null, 2)}\n`, 0o600)
}

export async function writeFileAtomic(path: string, content: string | Uint8Array, mode: number): Promise<void> {
  const directory = dirname(path)
  await mkdir(directory, { recursive: true })
  const temporary = join(directory, `.${randomUUID()}.tmp`)
  const handle = await open(temporary, 'wx', mode)
  try {
    await handle.writeFile(content)
    await handle.sync()
  } finally {
    await handle.close()
  }
  await chmod(temporary, mode)
  try {
    await rename(temporary, path)
  } catch (error) {
    // Windows does not replace an existing destination atomically. Keep the
    // POSIX path atomic, and use the narrow remove-then-rename fallback only
    // for the Windows error shapes.
    if (process.platform !== 'win32' || !['EEXIST', 'EPERM'].includes(nodeErrorCode(error) ?? '')) {
      await rm(temporary, { force: true })
      throw error
    }
    await rm(path, { force: true })
    await rename(temporary, path)
  }
  await chmod(path, mode)
}

// Directories are created private, but existing ones are left as they are:
// ~/.memoh is shared with other Memoh components and --config may point
// anywhere. The 0600 file mode is what protects the enrollment itself.
export async function ensureDirectory(path: string): Promise<void> {
  await mkdir(path, { recursive: true, mode: 0o700 })
  const info = await lstat(path)
  if (!info.isDirectory() || info.isSymbolicLink()) {
    throw new Error(`runtime directory is not a directory: ${path}`)
  }
}

export function sameEnrollment(left: RuntimeEnrollment, right: RuntimeEnrollment): boolean {
  if (left.runtimeId && right.runtimeId) {
    return left.runtimeId === right.runtimeId
      && left.serverUrl === right.serverUrl
      && (!left.teamId || !right.teamId || left.teamId === right.teamId)
  }
  return left.serverUrl === right.serverUrl
    && left.key === right.key
    && left.teamId === right.teamId
}

export function parseBooleanEnvironment(value: string | undefined, name: string): boolean | undefined {
  const normalized = value?.trim().toLowerCase()
  if (!normalized) return undefined
  if (['1', 'true', 'yes', 'on'].includes(normalized)) return true
  if (['0', 'false', 'no', 'off'].includes(normalized)) return false
  throw new Error(`${name} must be true or false`)
}

export function nodeErrorCode(error: unknown): string | undefined {
  return error && typeof error === 'object' && 'code' in error
    ? String((error as { code?: unknown }).code)
    : undefined
}

function normalizeRuntimeID(value: string | undefined): string | undefined {
  const normalized = value?.trim().toLowerCase()
  if (!normalized) return undefined
  if (!runtimeIDPattern.test(normalized)) {
    throw new Error('runtime ID must be a UUID')
  }
  return normalized
}

function nonEmpty(value: string | undefined): string | undefined {
  return value?.trim() || undefined
}
