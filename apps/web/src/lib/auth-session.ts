export type AuthSessionClearReason = 'login' | 'logout' | 'token-cleared' | 'unauthorized'

export interface AuthSessionClearedDetail {
  reason: AuthSessionClearReason
}

export const AUTH_SESSION_CLEARED_EVENT = 'memoh:auth-session-cleared'

// Per-Tab focus/layout state now lives in sessionStorage (see
// utils/tab-scoped-storage.ts), with a localStorage cold-start seed under the
// SAME key name. So logout must clear BOTH areas for these keys, or the next
// account on this tab inherits the previous user's session focus / layout.
// `chat-input-drafts` and `pinned-bot-ids` remain localStorage-only.
const USER_SCOPED_STORAGE_KEYS = [
  'chat-bot-id',
  'chat-session-id',
  'chat-explicit-selection',
  'chat-draft-intent',
  'chat-input-drafts',
  'pinned-bot-ids',
  // Was 'workspace-tabs' — a long-dead key. The live dockview layout key is
  // 'workspace-layout'; logout never actually cleared it before this fix.
  'workspace-layout',
]

export function clearPersistedUserScopedState() {
  for (const key of USER_SCOPED_STORAGE_KEYS) {
    if (typeof localStorage !== 'undefined') localStorage.removeItem(key)
    if (typeof sessionStorage !== 'undefined') sessionStorage.removeItem(key)
  }
}

export function notifyAuthSessionCleared(reason: AuthSessionClearReason) {
  clearPersistedUserScopedState()

  if (typeof window === 'undefined') return
  window.dispatchEvent(new CustomEvent<AuthSessionClearedDetail>(AUTH_SESSION_CLEARED_EVENT, {
    detail: { reason },
  }))
}

export function onAuthSessionCleared(callback: (detail: AuthSessionClearedDetail) => void) {
  if (typeof window === 'undefined') return () => {}

  const listener = (event: Event) => {
    callback((event as CustomEvent<AuthSessionClearedDetail>).detail)
  }
  window.addEventListener(AUTH_SESSION_CLEARED_EVENT, listener)
  return () => window.removeEventListener(AUTH_SESSION_CLEARED_EVENT, listener)
}
