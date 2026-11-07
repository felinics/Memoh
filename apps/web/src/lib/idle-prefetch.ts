/**
 * One queue of code chunks to download during browser idle time, ahead of the
 * navigation or panel that needs them. Skipped under Save-Data; failures are
 * swallowed, and the code then loads normally when it is actually needed.
 *
 * Chunks are warmed ONE AT A TIME. Firing every loader at once makes a click
 * that lands mid-warmup queue behind the whole swarm — the module map dedupes
 * by URL, so the click cannot be prioritized and can end up slower than the
 * old single lazy import. Sequentially, an in-flight chunk costs a click at
 * most one chunk, and chunks not yet started leave the connection free for
 * the click itself.
 */
type ChunkLoader = () => Promise<unknown>

const queue: ChunkLoader[] = []
let warming = false

/** Queue chunks after those already queued. */
export function prefetchInIdle(loaders: ChunkLoader[]): void {
  if (saveDataRequested()) return
  queue.push(...loaders)
  start()
}

/** Queue a chunk ahead of those already queued, for code the current screen is likely to need next. */
export function prefetchFirst(load: ChunkLoader): void {
  if (saveDataRequested()) return
  queue.unshift(load)
  start()
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
  scheduleIdle(warmNext)
}

function warmNext() {
  const load = queue.shift()
  if (!load) {
    warming = false
    return
  }
  void load().catch(() => {}).finally(() => scheduleIdle(warmNext))
}
