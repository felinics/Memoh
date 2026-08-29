import { arch, hostname, platform } from 'node:os'
import { realpath } from 'node:fs/promises'
import type { IncomingMessage } from 'node:http'

import WebSocket from 'ws'

import { normalizeRuntimeTeamId, validateConfig, type RuntimeClientConfig } from './config.js'
import { bridgeWebSocketToGrpc } from './pipe/grpc-websocket'
import { grpcMessageLimit, startRuntimeGrpcServer } from './service'
import {
  runtimeCapabilities,
  type RuntimeCapability,
} from './core/guards'
import {
  cleanupStaleTrustedACPLaunchers,
  normalizeTrustedACPLaunchers,
  type TrustedACPLaunchers,
} from './core/acp-launchers.js'
import { runtimeClientVersion } from './version'

export const runtimeProtocolGrpc = 'memoh.runtime.v1.grpc'
export const runtimeMetadataHeader = 'X-Memoh-Runtime-Metadata'
// Hosted gateways consume this tenant-routing header before proxying the
// WebSocket. The upstream single-team server intentionally does not require it.
export const runtimeTeamIDHeader = 'X-Team-ID'
export const runtimeMetadataMaxBytes = 8 * 1024
const runtimeHandshakeErrorMaxBytes = 8 * 1024
const runtimeHandshakeErrorReadTimeoutMs = 2_000

export interface RuntimeHandshakeMetadataV1 {
  version: 1
  hostname: string
  os: 'darwin' | 'linux' | 'win32'
  arch: string
  client_version: string
  workspace_base: string
  capabilities: RuntimeCapability[]
}

export interface RuntimeHandshakeHeaders {
  Authorization: string
  'Sec-WebSocket-Protocol': typeof runtimeProtocolGrpc
  'X-Memoh-Runtime-Metadata': string
  'X-Team-ID'?: string
}

/**
 * Trusted launchers may be given as a value or as a provider. A provider is
 * re-resolved on every connection attempt, so hosts like Desktop can pick up
 * a CLI the user installed after the session started without a restart.
 */
export type TrustedACPLaunchersSource =
  | TrustedACPLaunchers
  | (() => Promise<TrustedACPLaunchers | undefined> | TrustedACPLaunchers | undefined)

export interface RuntimeSessionOptions {
  version?: string
  random?: () => number
  onStatus?: (status: RuntimeSessionStatus, error?: string) => void
  warn?: (message: string) => void
  trustedACPLaunchers?: TrustedACPLaunchersSource
}

export type RuntimeSessionStatus = 'connecting' | 'connected' | 'disconnected' | 'stopped'

export function runtimeConnectUrl(serverUrl: string): URL {
  const url = new URL(serverUrl)
  if (url.protocol === 'http:') {
    url.protocol = 'ws:'
  } else if (url.protocol === 'https:') {
    url.protocol = 'wss:'
  }
  const basePath = url.pathname.replace(/\/+$/, '')
  url.pathname = `${basePath}/runtimes/connect`
  url.search = ''
  url.hash = ''
  return url
}

export function assertSecureRuntimeUrl(url: URL, insecureLocalhost = false): void {
  if (url.username || url.password) {
    throw new Error('runtime connection URL must not contain credentials')
  }
  if (url.protocol === 'wss:') {
    return
  }
  const hostname = url.hostname.replace(/^\[|\]$/g, '')
  const local = hostname === '127.0.0.1' || hostname === 'localhost' || hostname === '::1'
  if (url.protocol === 'ws:' && insecureLocalhost && local) {
    return
  }
  throw new Error('runtime connections require wss://; use --insecure-localhost only for localhost development')
}

export function createHandshakeMetadata(
  workspaceBase: string,
  version = runtimeClientVersion,
  machine: Pick<RuntimeHandshakeMetadataV1, 'hostname' | 'os' | 'arch'> = {
    hostname: hostname(),
    os: platform() as RuntimeHandshakeMetadataV1['os'],
    arch: arch(),
  },
  capabilities: readonly RuntimeCapability[] = runtimeCapabilities(),
): RuntimeHandshakeMetadataV1 {
  if (!['darwin', 'linux', 'win32'].includes(machine.os)) {
    throw new Error(`unsupported runtime operating system: ${machine.os}`)
  }
  const workspace = requiredMetadataString(workspaceBase, 'workspace_base', 4_096)
  if (machine.os === 'win32') {
    if (!/^[A-Za-z]:[\\/]/.test(workspace) && !workspace.startsWith('\\\\')) {
      throw new Error('runtime metadata workspace_base must be absolute')
    }
  } else if (!workspace.startsWith('/')) {
    throw new Error('runtime metadata workspace_base must be absolute')
  }
  return {
    version: 1,
    hostname: requiredMetadataString(machine.hostname, 'hostname', 255),
    os: machine.os,
    arch: requiredMetadataString(machine.arch, 'arch', 64),
    client_version: requiredMetadataString(version, 'client_version', 128),
    workspace_base: workspace,
    capabilities: [...capabilities],
  }
}

export function encodeHandshakeMetadata(metadata: RuntimeHandshakeMetadataV1): string {
  const json = Buffer.from(JSON.stringify(metadata), 'utf8')
  if (json.length > runtimeMetadataMaxBytes) {
    throw new Error('runtime handshake metadata exceeds 8 KiB')
  }
  return json.toString('base64url')
}

export function handshakeHeaders(
  config: RuntimeClientConfig,
  version = runtimeClientVersion,
  metadata = createHandshakeMetadata(config.workspaceBase, version),
): RuntimeHandshakeHeaders {
  const headers: RuntimeHandshakeHeaders = {
    Authorization: `Bearer ${config.key.trim()}`,
    'Sec-WebSocket-Protocol': runtimeProtocolGrpc,
    'X-Memoh-Runtime-Metadata': encodeHandshakeMetadata(metadata),
  }
  if (config.teamId !== undefined) {
    headers[runtimeTeamIDHeader] = normalizeRuntimeTeamId(config.teamId)
  }
  return headers
}

export class RuntimeSession {
  private readonly version: string
  private readonly random: () => number
  private readonly onStatus: ((status: RuntimeSessionStatus, error?: string) => void) | undefined
  private readonly warn: ((message: string) => void) | undefined
  private readonly trustedACPLaunchers: TrustedACPLaunchersSource | undefined
  private stopped = false
  private activeController: AbortController | undefined

  constructor(
    private readonly config: RuntimeClientConfig,
    options: RuntimeSessionOptions = {},
  ) {
    validateConfig(config)
    this.version = options.version ?? runtimeClientVersion
    this.random = options.random ?? Math.random
    this.onStatus = options.onStatus
    this.warn = options.warn
    this.trustedACPLaunchers = typeof options.trustedACPLaunchers === 'function'
      ? options.trustedACPLaunchers
      : normalizeTrustedACPLaunchers(options.trustedACPLaunchers)
  }

  async start(signal?: AbortSignal): Promise<void> {
    if (this.activeController) {
      throw new Error('runtime session is already running')
    }
    const controller = new AbortController()
    this.activeController = controller
    const forwardAbort = () => controller.abort()
    signal?.addEventListener('abort', forwardAbort, { once: true })
    if (signal?.aborted) {
      controller.abort()
    }
    try {
      const url = runtimeConnectUrl(this.config.serverUrl)
      assertSecureRuntimeUrl(url, this.config.insecureLocalhost)
      const workspaceBase = await realpath(this.config.workspaceBase)
      await cleanupStaleTrustedACPLaunchers()
      let retry = 1_000
      let lastError: string | undefined

      while (!this.stopped && !controller.signal.aborted) {
        this.onStatus?.('connecting', lastError)
        const attemptStartedAt = Date.now()
        try {
          await this.runConnection(url, workspaceBase, controller.signal)
          lastError = this.stopped || controller.signal.aborted ? undefined : 'runtime connection closed'
        } catch (error) {
          lastError = sanitizeError(error, this.config.key)
        }
        if (this.stopped || controller.signal.aborted) {
          break
        }
        if (Date.now() - attemptStartedAt >= 30_000) {
          retry = 1_000
        }
        this.onStatus?.('disconnected', lastError)
        const jittered = Math.round(retry * (0.8 + this.random() * 0.4))
        await abortableDelay(jittered, controller.signal)
        retry = Math.min(60_000, retry * 2)
      }
      this.onStatus?.('stopped', lastError)
    } finally {
      signal?.removeEventListener('abort', forwardAbort)
      this.activeController = undefined
    }
  }

  stop(): void {
    this.stopped = true
    this.activeController?.abort()
  }

  private async resolveTrustedACPLaunchers(): Promise<TrustedACPLaunchers | undefined> {
    if (typeof this.trustedACPLaunchers !== 'function') {
      return this.trustedACPLaunchers
    }
    try {
      return normalizeTrustedACPLaunchers(await this.trustedACPLaunchers())
    } catch (error) {
      // A broken discovery must not take down the whole computer connection.
      // Explicitly disabling both aliases also blocks ambient PATH fallback,
      // so a failed probe cannot silently widen what the server may launch.
      this.warn?.(`trusted ACP launcher discovery failed: ${error instanceof Error ? error.message : String(error)}`)
      return { 'codex-acp': false, 'claude-agent-acp': false }
    }
  }

  private async runConnection(url: URL, workspaceBase: string, signal?: AbortSignal): Promise<void> {
    let grpc: Awaited<ReturnType<typeof startRuntimeGrpcServer>> | undefined
    let websocket: WebSocket | undefined
    let abort: (() => void) | undefined
    try {
      grpc = await startRuntimeGrpcServer({
        workspaceBase,
        warn: this.warn,
        trustedACPLaunchers: await this.resolveTrustedACPLaunchers(),
      })
      const metadata = createHandshakeMetadata(workspaceBase, this.version, undefined, grpc.capabilities)
      const headers = handshakeHeaders(this.config, this.version, metadata)
      const websocketHeaders: Record<string, string> = {
        Authorization: headers.Authorization,
        [runtimeMetadataHeader]: headers[runtimeMetadataHeader],
      }
      if (headers[runtimeTeamIDHeader]) {
        websocketHeaders[runtimeTeamIDHeader] = headers[runtimeTeamIDHeader]
      }
      websocket = new WebSocket(url, runtimeProtocolGrpc, {
        headers: websocketHeaders,
        handshakeTimeout: 15_000,
        maxPayload: grpcMessageLimit,
        perMessageDeflate: false,
      })
      abort = () => websocket?.terminate()
      signal?.addEventListener('abort', abort, { once: true })
      if (signal?.aborted) {
        websocket.terminate()
      }
      // Preparing the bridge while CONNECTING installs its first-frame
      // listener and open-turn injector before waitForOpen() yields.
      const bridge = bridgeWebSocketToGrpc(websocket, grpc)
      void bridge.catch(() => undefined)
      await waitForOpen(websocket)
      if (websocket.protocol !== runtimeProtocolGrpc) {
        throw new Error(`server selected unexpected runtime protocol: ${websocket.protocol || '<none>'}`)
      }
      const stopWatchdog = startWatchdog(websocket)
      try {
        this.onStatus?.('connected')
        await bridge
      } finally {
        stopWatchdog()
      }
    } finally {
      if (abort) {
        signal?.removeEventListener('abort', abort)
      }
      websocket?.terminate()
      await grpc?.close()
    }
  }

}

function waitForOpen(websocket: WebSocket): Promise<void> {
  return new Promise((resolve, reject) => {
    const cleanup = () => {
      websocket.off('open', onOpen)
      websocket.off('error', onError)
      websocket.off('close', onClose)
      websocket.off('unexpected-response', onUnexpectedResponse)
    }
    const onOpen = () => {
      cleanup()
      resolve()
    }
    const onError = (error: Error) => {
      cleanup()
      reject(error)
    }
    const onClose = () => {
      cleanup()
      reject(new Error('runtime WebSocket closed during handshake'))
    }
    const onUnexpectedResponse = (_request: unknown, response: IncomingMessage) => {
      cleanup()
      void readHandshakeError(response).then((detail) => {
        const suffix = detail ? `: ${detail}` : ''
        reject(new Error(`runtime handshake rejected with HTTP ${response.statusCode ?? 'unknown'}${suffix}`))
      })
    }
    websocket.once('open', onOpen)
    websocket.once('error', onError)
    websocket.once('close', onClose)
    websocket.once('unexpected-response', onUnexpectedResponse)
  })
}

function readHandshakeError(response: IncomingMessage): Promise<string> {
  return new Promise((resolve) => {
    const chunks: Buffer[] = []
    let size = 0
    let settled = false

    const cleanup = () => {
      clearTimeout(timer)
      response.off('data', onData)
      response.off('end', finish)
      response.off('error', finish)
      response.off('aborted', finish)
      response.off('close', finish)
    }
    const finish = () => {
      if (settled) return
      settled = true
      cleanup()
      response.resume()
      resolve(
        Buffer.concat(chunks)
          .toString('utf8')
          .replace(/\s+/g, ' ')
          .trim(),
      )
    }
    const onData = (chunk: Buffer | Uint8Array | string) => {
      if (size >= runtimeHandshakeErrorMaxBytes) return
      const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
      const remaining = runtimeHandshakeErrorMaxBytes - size
      const accepted = buffer.subarray(0, remaining)
      chunks.push(accepted)
      size += accepted.length
      if (size >= runtimeHandshakeErrorMaxBytes) {
        finish()
      }
    }

    const timer = setTimeout(finish, runtimeHandshakeErrorReadTimeoutMs)
    timer.unref()
    response.on('data', onData)
    response.once('end', finish)
    response.once('error', finish)
    response.once('aborted', finish)
    response.once('close', finish)
  })
}

function startWatchdog(websocket: WebSocket): () => void {
  let lastPong = Date.now()
  let lastTick = lastPong
  const onPong = () => {
    lastPong = Date.now()
  }
  websocket.on('pong', onPong)
  const timer = setInterval(() => {
    const now = Date.now()
    const clockJumped = now - lastTick > 45_000 || now < lastTick
    lastTick = now
    if (clockJumped || now - lastPong > 30_000) {
      websocket.terminate()
      return
    }
    if (websocket.readyState === WebSocket.OPEN) {
      websocket.ping()
    }
  }, 20_000)
  timer.unref()
  return () => {
    clearInterval(timer)
    websocket.off('pong', onPong)
  }
}

function abortableDelay(milliseconds: number, signal?: AbortSignal): Promise<void> {
  if (signal?.aborted) {
    return Promise.resolve()
  }
  return new Promise(resolve => {
    const finish = () => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }
    const timer = setTimeout(finish, milliseconds)
    const onAbort = () => {
      clearTimeout(timer)
      finish()
    }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

function requiredMetadataString(value: string, name: string, maxLength: number): string {
  const normalized = value.trim()
  if (!normalized || Buffer.byteLength(normalized, 'utf8') > maxLength || normalized.includes('\0')) {
    throw new Error(`runtime metadata ${name} must contain 1-${maxLength} bytes without NUL`)
  }
  return normalized
}

function sanitizeError(error: unknown, key: string): string {
  const message = error instanceof Error ? error.message : String(error)
  return message
    .replaceAll(key, '<redacted>')
    .replace(/mrk_[0-9a-f]{64}/g, '<redacted>')
    .slice(0, 1_024)
}
