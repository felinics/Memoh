/**
 * Arrival motion for unread attention: the "slide in + highlight" design picked in the
 * needs-you lab. Rows and the inbox card grow their height together with a short drop
 * and fade, so the list below is pushed instead of jumping; a fresh row flashes its
 * kind tint once and its dot pops. Everything runs through the Web Animations API so
 * Vue transition hooks can finish exactly when the motion does, and it all collapses to
 * an instant change under `prefers-reduced-motion` or where the API is missing.
 */

const EASE = 'cubic-bezier(0.22, 1, 0.36, 1)'
const ENTER_MS = 340
const LEAVE_MS = 240
const STAGGER_MS = 80
const DROP = '-0.625rem'

let enterBatch = 0
let enterBatchReset = false

function canAnimate(el: Element): el is HTMLElement {
  if (typeof (el as HTMLElement).animate !== 'function') return false
  return !window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
}

/** Rows entering in the same tick cascade, so a burst reads as several arrivals. */
function nextStagger() {
  const delay = enterBatch * STAGGER_MS
  enterBatch += 1
  if (!enterBatchReset) {
    enterBatchReset = true
    queueMicrotask(() => {
      enterBatch = 0
      enterBatchReset = false
    })
  }
  return delay
}

/** Vue `@enter` hook for live arrivals: grow from zero height while dropping in, staggered. */
export function enterRow(el: Element, done: () => void) {
  if (!canAnimate(el)) return done()
  const height = `${el.offsetHeight}px`
  const animation = el.animate([
    { height: '0px', opacity: 0, translate: `0 ${DROP}` },
    { height, opacity: 1, translate: '0 0' },
  ], { duration: ENTER_MS, delay: nextStagger(), easing: EASE, fill: 'backwards' })
  void animation.finished.then(done, done)
}

/**
 * Rows the user reveals (expanding the list) open together at the collapse speed, as the
 * mirror of collapsing. The slower, staggered entrance is reserved for live arrivals.
 */
export function revealRow(el: Element, done: () => void) {
  if (!canAnimate(el)) return done()
  const height = `${el.offsetHeight}px`
  const animation = el.animate([
    { height: '0px', opacity: 0, translate: '0 -0.3125rem' },
    { height, opacity: 1, translate: '0 0' },
  ], { duration: LEAVE_MS, easing: EASE, fill: 'backwards' })
  void animation.finished.then(done, done)
}

/** Vue `@leave` hook: collapse in place so the rows below close the gap smoothly. */
export function leaveRow(el: Element, done: () => void) {
  if (!canAnimate(el)) return done()
  const height = `${el.offsetHeight}px`
  const animation = el.animate([
    { height, opacity: 1, translate: '0 0' },
    { height: '0px', opacity: 0, translate: '0 -0.3125rem' },
  ], { duration: LEAVE_MS, easing: EASE, fill: 'forwards' })
  void animation.finished.then(done, done)
}

/** One-shot tint on a row whose attention just arrived; the tint color comes from CSS. */
export function flashRow(el: Element) {
  if (!canAnimate(el)) return
  el.animate([{ opacity: 1 }, { opacity: 0 }], { duration: 1400, easing: 'ease-out' })
}

/** Spring pop plus one expanding halo, played only for a fresh arrival. */
export function popDot(el: Element) {
  if (!canAnimate(el)) return
  el.animate([{ scale: 0.2 }, { scale: 1.15, offset: 0.55 }, { scale: 1 }], { duration: 360, easing: 'ease-out' })
  el.animate([{ opacity: 0.5, scale: 1 }, { opacity: 0, scale: 3 }], {
    duration: 900,
    delay: 120,
    easing: EASE,
    pseudoElement: '::after',
    fill: 'backwards',
  })
}

/** Small bump on a count that changed. */
export function bounce(el: Element) {
  if (!canAnimate(el)) return
  el.animate([{ scale: 1 }, { scale: 1.3 }, { scale: 1 }], { duration: 320, easing: 'ease-out' })
}
