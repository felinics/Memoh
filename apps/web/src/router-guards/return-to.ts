import type {
  NavigationGuardReturn,
  RouteLocationNormalized,
  Router,
} from 'vue-router'

const INTERNAL_ORIGIN = 'https://memoh.invalid'
const MAX_RETURN_TO_LENGTH = 2048

// Auth boundaries, external OAuth callbacks, reverse-proxied backend endpoints,
// and bearer-credential fragment deep links (`/providers/connect#payload=...`)
// must never become post-login redirects: the first group loops or replays
// stale handshakes, `/api` bypasses SPA views, and putting `#payload=` into
// `?return_to=` would move an API key from the URL fragment into a
// network-visible query parameter.
const DISALLOWED_RETURN_PREFIXES = [
  '/login',
  '/oauth',
  '/connect',
  '/onboarding',
  '/providers/connect',
  '/api',
] as const

function hasAsciiControlChar(value: string): boolean {
  for (let i = 0; i < value.length; i++) {
    const code = value.charCodeAt(i)
    if (code <= 0x1F || code === 0x7F) return true
  }
  return false
}

function isDisallowedReturnPathname(pathname: string): boolean {
  return DISALLOWED_RETURN_PREFIXES.some(
    prefix => pathname === prefix || pathname.startsWith(`${prefix}/`),
  )
}

/**
 * Validate an untrusted `return_to` value and return a normalized in-app
 * relative destination (`pathname + search + hash`), or `null` when invalid
 * or non-returnable.
 */
export function parseReturnTo(raw: unknown): string | null {
  const value = Array.isArray(raw) ? raw[0] : raw
  if (typeof value !== 'string' || !value || value.length > MAX_RETURN_TO_LENGTH) return null
  // Reject anything not starting with a single `/`, scheme-relative `//`,
  // backslashes (`/\evil.example` normalizes to `//evil.example` in browsers),
  // and ASCII control characters (`/\t/evil.example` strips `\t` in WHATWG URL).
  if (!value.startsWith('/') || value.startsWith('//') || value.includes('\\') || hasAsciiControlChar(value)) {
    return null
  }
  let parsed: URL
  try {
    parsed = new URL(value, INTERNAL_ORIGIN)
  } catch {
    return null
  }
  if (parsed.origin !== INTERNAL_ORIGIN) return null
  // Dot-segment collapse can turn `/..//evil.example` into `pathname === '//evil.example'`.
  if (!parsed.pathname.startsWith('/') || parsed.pathname.startsWith('//')) return null
  if (isDisallowedReturnPathname(parsed.pathname)) return null

  let decodedPathname: string
  try {
    decodedPathname = decodeURIComponent(parsed.pathname)
  } catch {
    return null
  }
  if (
    !decodedPathname.startsWith('/')
    || decodedPathname.startsWith('//')
    || decodedPathname.includes('\\')
    || hasAsciiControlChar(decodedPathname)
  ) {
    return null
  }
  let decodedResolved: URL
  try {
    decodedResolved = new URL(decodedPathname, INTERNAL_ORIGIN)
  } catch {
    return null
  }
  if (
    decodedResolved.origin !== INTERNAL_ORIGIN
    || !decodedResolved.pathname.startsWith('/')
    || decodedResolved.pathname.startsWith('//')
    || isDisallowedReturnPathname(decodedResolved.pathname)
  ) {
    return null
  }

  return `${parsed.pathname}${parsed.search}${parsed.hash}`
}

/**
 * Return a safe in-app `return_to` destination, falling back to `'/'`.
 */
export function sanitizeReturnTo(raw: unknown): string {
  return parseReturnTo(raw) ?? '/'
}

/**
 * Return a non-root in-app `return_to` query value, or `undefined` when the
 * target is `'/'` or invalid so `/login` and `/onboarding` omit `?return_to=%2F`.
 */
export function toReturnToParam(raw: unknown): string | undefined {
  const safe = parseReturnTo(raw)
  return safe && safe !== '/' ? safe : undefined
}

export interface LoginRedirectLocation {
  name: 'Login'
  query?: { return_to: string }
}

export function buildLoginRedirect(targetFullPath?: unknown): LoginRedirectLocation {
  const returnTo = toReturnToParam(targetFullPath)
  return returnTo
    ? { name: 'Login', query: { return_to: returnTo } }
    : { name: 'Login' }
}

export interface OnboardingRedirectLocation {
  path: '/onboarding'
  query?: { return_to: string }
}

export function buildOnboardingRedirect(targetFullPath?: unknown): OnboardingRedirectLocation {
  const returnTo = toReturnToParam(targetFullPath)
  return returnTo
    ? { path: '/onboarding', query: { return_to: returnTo } }
    : { path: '/onboarding' }
}

/**
 * Resolve the active destination to preserve across an unexpected 401 redirect.
 * Before the initial navigation commits (`currentRoute === START_LOCATION`),
 * fall back to the browser URL under web history so cold-start 401s on deep
 * links keep their target.
 */
export function currentReturnToTarget(
  router?: Pick<Router, 'currentRoute'> | null,
): string | undefined {
  const current = router?.currentRoute?.value
  const isCommitted = !!current && (!Array.isArray(current.matched) || current.matched.length > 0)
  if (isCommitted) {
    if (current.path === '/onboarding') {
      return toReturnToParam(current.query?.return_to)
    }
    return current.fullPath
  }
  if (typeof window !== 'undefined' && (window.location.protocol === 'http:' || window.location.protocol === 'https:')) {
    return `${window.location.pathname}${window.location.search}${window.location.hash}`
  }
  return current?.fullPath
}

export interface AuthRouteGuardDeps {
  readToken: () => string | null
  readUserRole: () => string
  hasOnboardingInProgress: () => boolean
  ensureOnboarding: () => Promise<boolean>
}

export async function guardAuthenticatedRoute(
  to: Pick<RouteLocationNormalized, 'path' | 'fullPath' | 'query' | 'meta'>,
  deps: AuthRouteGuardDeps,
): Promise<NavigationGuardReturn> {
  const token = deps.readToken()

  if (to.path === '/login') {
    return token ? sanitizeReturnTo(to.query.return_to) : true
  }
  if (to.path.startsWith('/oauth/')) {
    return true
  }
  if (!token) {
    return buildLoginRedirect(to.fullPath)
  }
  if (to.meta.adminOnly) {
    if (String(deps.readUserRole()).toLowerCase() !== 'admin') {
      return { name: 'bots' }
    }
  }

  if (to.path === '/onboarding') {
    if (deps.hasOnboardingInProgress()) return true
    const completed = await deps.ensureOnboarding()
    if (!deps.readToken()) {
      return buildLoginRedirect(to.query.return_to)
    }
    return completed ? sanitizeReturnTo(to.query.return_to) : true
  }

  const completed = await deps.ensureOnboarding()
  if (!deps.readToken()) {
    return buildLoginRedirect(to.fullPath)
  }
  if (!completed) {
    return buildOnboardingRedirect(to.fullPath)
  }

  return true
}
