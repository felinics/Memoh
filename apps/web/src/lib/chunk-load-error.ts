import type { Locale } from '@/i18n/messages'
import { detectLocale } from '@/utils/detect-locale'

// A dynamically imported chunk can fail to load when the network drops, the
// user aborts a reload, or a new deployment has replaced the hashed file the
// running page still points at. Browsers report it with these messages
// (Chromium, WebKit, Firefox); a page reload fetches the current index.html
// and its chunks.
export function isChunkLoadError(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error)
  return message.includes('Failed to fetch dynamically imported module')
    || message.includes('Importing a module script failed')
    || message.includes('error loading dynamically imported module')
}

// Reloading recovers from a stale or dropped chunk, but a chunk that keeps
// failing (blocked by a proxy or a browser extension, a server that keeps
// erroring) would reload the page in a loop: measured locally at dozens of
// page loads in 10s. The time of the last automatic reload is kept in
// sessionStorage, which survives the reload and is scoped to this tab. A
// second failure within the cooldown is left to the user (see
// showLoadFailedScreen) instead of reloading again.
const LAST_RELOAD_KEY = 'memoh:chunk-reload-at'
const RELOAD_COOLDOWN_MS = 60_000

/**
 * Reloads the page to recover from a chunk load failure, unless this tab
 * already did so in the last minute. Returns whether the page is reloading.
 */
export function reloadForChunkLoadError(): boolean {
  try {
    const last = Number(sessionStorage.getItem(LAST_RELOAD_KEY))
    if (last && Math.abs(Date.now() - last) < RELOAD_COOLDOWN_MS) return false
    sessionStorage.setItem(LAST_RELOAD_KEY, String(Date.now()))
  } catch {
    // Without storage the reloads can't be counted, so don't start any.
    return false
  }
  window.location.reload()
  return true
}

// The copy is built in rather than read from the locale files: this screen is
// shown when the starting language itself failed to load.
const LOAD_FAILED_COPY: Record<Locale, { message: string, action: string }> = {
  en: { message: 'The page couldn\'t load. Check your connection, then reload.', action: 'Reload' },
  zh: { message: '页面加载失败，请检查网络后刷新。', action: '刷新页面' },
  ja: { message: 'ページを読み込めませんでした。接続を確認してから再読み込みしてください。', action: '再読み込み' },
}

function pageLocale(): Locale {
  try {
    const stored = localStorage.getItem('language')
    if (stored === 'en' || stored === 'zh' || stored === 'ja') return stored
  } catch {
    // Storage blocked: fall back to the browser language.
  }
  return detectLocale()
}

/**
 * Covers the page with a message and a reload button, for a chunk failure that
 * leaves nothing to show: the starting language, the chat workspace, or the
 * first route's page. Built with plain DOM, without Vue or the locale files,
 * since either may be what failed. Styled by the .load-failed rules in
 * index.html, next to the loading splash it replaces.
 */
export function showLoadFailedScreen(): void {
  if (typeof document === 'undefined' || document.getElementById('load-failed')) return
  const copy = LOAD_FAILED_COPY[pageLocale()]
  const screen = document.createElement('div')
  screen.id = 'load-failed'
  screen.className = 'boot-splash load-failed'
  screen.setAttribute('role', 'alert')
  const message = document.createElement('p')
  message.textContent = copy.message
  const button = document.createElement('button')
  button.type = 'button'
  button.textContent = copy.action
  button.addEventListener('click', () => window.location.reload())
  screen.append(message, button)
  document.getElementById('boot-splash')?.remove()
  document.body.append(screen)
}
