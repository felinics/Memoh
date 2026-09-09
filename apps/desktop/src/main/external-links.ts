// Decides what the main process does with a navigation the renderer tries to
// start. Kept free of Electron imports so the policy itself is unit-testable.

const EXTERNAL_PROTOCOLS = ['http:', 'https:', 'mailto:']

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
}

export function resolveNavigationGuardAction(input: NavigationGuardInput): NavigationGuardAction {
  // Subframes are content, not the app shell. The workspace browser panel renders
  // the proxied workspace site in an <iframe>, and `will-redirect` — unlike
  // `will-navigate` — is emitted for every frame, so guarding subframes here would
  // cancel the iframe load and hand the page to the OS browser on any 3xx.
  // Framing rules stay where they belong: the iframe's own `sandbox` attribute.
  if (!input.isMainFrame) return { kind: 'allow' }
  if (input.isTrustedRenderer) return { kind: 'allow' }

  const external = normalizeExternalUrl(input.url)
  if (!external.supported) return { kind: 'block', url: external.url || input.url }
  return { kind: 'external', url: external.url }
}
