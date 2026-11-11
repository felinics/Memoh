/**
 * One queue of code chunks to download in the background, ahead of the
 * navigation or panel that needs them. Skipped under Save-Data; failures are
 * swallowed, and the code then loads normally when it is actually needed.
 *
 * The queue waits for the host's go-ahead (holdPrefetchUntil). The first
 * screen of the chat workspace starts a burst of its own requests (web fonts
 * and a dozen API calls); background chunks started during that burst compete
 * with it, and with whatever the user clicks first. Over HTTP/1.1 the browser
 * opens at most six connections per server, so a click's requests then queue
 * behind the background downloads (measured on Slow 4G over HTTP/1.1: opening
 * Settings right after the chat appeared took about 9s while the editor
 * downloaded in the background).
 *
 * Chunks are warmed ONE AT A TIME. Firing every loader at once makes a click
 * that lands mid-warmup queue behind the whole swarm — the module map dedupes
 * by URL, so the click cannot be prioritized and can end up slower than the
 * old single lazy import. Sequentially, an in-flight chunk costs a click at
 * most one chunk, and chunks not yet started leave the connection free for
 * the click itself. For the same reason large chunks (the panels: the editor
 * alone is about 700KB gzip) go through prefetchLater, after every chunk
 * queued with prefetchInIdle, including ones queued after them.
 */
type ChunkLoader = () => Promise<unknown>

const queue: ChunkLoader[] = []
const laterQueue: ChunkLoader[] = []
let warming = false
let ready: Promise<void> = Promise.resolve()

/**
 * Holds the queue until `until` settles (either way). Call it before anything
 * is queued: the queue reads the hold when it starts. A host that never calls
 * it warms on the first idle period.
 */
export function holdPrefetchUntil(until: Promise<unknown>): void {
  ready = until.then(() => {}, () => {})
}

/** Queue chunks after those already queued. */
export function prefetchInIdle(loaders: ChunkLoader[]): void {
  if (saveDataRequested()) return
  queue.push(...loaders)
  start()
}

/** Queue large chunks; they download only once everything queued with prefetchInIdle has. */
export function prefetchLater(loaders: ChunkLoader[]): void {
  if (saveDataRequested()) return
  laterQueue.push(...loaders)
  start()
}

/**
 * Settles once no resource (script, stylesheet, font, fetch) has finished
 * loading for `quietMs`, or after `maxMs` at most. Only finished requests are
 * visible to the page, so a single long download still in flight reads as
 * quiet; the bursts this waits out are many requests finishing in turn.
 */
export function whenNetworkQuiet(quietMs = 1000, maxMs = 10_000): Promise<void> {
  return new Promise((resolve) => {
    let observer: PerformanceObserver | undefined
    let quietTimer: ReturnType<typeof setTimeout> | undefined
    const done = () => {
      observer?.disconnect()
      clearTimeout(quietTimer)
      clearTimeout(maxTimer)
      resolve()
    }
    const restartQuietTimer = () => {
      clearTimeout(quietTimer)
      quietTimer = setTimeout(done, quietMs)
    }
    const maxTimer = setTimeout(done, maxMs)
    try {
      observer = new PerformanceObserver(restartQuietTimer)
      observer.observe({ type: 'resource' })
    } catch {
      // No resource timing: fall back to the plain delay.
    }
    restartQuietTimer()
  })
}

function saveDataRequested(): boolean {
  const connection = (navigator as Navigator & { connection?: { saveData?: boolean } }).connection
  return !!connection?.saveData
}

function scheduleIdle(cb: () => void) {
  const w = window as Window & {
    requestIdleCallback?: (cb: () => void, opts?: { timeout: number }) => number
  }
  if (w.requestIdleCallback) w.requestIdleCallback(cb, { timeout: 3000 })
  else setTimeout(cb, 2000)
}

function start() {
  if (warming) return
  warming = true
  void ready.then(() => scheduleIdle(warmNext))
}

function warmNext() {
  const load = queue.shift() ?? laterQueue.shift()
  if (!load) {
    warming = false
    return
  }
  void load().catch(() => {}).finally(() => scheduleIdle(warmNext))
}
