import type { NavigationGuardReturn, RouteLocationNormalized, Router } from 'vue-router'

const INTERNAL_ORIGIN = 'https://memoh.invalid'
const MAX_RETURN_TO_LENGTH = 2048

// Auth boundaries, external OAuth callbacks, reverse-proxied backend endpoints,
// dev tools, and bearer-credential fragment deep links (`/providers/connect#payload=...`)
// must never become post-login redirects.
const DISALLOWED_PREFIXES = [
  '/login',
  '/oauth',
  '/connect',
  '/onboarding',
  '/providers/connect',
  '/api',
  '/dev',
] as const

/** Lowercase pathname with non-root trailing slashes stripped, matching Vue Router's default (`sensitive: false, strict: false`). */
export function normalizeRoutePath(pathname: string): string {
  const lower = pathname.toLowerCase()
  return lower.length > 1 ? lower.replace(/\/+$/, '') || '/' : lower
}

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
    if (url.origin !== INTERNAL_ORIGIN || !url.pathname.startsWith('/') || url.pathname.includes('//')) return null
    const canonical = normalizeRoutePath(url.pathname)
    if (DISALLOWED_PREFIXES.some(p => canonical === p || canonical.startsWith(`${p}/`))) return null
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

export function buildLoginRedirect(target?: unknown): { path: '/login', query?: { return_to: string } } {
  const returnTo = toReturnToParam(target)
  return returnTo ? { path: '/login', query: { return_to: returnTo } } : { path: '/login' }
}

export function buildOnboardingRedirect(target?: unknown): { path: '/onboarding', query?: { return_to: string } } {
  const returnTo = toReturnToParam(target)
  return returnTo ? { path: '/onboarding', query: { return_to: returnTo } } : { path: '/onboarding' }
}

export function shouldRedirectOnUnauthorized(currentPath: unknown): boolean {
  if (typeof currentPath !== 'string' || !currentPath) return true
  const path = normalizeRoutePath(currentPath)
  return path !== '/login' && path !== '/connect' && path !== '/oauth' && !path.startsWith('/oauth/')
}

function resolveActiveReturnTarget(router?: Pick<Router, 'currentRoute'> | null): unknown {
  const current = router?.currentRoute?.value
  if (current && (!Array.isArray(current.matched) || current.matched.length > 0)) {
    const path = normalizeRoutePath(current.path)
    return path === '/login' || path === '/onboarding' ? current.query?.return_to : current.fullPath
  }
  if (typeof window !== 'undefined' && (window.location.protocol === 'http:' || window.location.protocol === 'https:')) {
    const path = normalizeRoutePath(window.location.pathname)
    return path === '/login' || path === '/onboarding'
      ? new URLSearchParams(window.location.search).get('return_to')
      : `${window.location.pathname}${window.location.search}${window.location.hash}`
  }
  return undefined
}

export function loginRedirectForCurrentRoute(
  router?: Pick<Router, 'currentRoute'> | null,
): { path: '/login', query?: { return_to: string } } {
  return buildLoginRedirect(resolveActiveReturnTarget(router))
}

export function onboardingRedirectForCurrentRoute(
  router?: Pick<Router, 'currentRoute'> | null,
): { path: '/onboarding', query?: { return_to: string } } {
  return buildOnboardingRedirect(resolveActiveReturnTarget(router))
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
  const path = normalizeRoutePath(to.path)
  if (path === '/login') return token ? sanitizeReturnTo(to.query.return_to) : true
  if (path === '/oauth' || path.startsWith('/oauth/')) return true
  if (!token) return buildLoginRedirect(path === '/onboarding' ? to.query.return_to : to.fullPath)
  if (to.meta.adminOnly && String(deps.readUserRole()).toLowerCase() !== 'admin') {
    return { name: 'bots' }
  }

  if (path === '/onboarding') {
    if (deps.hasOnboardingInProgress()) return true
    const completed = await deps.ensureOnboarding()
    if (!deps.readToken()) return buildLoginRedirect(to.query.return_to)
    return completed ? sanitizeReturnTo(to.query.return_to) : true
  }

  const completed = await deps.ensureOnboarding()
  if (!deps.readToken()) return buildLoginRedirect(to.fullPath)
  return completed ? true : buildOnboardingRedirect(to.fullPath)
}
