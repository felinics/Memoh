// Decides what the main process does with a navigation the renderer tries to
// start. Kept free of Electron imports so the policy itself is unit-testable.

const EXTERNAL_PROTOCOLS = ['http:', 'https:', 'mailto:']
const PREVIEW_SUBFRAME_PREFIXES = ['blob:', 'about:srcdoc', 'data:', 'chrome-extension:']
const PREVIEW_ANCESTOR_PREFIXES = ['blob:', 'about:srcdoc', 'about:blank', 'data:', 'chrome-extension:']
const ALLOWED_SUBFRAME_TARGET_PREFIXES = ['blob:', 'about:blank', 'about:srcdoc', 'chrome-extension:']

export interface NormalizedExternalUrl {
  url: string
  protocol: string
  supported: boolean
}

export function normalizeExternalUrl(rawURL: unknown): NormalizedExternalUrl {
  const url = typeof rawURL === 'string' ? rawURL.trim() : ''
  let protocol = ''
  try {
    protocol = new URL(url).protocol
  } catch {
    protocol = ''
  }
  return { url, protocol, supported: EXTERNAL_PROTOCOLS.includes(protocol) }
}

export type NavigationGuardAction =
  | { kind: 'allow' }
  | { kind: 'external', url: string }
  | { kind: 'block', url: string }

export interface NavigationGuardInput {
  url: string
  isMainFrame: boolean
  isTrustedRenderer: boolean
  frameUrl?: string
  initiatorUrl?: string
  ancestorFrameUrls?: readonly string[]
}

function hasAnyUrlPrefix(rawURL: string | undefined, prefixes: readonly string[]): boolean {
  const normalized = (rawURL ?? '').trim().toLowerCase()
  return Boolean(normalized) && prefixes.some(prefix => normalized.startsWith(prefix))
}

export function resolveNavigationGuardAction(input: NavigationGuardInput): NavigationGuardAction {
  if (!input.isMainFrame) {
    if (hasAnyUrlPrefix(input.url, ALLOWED_SUBFRAME_TARGET_PREFIXES)) return { kind: 'allow' }
    if ((input.ancestorFrameUrls ?? []).some(u => !hasAnyUrlPrefix(u, PREVIEW_ANCESTOR_PREFIXES))) return { kind: 'allow' }
    if (!hasAnyUrlPrefix(input.frameUrl, PREVIEW_SUBFRAME_PREFIXES) && !hasAnyUrlPrefix(input.initiatorUrl, PREVIEW_SUBFRAME_PREFIXES)) {
      return { kind: 'allow' }
    }
    const sub = normalizeExternalUrl(input.url)
    return sub.supported ? { kind: 'external', url: sub.url } : { kind: 'block', url: sub.url || input.url }
  }

  if (input.isTrustedRenderer) return { kind: 'allow' }

  const external = normalizeExternalUrl(input.url)
  if (!external.supported) return { kind: 'block', url: external.url || input.url }
  return { kind: 'external', url: external.url }
}

export interface WindowOpenGuardTarget {
  getType: () => string
  setWindowOpenHandler: (handler: (details: { url: string }) => { action: 'deny' }) => void
}

export function createExternalWindowOpenHandler(
  openExternal: (url: string) => void | Promise<unknown>,
  onBlocked?: (url: string) => void,
): (details: { url: string }) => { action: 'deny' } {
  return ({ url }) => {
    const external = normalizeExternalUrl(url)
    if (external.supported) void Promise.resolve(openExternal(external.url)).catch(() => undefined)
    else onBlocked?.(external.url || url)
    return { action: 'deny' }
  }
}

export function attachGuestWindowOpenGuard(
  contents: WindowOpenGuardTarget,
  openExternal: (url: string) => void | Promise<unknown>,
  onBlocked?: (url: string) => void,
): boolean {
  if (contents.getType() === 'window') return false
  contents.setWindowOpenHandler(createExternalWindowOpenHandler(openExternal, onBlocked))
  return true
}
