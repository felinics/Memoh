// The loading splash itself (markup, timeline, failure state) lives inline in
// index.html because it must run before this bundle. This module only owns
// taking it down once the first screen has rendered.

// Matches the mark's fade-in delay in index.html: before it, the splash is
// still only the page background, so there is nothing to animate away.
const SPLASH_VISIBLE_AFTER_MS = 200
// The exit pair is the same one login -> home uses (the login page's exit in
// pages/login/index.vue, then main-section's entry): content scales to 0.88 and
// fades over 175ms, then the screen behind scales in from 1.15 over 450ms.
const SPLASH_EXIT_MS = 175
const APP_ENTER_MS = 450

export function dismissBootSplash(): void {
  const splash = document.getElementById('boot-splash')
  if (!splash) return
  // A failed splash stays: it is the only error surface when the app can't start.
  if (splash.classList.contains('is-failed')) return
  const reducedMotion = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
  // performance.now() counts from navigation start, which is close enough to
  // when the splash's CSS delays started.
  if (reducedMotion || performance.now() < SPLASH_VISIBLE_AFTER_MS) {
    splash.remove()
    return
  }
  splash.classList.add('is-leaving')
  window.setTimeout(() => {
    splash.remove()
    const root = document.documentElement
    root.classList.add('boot-entering')
    window.setTimeout(() => root.classList.remove('boot-entering'), APP_ENTER_MS)
  }, SPLASH_EXIT_MS)
}
