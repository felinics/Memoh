/** Browser-owned smooth scrolling; JS only observes completion and cancellation. */
export function nativeScrollTo(
  root: HTMLElement,
  top: number,
  onFinish: () => void,
): () => void {
  let finished = false
  let idleTimer: ReturnType<typeof setTimeout> | undefined
  const finish = () => {
    if (finished) return
    finished = true
    clearTimeout(idleTimer)
    root.removeEventListener('scrollend', finish)
    root.removeEventListener('scroll', onScroll)
    onFinish()
  }
  // Older browsers have no scrollend. Observe idle time without driving frames.
  const onScroll = () => {
    clearTimeout(idleTimer)
    idleTimer = setTimeout(finish, 150)
  }
  const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
  const target = Math.min(Math.max(top, 0), Math.max(0, root.scrollHeight - root.clientHeight))
  const stationary = Math.abs(root.scrollTop - target) < 1
  root.addEventListener('scrollend', finish)
  if (!('onscrollend' in root)) {
    root.addEventListener('scroll', onScroll, { passive: true })
    onScroll()
  }
  root.scrollTo({ top: target, behavior: reduced ? 'instant' : 'smooth' })
  // No movement means no scrollend event, including reduced-motion jumps.
  if (stationary || reduced) queueMicrotask(finish)
  return () => {
    if (finished) return
    // Stop at the current position; do not let a cancelled send keep scrolling.
    root.scrollTo({ top: root.scrollTop, behavior: 'instant' })
    finish()
  }
}
