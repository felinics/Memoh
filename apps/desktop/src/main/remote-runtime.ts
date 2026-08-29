import { chmod, mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { dirname } from 'node:path'

import {
  RuntimeSession,
  normalizeRuntimeTeamId,
  normalizeRuntimeServerUrl,
  validateRuntimeKey,
  type RuntimeClientConfig,
  type RuntimeSessionOptions,
  type RuntimeSessionStatus,
  type TrustedACPLaunchersSource,
} from '@memohai/runtime'

import type {
  DesktopRuntimeConfig,
  DesktopRuntimeState,
} from '../shared/remote-runtime'

const configVersion = 1
const runtimeIDPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i

interface StoredRuntimeConfig {
  version: typeof configVersion
  runtimeId: string
  runtimeName?: string
  serverUrl: string
  teamId?: string
  encryptedKey: string
}

export interface RuntimeEncryption {
  isAvailable(): boolean
  encrypt(value: string): Buffer
  decrypt(value: Buffer): string
}

export interface ManagedRuntimeSession {
  start(): Promise<void>
  stop(): void
}

export type RuntimeSessionFactory = (
  config: RuntimeClientConfig,
  options: RuntimeSessionOptions,
) => ManagedRuntimeSession

export interface DesktopRemoteRuntimeManagerOptions {
  configPath: string
  currentServerUrl: () => string
  workspaceBase: string
  deviceName: string
  encryption: RuntimeEncryption
  trustedACPLaunchers?: TrustedACPLaunchersSource
  createSession?: RuntimeSessionFactory
  warn?: (message: string, error?: unknown) => void
}

export class DesktopRemoteRuntimeManager {
  private readonly listeners = new Set<(state: DesktopRuntimeState) => void>()
  private readonly createSession: RuntimeSessionFactory
  private state: DesktopRuntimeState
  private configuredRuntimeID: string | undefined
  private configuredRuntimeName: string | undefined
  private activeSession: ManagedRuntimeSession | undefined
  private activeRun: Promise<void> | undefined
  private activeToken: object | undefined
  private pendingOperation: Promise<void> = Promise.resolve()

  constructor(private readonly options: DesktopRemoteRuntimeManagerOptions) {
    this.createSession = options.createSession ?? ((config, sessionOptions) => (
      new RuntimeSession(config, sessionOptions)
    ))
    this.state = {
      enabled: false,
      status: 'disabled',
      deviceName: normalizedDeviceName(options.deviceName),
    }
  }

  runtimeState(): DesktopRuntimeState {
    return { ...this.state }
  }

  onStateChanged(listener: (state: DesktopRuntimeState) => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  restore(): Promise<DesktopRuntimeState> {
    return this.enqueue(async () => {
      await this.stopActiveSession()

      let stored: StoredRuntimeConfig | undefined
      try {
        stored = parseStoredConfig(await readFile(this.options.configPath, 'utf8'))
      } catch (error) {
        if (nodeErrorCode(error) === 'ENOENT') {
          this.configuredRuntimeID = undefined
          this.configuredRuntimeName = undefined
          return this.updateState({ enabled: false, status: 'disabled' })
        }
        this.configuredRuntimeID = undefined
        this.configuredRuntimeName = undefined
        return this.updateState({
          enabled: true,
          status: 'error',
          error: 'This computer\'s saved connection could not be read',
        })
      }

      this.configuredRuntimeID = stored.runtimeId
      this.configuredRuntimeName = normalizedRuntimeName(stored.runtimeName, this.state.deviceName)
      let currentServerUrl: string
      try {
        currentServerUrl = normalizeDesktopServerUrl(this.options.currentServerUrl())
      } catch {
        return this.updateState({
          enabled: true,
          runtimeId: stored.runtimeId,
          status: 'error',
          error: 'The current server URL is invalid',
        })
      }
      if (normalizeDesktopServerUrl(stored.serverUrl) !== currentServerUrl) {
        return this.updateState({
          enabled: true,
          runtimeId: stored.runtimeId,
          status: 'error',
          error: 'This computer is connected to a different server',
        })
      }
      if (!this.options.encryption.isAvailable()) {
        return this.updateState({
          enabled: true,
          runtimeId: stored.runtimeId,
          status: 'error',
          error: 'Secure storage is unavailable on this computer',
        })
      }

      let key: string
      try {
        key = this.options.encryption.decrypt(decodeEncryptedKey(stored.encryptedKey))
        validateRuntimeKey(key)
      } catch {
        return this.updateState({
          enabled: true,
          runtimeId: stored.runtimeId,
          status: 'error',
          error: 'This computer\'s saved connection could not be unlocked',
        })
      }

      try {
        this.startSession(stored.runtimeId, currentServerUrl, key, stored.teamId)
      } catch (error) {
        return this.updateState({
          enabled: true,
          runtimeId: stored.runtimeId,
          status: 'error',
          error: sanitizeError(error, key),
        })
      }
      return this.runtimeState()
    })
  }

  configure(config: DesktopRuntimeConfig | null): Promise<DesktopRuntimeState> {
    const input = config ? { ...config } : null
    return this.enqueue(async () => {
      if (!input) {
        await this.stopActiveSession()
        await rm(this.options.configPath, { force: true })
        this.configuredRuntimeID = undefined
        this.configuredRuntimeName = undefined
        return this.updateState({ enabled: false, status: 'disabled' })
      }

      const runtimeId = validateRuntimeID(input.runtimeId)
      const runtimeName = normalizedRuntimeName(input.name)
      const key = input.key.trim()
      validateRuntimeKey(key)
      const teamId = input.teamId === undefined
        ? undefined
        : normalizeRuntimeTeamId(input.teamId)
      if (!this.options.encryption.isAvailable()) {
        throw new Error('secure credential storage is unavailable')
      }
      const serverUrl = normalizeDesktopServerUrl(this.options.currentServerUrl())
      const prepared = this.prepareSession(runtimeId, serverUrl, key, teamId)
      const stored: StoredRuntimeConfig = {
        version: configVersion,
        runtimeId,
        runtimeName,
        serverUrl,
        teamId,
        encryptedKey: this.options.encryption.encrypt(key).toString('base64'),
      }
      await writeStoredConfig(this.options.configPath, stored)

      await this.stopActiveSession()
      this.configuredRuntimeID = runtimeId
      this.configuredRuntimeName = runtimeName
      this.activateSession(prepared, runtimeId, key)
      return this.runtimeState()
    })
  }

  stop(): Promise<DesktopRuntimeState> {
    return this.enqueue(async () => {
      await this.stopActiveSession()
      if (!this.configuredRuntimeID) {
        return this.updateState({ enabled: false, status: 'disabled' })
      }
      return this.updateState({
        enabled: true,
        runtimeId: this.configuredRuntimeID,
        runtimeName: this.configuredRuntimeName,
        status: 'stopped',
      })
    })
  }

  private prepareSession(runtimeId: string, serverUrl: string, key: string, teamId?: string): {
    session: ManagedRuntimeSession
    token: object
  } {
    const token = {}
    const session = this.createSession(
      {
        serverUrl,
        key,
        teamId,
        workspaceBase: this.options.workspaceBase,
        insecureLocalhost: isInsecureLocalhost(serverUrl),
      },
      {
        onStatus: (status, error) => this.handleSessionStatus(token, runtimeId, key, status, error),
        warn: message => this.options.warn?.(message),
        trustedACPLaunchers: this.options.trustedACPLaunchers,
      },
    )
    return { session, token }
  }

  private startSession(runtimeId: string, serverUrl: string, key: string, teamId?: string): void {
    this.activateSession(this.prepareSession(runtimeId, serverUrl, key, teamId), runtimeId, key)
  }

  private activateSession(
    prepared: { session: ManagedRuntimeSession, token: object },
    runtimeId: string,
    key: string,
  ): void {
    this.activeSession = prepared.session
    this.activeToken = prepared.token
    this.updateState({ enabled: true, runtimeId, status: 'connecting' })
    const run = Promise.resolve()
      .then(() => prepared.session.start())
      .catch((error) => {
        if (this.activeToken !== prepared.token) return
        this.updateState({
          enabled: true,
          runtimeId,
          status: 'error',
          error: sanitizeError(error, key),
        })
      })
      .finally(() => {
        if (this.activeToken !== prepared.token) return
        this.activeSession = undefined
        this.activeRun = undefined
        this.activeToken = undefined
      })
    this.activeRun = run
  }

  private handleSessionStatus(
    token: object,
    runtimeId: string,
    key: string,
    status: RuntimeSessionStatus,
    error?: string,
  ): void {
    if (this.activeToken !== token) return
    this.updateState({
      enabled: true,
      runtimeId,
      status,
      error: error ? sanitizeError(error, key) : undefined,
    })
  }

  private async stopActiveSession(): Promise<void> {
    const session = this.activeSession
    const run = this.activeRun
    this.activeSession = undefined
    this.activeRun = undefined
    this.activeToken = undefined
    session?.stop()
    if (run) {
      await run.catch(error => this.options.warn?.('failed to stop desktop runtime session', error))
    }
  }

  private updateState(next: Omit<DesktopRuntimeState, 'deviceName'>): DesktopRuntimeState {
    const state: DesktopRuntimeState = {
      ...next,
      runtimeName: next.enabled ? (next.runtimeName ?? this.configuredRuntimeName) : undefined,
      deviceName: this.state.deviceName,
    }
    if (sameState(this.state, state)) return this.runtimeState()
    this.state = state
    for (const listener of this.listeners) {
      try {
        listener(this.runtimeState())
      } catch (error) {
        this.options.warn?.('desktop runtime state listener failed', error)
      }
    }
    return this.runtimeState()
  }

  private enqueue(operation: () => Promise<DesktopRuntimeState>): Promise<DesktopRuntimeState> {
    const result = this.pendingOperation.then(operation, operation)
    this.pendingOperation = result.then(() => undefined, () => undefined)
    return result
  }
}

async function writeStoredConfig(path: string, config: StoredRuntimeConfig): Promise<void> {
  await mkdir(dirname(path), { recursive: true })
  await writeFile(path, `${JSON.stringify(config, null, 2)}\n`, { mode: 0o600 })
  await chmod(path, 0o600)
}

function parseStoredConfig(raw: string): StoredRuntimeConfig {
  const parsed = JSON.parse(raw) as Partial<StoredRuntimeConfig> | null
  if (!parsed || parsed.version !== configVersion) {
    throw new Error('unsupported desktop runtime config version')
  }
  const runtimeId = validateRuntimeID(parsed.runtimeId)
  if (typeof parsed.serverUrl !== 'string') {
    throw new Error('desktop runtime server URL is missing')
  }
  const serverUrl = normalizeDesktopServerUrl(parsed.serverUrl)
  if (typeof parsed.encryptedKey !== 'string') {
    throw new Error('desktop runtime credential is missing')
  }
  if (parsed.teamId !== undefined && typeof parsed.teamId !== 'string') {
    throw new Error('desktop runtime team ID is invalid')
  }
  const teamId = parsed.teamId === undefined
    ? undefined
    : normalizeRuntimeTeamId(parsed.teamId)
  decodeEncryptedKey(parsed.encryptedKey)
  return {
    version: configVersion,
    runtimeId,
    runtimeName: typeof parsed.runtimeName === 'string'
      ? normalizedRuntimeName(parsed.runtimeName)
      : undefined,
    serverUrl,
    teamId,
    encryptedKey: parsed.encryptedKey,
  }
}

function decodeEncryptedKey(value: string): Buffer {
  if (!value || !/^[A-Za-z0-9+/]+={0,2}$/.test(value)) {
    throw new Error('desktop runtime credential is invalid')
  }
  const decoded = Buffer.from(value, 'base64')
  if (!decoded.length || decoded.toString('base64') !== value) {
    throw new Error('desktop runtime credential is invalid')
  }
  return decoded
}

function validateRuntimeID(value: unknown): string {
  const runtimeId = typeof value === 'string' ? value.trim().toLowerCase() : ''
  if (!runtimeIDPattern.test(runtimeId)) {
    throw new Error('runtimeId must be a UUID')
  }
  return runtimeId
}

function normalizeDesktopServerUrl(value: string): string {
  const normalized = normalizeRuntimeServerUrl(value)
  const url = new URL(normalized)
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) {
    throw new Error('desktop runtime server URL must use http or https without credentials')
  }
  return normalized
}

function isInsecureLocalhost(serverUrl: string): boolean {
  const url = new URL(serverUrl)
  const host = url.hostname.replace(/^\[|\]$/g, '')
  return url.protocol === 'http:' && ['localhost', '127.0.0.1', '::1'].includes(host)
}

function normalizedDeviceName(value: string): string {
  return value.trim() || 'Memoh Desktop'
}

function normalizedRuntimeName(value: unknown, fallback = ''): string {
  const name = typeof value === 'string' ? value.trim() : ''
  const fallbackName = fallback.trim()
  if (name) return name
  if (fallbackName) return fallbackName
  throw new Error('computer name is required')
}

function sanitizeError(error: unknown, key: string): string {
  const message = error instanceof Error ? error.message : String(error)
  return key ? message.replaceAll(key, '[redacted]') : message
}

function sameState(left: DesktopRuntimeState, right: DesktopRuntimeState): boolean {
  return left.enabled === right.enabled
    && left.runtimeId === right.runtimeId
    && left.runtimeName === right.runtimeName
    && left.status === right.status
    && left.deviceName === right.deviceName
    && left.error === right.error
}

function nodeErrorCode(error: unknown): string | undefined {
  return error && typeof error === 'object' && 'code' in error
    ? String((error as { code?: unknown }).code)
    : undefined
}
