import { lstat } from 'node:fs/promises'
import { homedir } from 'node:os'
import { dirname, join, resolve } from 'node:path'

import { ensureDirectory, readPrivateFile, writeFileAtomic } from './secure-files'
export { ensureDirectory, writeFileAtomic } from './secure-files'

import { normalizeRuntimeTeamId, validateConfig } from './config'
import { assertSecureRuntimeUrl, runtimeConnectUrl, normalizeRuntimeServerUrl } from './server-url'

export const runtimeEnrollmentSchemaVersion = 1

// The server identifies a runtime by its key alone, so the enrollment carries
// nothing else that names it.
export interface RuntimeEnrollment {
  schemaVersion: typeof runtimeEnrollmentSchemaVersion
  serverUrl: string
  key: string
  teamId?: string
  insecureLocalhost: boolean
}

export interface RuntimePaths {
  home: string
  runtimeHome: string
  configPath: string
  logsDir: string
  serviceDir: string
  launchdPlistPath: string
  systemdUnitPath: string
  windowsTaskXMLPath: string
}

export interface RuntimePathOptions {
  home?: string
}

export function resolveRuntimePaths(options: RuntimePathOptions = {}): RuntimePaths {
  const home = resolve(options.home ?? homedir())
  const runtimeHome = join(home, '.memoh', 'runtime')
  const configPath = join(home, '.memoh', 'runtime.json')
  const serviceDir = join(runtimeHome, 'service')
  return {
    home,
    runtimeHome,
    configPath,
    logsDir: join(runtimeHome, 'logs'),
    serviceDir,
    launchdPlistPath: join(home, 'Library', 'LaunchAgents', 'ai.memoh.runtime.plist'),
    systemdUnitPath: join(serviceDir, 'memoh-runtime.service'),
    windowsTaskXMLPath: join(serviceDir, 'memoh-runtime-task.xml'),
  }
}

export function normalizeRuntimeEnrollment(input: {
  serverUrl: string
  key: string
  teamId?: string
  insecureLocalhost?: boolean
}, workspaceBase = homedir()): RuntimeEnrollment {
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
  assertSecureRuntimeUrl(runtimeConnectUrl(rawServerUrl), insecureLocalhost)
  const serverUrl = normalizeRuntimeServerUrl(rawServerUrl)
  return {
    schemaVersion: runtimeEnrollmentSchemaVersion,
    serverUrl,
    key,
    teamId,
    insecureLocalhost,
  }
}

export async function readRuntimeEnrollment(path: string, workspaceBase = homedir()): Promise<RuntimeEnrollment> {
  let raw: string
  try {
    raw = await readPrivateFile(path)
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
  if (record.teamId !== undefined && typeof record.teamId !== 'string') {
    throw new Error(`runtime configuration at ${path} has an invalid teamId`)
  }
  if (record.insecureLocalhost !== undefined && typeof record.insecureLocalhost !== 'boolean') {
    throw new Error(`runtime configuration at ${path} has an invalid insecureLocalhost value`)
  }
  try {
    return normalizeRuntimeEnrollment({
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

export function sameEnrollment(left: RuntimeEnrollment, right: RuntimeEnrollment): boolean {
  return left.serverUrl === right.serverUrl && left.key === right.key
    && left.teamId === right.teamId && left.insecureLocalhost === right.insecureLocalhost
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
