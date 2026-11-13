import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export type RendererTrustOptions = {
  devBaseUrl?: string
  productionEntry: string
}

export const ALLOWED_MAIN_FRAME_PERMISSIONS: ReadonlySet<string> = new Set([
  'clipboard-sanitized-write',
  'fullscreen',
  'media',
])

export type IpcSenderCheckInput = {
  hasChatWindow: boolean
  isChatWindowSender: boolean
  hasSenderFrame: boolean
  isMainFrame: boolean
  senderUrl: string
}

export type PermissionRequestCheckInput = {
  isChatWindowContents: boolean
  isMainFrame: boolean
  permission: string
  mediaTypes?: readonly string[]
  requestingUrl: string
}

export type PermissionCheckInput = {
  isChatWindowContents: boolean
  isMainFrame: boolean
  permission: string
  mediaType?: string
  requestingUrl: string
}

export function isTrustedRendererUrl(rawUrl: string, options: RendererTrustOptions): boolean {
  let candidate: URL
  try {
    candidate = new URL(rawUrl)
  } catch {
    return false
  }

  const devBaseUrl = options.devBaseUrl?.trim()
  if (devBaseUrl) {
    try {
      const entry = new URL('index.html', `${devBaseUrl.replace(/\/+$/, '')}/`)
      return candidate.origin === entry.origin && candidate.pathname === entry.pathname
    } catch {
      return false
    }
  }

  if (candidate.protocol !== 'file:') return false
  try {
    return resolve(fileURLToPath(candidate)) === resolve(options.productionEntry)
  } catch {
    return false
  }
}

export function isTrustedIpcSender(
  input: IpcSenderCheckInput,
  options: RendererTrustOptions,
): boolean {
  if (!input.hasChatWindow || !input.isChatWindowSender) return false
  if (!input.hasSenderFrame || !input.isMainFrame) return false
  return isTrustedRendererUrl(input.senderUrl, options)
}

export function assertTrustedIpcSender(
  input: IpcSenderCheckInput,
  options: RendererTrustOptions,
): void {
  if (!isTrustedIpcSender(input, options)) {
    throw new Error('IPC request rejected from an untrusted renderer')
  }
}

export function isAllowedMainFramePermissionRequest(
  input: PermissionRequestCheckInput,
  options: RendererTrustOptions,
): boolean {
  if (!input.isChatWindowContents || !input.isMainFrame) return false
  if (!ALLOWED_MAIN_FRAME_PERMISSIONS.has(input.permission)) return false
  if (input.permission === 'media') {
    if (!Array.isArray(input.mediaTypes) || input.mediaTypes.length === 0) return false
    if (!input.mediaTypes.every(type => type === 'audio')) return false
  }
  return isTrustedRendererUrl(input.requestingUrl, options)
}

export function isAllowedMainFramePermissionCheck(
  input: PermissionCheckInput,
  options: RendererTrustOptions,
): boolean {
  if (!input.isChatWindowContents || !input.isMainFrame) return false
  if (!ALLOWED_MAIN_FRAME_PERMISSIONS.has(input.permission)) return false
  if (input.permission === 'media' && input.mediaType !== 'audio') return false
  return isTrustedRendererUrl(input.requestingUrl, options)
}
