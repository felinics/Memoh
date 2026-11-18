import {
  buildLoginRedirect,
  sanitizeReturnTo,
  shouldRedirectOnUnauthorized,
} from '@memohai/web/router-guards/return-to'

export { shouldRedirectOnUnauthorized }

export interface PostConnectNavigationInput {
  changed: boolean
  hasToken: boolean
  returnTo: unknown
}

export interface SessionCleanupStorage {
  removeItem(key: string): void
}

export interface SessionCleanupClearableStorage {
  clear(): void
}

export function isCurrentServerProbe(probedBaseUrl: string, currentBaseUrl: string): boolean {
  return probedBaseUrl === currentBaseUrl
}

export function applyPostConnectSessionCleanup(
  clearAuth: boolean,
  deps: {
    exitLogin: () => void
    localStorage: SessionCleanupStorage
    sessionStorage: SessionCleanupClearableStorage
  },
): void {
  if (!clearAuth) return
  deps.exitLogin()
  deps.localStorage.removeItem('user')
  deps.sessionStorage.clear()
}

export type PostConnectNavigation =
  | { clearAuth: true, animateLogin: true, destination: { path: '/login' } }
  | { clearAuth: false, animateLogin: true, destination: { path: '/login', query?: { return_to: string } } }
  | { clearAuth: false, animateLogin: false, destination: string }

export function decidePostConnectNavigation(
  input: PostConnectNavigationInput,
): PostConnectNavigation {
  if (input.changed) {
    return { clearAuth: true, animateLogin: true, destination: { path: '/login' } }
  }
  if (!input.hasToken) {
    return { clearAuth: false, animateLogin: true, destination: buildLoginRedirect(input.returnTo) }
  }
  return { clearAuth: false, animateLogin: false, destination: sanitizeReturnTo(input.returnTo) }
}
