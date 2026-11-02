// Service worker for the installed web app (registered by src/main.ts in
// production builds only).
//
// It keeps the app shell openable offline and nothing more:
//   - page navigations go to the network first, and the latest index.html is
//     kept as the shell to fall back on when the network is unreachable;
//   - the few unhashed files at the site root that the shell itself shows
//     (the logo, install icons, manifest) are precached and refreshed from the
//     network whenever it is reachable;
//   - built assets under /assets/ carry a content hash in their name, so they
//     are served from the cache once fetched and never revalidated;
//   - everything else, above all /api/ requests, streams and channel traffic,
//     is not intercepted: data is never answered from a cache.
//
// Bump VERSION to drop every cache from an earlier worker on activation.
const VERSION = 'v2'
const SHELL_CACHE = `memoh-shell-${VERSION}`
const ASSET_CACHE = `memoh-assets-${VERSION}`
// Hashed assets accumulate across deploys; keep the newest entries only.
const ASSET_CACHE_LIMIT = 400

const scopeUrl = new URL(self.registration.scope)
const SHELL_URL = new URL('./', scopeUrl).href
const ASSET_PREFIX = new URL('./assets/', scopeUrl).pathname
const API_PREFIX = new URL('./api/', scopeUrl).pathname
const STATIC_URLS = ['logo.svg', 'manifest.webmanifest', 'apple-touch-icon.png', 'pwa-192.png', 'pwa-512.png']
  .map(path => new URL(path, scopeUrl).href)

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(SHELL_CACHE)
      .then(cache => cache.addAll([SHELL_URL, ...STATIC_URLS].map(url => new Request(url, { cache: 'reload' }))))
      .then(() => self.skipWaiting()),
  )
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys()
      .then(keys => Promise.all(keys
        .filter(key => key.startsWith('memoh-') && key !== SHELL_CACHE && key !== ASSET_CACHE)
        .map(key => caches.delete(key))))
      .then(() => self.clients.claim()),
  )
})

async function trimAssets() {
  const cache = await caches.open(ASSET_CACHE)
  const keys = await cache.keys()
  // Cache.keys() returns entries in insertion order: the oldest go first.
  await Promise.all(keys.slice(0, Math.max(0, keys.length - ASSET_CACHE_LIMIT)).map(key => cache.delete(key)))
}

async function handleNavigation(request) {
  try {
    const response = await fetch(request)
    if (response.ok && response.headers.get('content-type')?.includes('text/html')) {
      const copy = response.clone()
      void caches.open(SHELL_CACHE).then(cache => cache.put(SHELL_URL, copy))
    }
    return response
  }
  catch (error) {
    const shell = await caches.match(SHELL_URL, { cacheName: SHELL_CACHE })
    if (shell) return shell
    throw error
  }
}

async function handleStatic(request) {
  try {
    const response = await fetch(request)
    if (response.ok) {
      const copy = response.clone()
      void caches.open(SHELL_CACHE).then(cache => cache.put(request.url, copy))
    }
    return response
  }
  catch (error) {
    const cached = await caches.match(request.url, { cacheName: SHELL_CACHE })
    if (cached) return cached
    throw error
  }
}

async function handleAsset(request) {
  const cached = await caches.match(request, { cacheName: ASSET_CACHE })
  if (cached) return cached
  const response = await fetch(request)
  if (response.ok) {
    const copy = response.clone()
    void caches.open(ASSET_CACHE)
      .then(cache => cache.put(request, copy))
      .then(trimAssets)
  }
  return response
}

self.addEventListener('fetch', (event) => {
  const { request } = event
  if (request.method !== 'GET') return
  const url = new URL(request.url)
  if (url.origin !== scopeUrl.origin) return
  // Server-rendered pages under /api/ (OAuth callbacks) are navigations too;
  // they must reach the server and must not replace the cached shell.
  if (url.pathname.startsWith(API_PREFIX)) return
  if (request.mode === 'navigate') {
    event.respondWith(handleNavigation(request))
    return
  }
  if (STATIC_URLS.includes(url.origin + url.pathname)) {
    event.respondWith(handleStatic(request))
    return
  }
  if (url.pathname.startsWith(ASSET_PREFIX)) {
    event.respondWith(handleAsset(request))
  }
})
