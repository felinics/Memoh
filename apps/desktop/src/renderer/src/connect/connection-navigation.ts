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

export function shouldRedirectOnUnauthorized(currentPath: unknown): boolean {
  const path = typeof currentPath === 'string' ? currentPath : ''
  return path !== '/connect' && !path.startsWith('/oauth/')
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
  | { clearAuth: true, animateLogin: true, destination: { name: 'Login' } }
  | { clearAuth: false, animateLogin: true, destination: { name: 'Login' } }
  | { clearAuth: false, animateLogin: false, destination: string }

export function decidePostConnectNavigation(
  input: PostConnectNavigationInput,
): PostConnectNavigation {
  if (input.changed) {
    return { clearAuth: true, animateLogin: true, destination: { name: 'Login' } }
  }
  if (!input.hasToken) {
    return { clearAuth: false, animateLogin: true, destination: { name: 'Login' } }
  }
  const returnTo = typeof input.returnTo === 'string'
    && input.returnTo.startsWith('/')
    && !input.returnTo.startsWith('//')
    ? input.returnTo
    : '/'
  return { clearAuth: false, animateLogin: false, destination: returnTo }
}
