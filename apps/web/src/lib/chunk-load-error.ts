// Vite's preload helper reports a lazy chunk's stylesheet that failed to load
// with this message (vite:preloadError carries the same error).
const CSS_PRELOAD_FAILED = 'Unable to preload CSS for '

// A dynamically imported chunk can fail to load when the network drops, the
// user aborts a reload, or a new deployment has replaced the hashed file the
// running page still points at. Browsers report a failed script with the first
// three messages (Chromium, WebKit, Firefox); a chunk's stylesheet is fetched
// first and fails with Vite's message. A page reload fetches the current
// index.html and its chunks.
export function isChunkLoadError(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error)
  return message.includes('Failed to fetch dynamically imported module')
    || message.includes('Importing a module script failed')
    || message.includes('error loading dynamically imported module')
    || message.includes(CSS_PRELOAD_FAILED)
}

function failedStylesheetOf(error: unknown): string | undefined {
  const message = error instanceof Error ? error.message : String(error)
  const at = message.indexOf(CSS_PRELOAD_FAILED)
  return at === -1 ? undefined : message.slice(at + CSS_PRELOAD_FAILED.length).trim()
}

// Vite's preload helper tries each stylesheet once per page: after one failed,
// later imports of the same chunk skip it and run the script without its
// styles. A transient failure (a background prefetch on a flaky network, say)
// would leave that screen unstyled until a reload. So failed stylesheets are
// collected here, and loaders wrapped with withStylesheetRecovery load them
// again themselves.
const failedStylesheets = new Set<string>()
const pendingStylesheets = new Map<string, Promise<void>>()

if (typeof window !== 'undefined') {
  window.addEventListener('vite:preloadError', (event) => {
    const href = failedStylesheetOf((event as Event & { payload?: unknown }).payload)
    if (href) failedStylesheets.add(href)
  })
}

function loadStylesheet(href: string): Promise<void> {
  let pending = pendingStylesheets.get(href)
  if (!pending) {
    pending = new Promise<void>((resolve, reject) => {
      // Drop the link the failed attempt left behind.
      for (const link of document.querySelectorAll<HTMLLinkElement>('link[rel="stylesheet"]')) {
        if (link.href === href) link.remove()
      }
      const link = document.createElement('link')
      link.rel = 'stylesheet'
      link.crossOrigin = ''
      link.href = href
      link.addEventListener('load', () => resolve())
      link.addEventListener('error', () => {
        link.remove()
        reject(new Error(CSS_PRELOAD_FAILED + href))
      })
      document.head.appendChild(link)
    })
      .then(() => { failedStylesheets.delete(href) })
      .finally(() => pendingStylesheets.delete(href))
    pendingStylesheets.set(href, pending)
  }
  return pending
}

/**
 * Wraps a lazy chunk loader so a stylesheet that failed to load earlier is
 * loaded again once the chunk's script has. A stylesheet that failed for this
 * loader must load or the load fails (with a chunk load error, so the usual
 * recovery applies); stylesheets that failed for other loaders, which Vite
 * also skips when chunks share them, are retried on the side.
 */
export function withStylesheetRecovery<T>(load: () => Promise<T>): () => Promise<T> {
  const ownStylesheets = new Set<string>()
  return async () => {
    let module: T
    try {
      module = await load()
    } catch (error) {
      const href = failedStylesheetOf(error)
      if (href) ownStylesheets.add(href)
      throw error
    }
    const results = await Promise.allSettled([...failedStylesheets].map(href => loadStylesheet(href).then(() => href)))
    for (const result of results) {
      if (result.status === 'fulfilled') ownStylesheets.delete(result.value)
    }
    for (const href of ownStylesheets) {
      if (failedStylesheets.has(href)) throw new Error(CSS_PRELOAD_FAILED + href)
    }
    return module
  }
}

// Reloading recovers from a stale or dropped chunk, but a chunk that keeps
// failing (blocked by a proxy or a browser extension, a server that keeps
// erroring) would reload the page in a loop: measured locally at dozens of
// page loads in 10s. The time of the last automatic reload is kept in
// sessionStorage, which survives the reload and is scoped to this tab. A
// second failure within the cooldown is left to the user (see showBootFailure
// in lib/boot-splash.ts) instead of reloading again.
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
