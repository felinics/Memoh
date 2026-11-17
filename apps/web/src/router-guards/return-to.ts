import type { NavigationGuardReturn, RouteLocationNormalized, Router } from 'vue-router'

const INTERNAL_ORIGIN = 'https://memoh.invalid'
const MAX_RETURN_TO_LENGTH = 2048

// Auth boundaries, external OAuth callbacks, reverse-proxied backend endpoints,
// and bearer-credential fragment deep links (`/providers/connect#payload=...`)
// must never become post-login redirects.
const DISALLOWED_PREFIXES = [
  '/login',
  '/oauth',
  '/connect',
  '/onboarding',
  '/providers/connect',
  '/api',
] as const

function isSafePathInput(value: string): boolean {
  if (!value.startsWith('/') || value.startsWith('//') || value.includes('\\')) return false
  for (let i = 0; i < value.length; i++) {
    const code = value.charCodeAt(i)
    if (code <= 0x1F || code === 0x7F) return false
  }
  return true
}

function parseAllowedUrl(input: string): URL | null {
  if (!isSafePathInput(input)) return null
  try {
    const url = new URL(input, INTERNAL_ORIGIN)
    if (url.origin !== INTERNAL_ORIGIN || !url.pathname.startsWith('/') || url.pathname.startsWith('//')) return null
    if (DISALLOWED_PREFIXES.some(p => url.pathname === p || url.pathname.startsWith(`${p}/`))) return null
    return url
  } catch {
    return null
  }
}

/** Return a non-root in-app `return_to` value, or `undefined` when `'/'` or invalid. */
export function toReturnToParam(raw: unknown): string | undefined {
  const value = Array.isArray(raw) ? raw[0] : raw
  if (typeof value !== 'string' || !value || value.length > MAX_RETURN_TO_LENGTH) return undefined
  const parsed = parseAllowedUrl(value)
  if (!parsed) return undefined
  try {
    if (!parseAllowedUrl(decodeURIComponent(parsed.pathname))) return undefined
  } catch {
    return undefined
  }
  const normalized = `${parsed.pathname}${parsed.search}${parsed.hash}`
  return normalized === '/' ? undefined : normalized
}

/** Return a safe in-app `return_to` destination, falling back to `'/'`. */
export function sanitizeReturnTo(raw: unknown): string {
  return toReturnToParam(raw) ?? '/'
}

export function buildLoginRedirect(target?: unknown): { name: 'Login', query?: { return_to: string } } {
  const returnTo = toReturnToParam(target)
  return returnTo ? { name: 'Login', query: { return_to: returnTo } } : { name: 'Login' }
}

export function buildOnboardingRedirect(target?: unknown): { path: '/onboarding', query?: { return_to: string } } {
  const returnTo = toReturnToParam(target)
  return returnTo ? { path: '/onboarding', query: { return_to: returnTo } } : { path: '/onboarding' }
}

/**
 * Build the login redirect for an unexpected 401. Before the initial route
 * commits (`matched.length === 0`), read `window.location` on web history.
 */
export function loginRedirectForCurrentRoute(
  router?: Pick<Router, 'currentRoute'> | null,
): { name: 'Login', query?: { return_to: string } } {
  const current = router?.currentRoute?.value
  if (current && (!Array.isArray(current.matched) || current.matched.length > 0)) {
    const target = current.path === '/onboarding' ? current.query?.return_to : current.fullPath
    return buildLoginRedirect(target)
  }
  if (typeof window !== 'undefined' && (window.location.protocol === 'http:' || window.location.protocol === 'https:')) {
    return buildLoginRedirect(`${window.location.pathname}${window.location.search}${window.location.hash}`)
  }
  return buildLoginRedirect(current?.fullPath)
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
  if (to.path === '/login') return token ? sanitizeReturnTo(to.query.return_to) : true
  if (to.path.startsWith('/oauth/')) return true
  if (!token) return buildLoginRedirect(to.fullPath)
  if (to.meta.adminOnly && String(deps.readUserRole()).toLowerCase() !== 'admin') {
    return { name: 'bots' }
  }

  if (to.path === '/onboarding') {
    if (deps.hasOnboardingInProgress()) return true
    const completed = await deps.ensureOnboarding()
    if (!deps.readToken()) return buildLoginRedirect(to.query.return_to)
    return completed ? sanitizeReturnTo(to.query.return_to) : true
  }

  const completed = await deps.ensureOnboarding()
  if (!deps.readToken()) return buildLoginRedirect(to.fullPath)
  return completed ? true : buildOnboardingRedirect(to.fullPath)
}
