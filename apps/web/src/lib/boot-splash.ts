// The loading splash itself (markup, timeline, failure state) lives inline in
// index.html because it must run before this bundle. This module takes it down
// once the first screen has rendered, and brings it back in its failure state
// when the app cannot load what it needs to show anything.

// Matches the mark's fade-in delay in index.html: before it, the splash is
// still only the page background, so there is nothing to animate away.
const SPLASH_VISIBLE_AFTER_MS = 200
// The mark finishes fading in at 380ms (200ms delay + 180ms); a failure shown
// before then holds its message until the mark is in place.
const MARK_SETTLED_MS = 380
// The motions are the ones login -> home uses (the login page's exit in
// pages/login/index.vue, then main-section's entry): content scales to 0.88 and
// fades over 175ms, the screen behind scales in from 1.15. Login plays them one
// after the other because the next page mounts only after the exit. The screen
// behind the splash has already rendered, so here they run together: in
// sequence it stayed hidden about 200ms longer after it was ready (the exit,
// then blank frames before the entry began), which read as lag.
// The entry is also shorter than login's 450ms (300ms): it follows a wait the
// user did not start, and at 450ms the end of a load still felt slow.
//
// They run as a view transition, whose animations (the boot-entering rules in
// index.html) play on snapshots of the page. The app must not be scaled
// itself: dockview sizes its panels from their on-screen boxes, so a panel
// measured while #app was scaled stayed 15% too large until the next resize.
//
// Without view transitions only the splash fades out, over SPLASH_EXIT_MS
// (the is-leaving rules in index.html); the app's entry is skipped.
const SPLASH_EXIT_MS = 175

// Kept after dismissal so showBootFailure can bring the same, already
// localized markup back instead of drawing a second failure screen.
let dismissedSplash: HTMLElement | undefined

export function dismissBootSplash(): void {
  const splash = document.getElementById('boot-splash')
  if (!splash) return
  // A failed splash stays: it is the only error surface when the app can't start.
  if (splash.classList.contains('is-failed')) return
  dismissedSplash = splash
  const reducedMotion = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
  // performance.now() counts from navigation start, which is close enough to
  // when the splash's CSS delays started.
  if (reducedMotion || performance.now() < SPLASH_VISIBLE_AFTER_MS) {
    splash.remove()
    return
  }
  if (!('startViewTransition' in document)) {
    splash.classList.add('is-leaving')
    window.setTimeout(() => {
      // showBootFailure may have taken the splash back over meanwhile.
      if (!splash.classList.contains('is-leaving')) return
      splash.remove()
      splash.classList.remove('is-leaving')
    }, SPLASH_EXIT_MS)
    return
  }
  const root = document.documentElement
  root.classList.add('boot-entering')
  const leave = () => {
    root.classList.remove('boot-entering')
    // showBootFailure may have taken the splash over meanwhile.
    if (!splash.classList.contains('is-failed')) splash.remove()
  }
  try {
    const transition = document.startViewTransition(() => {
      if (!splash.classList.contains('is-failed')) splash.remove()
    })
    // A skipped transition (the tab is hidden, say) still removes the splash;
    // only its animation is dropped, which rejects `ready`.
    transition.ready.catch(() => {})
    void transition.finished.then(leave, leave)
  } catch {
    // The splash must never be left covering an app that is ready.
    leave()
  }
}

/**
 * Switches the splash to its failure state (message, Retry, Details), for a
 * chunk the app could not load even after reloading once (see
 * reloadForChunkLoadError): the starting language, the chat workspace, or the
 * first route's page. A splash already dismissed is put back first. `detail`
 * is shown under Details. Does nothing on a host whose page has no splash
 * (the desktop shell).
 */
export function showBootFailure(detail: string): void {
  const splash = document.getElementById('boot-splash') ?? dismissedSplash
  if (!splash || splash.classList.contains('is-failed')) return
  splash.classList.remove('is-leaving')
  const detailEl = splash.querySelector('#boot-splash-detail')
  if (detailEl) detailEl.textContent = detail
  let delay = Math.max(90, MARK_SETTLED_MS - performance.now())
  if (!splash.isConnected) {
    // Re-inserting restarts the mark's fade-in, so the message waits for it;
    // the class goes on after a style flush so the message transitions in.
    document.body.append(splash)
    void splash.offsetWidth
    delay = MARK_SETTLED_MS
  }
  splash.style.setProperty('--boot-failed-delay', `${delay}ms`)
  splash.classList.add('is-failed')
}
