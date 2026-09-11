/** Browser-owned scrolling with a live destination until natural completion. */
export function nativeScrollTo(
  root: HTMLElement,
  getTarget: () => number,
  onFinish: () => void,
): () => void {
  let finished = false
  let idleTimer: ReturnType<typeof setTimeout> | undefined
  let backstop: ReturnType<typeof setTimeout> | undefined
  const finish = () => {
    if (finished) return
    finished = true
    clearTimeout(idleTimer)
    clearTimeout(backstop)
    root.removeEventListener('scrollend', settle)
    root.removeEventListener('scroll', onScroll)
    onFinish()
  }
  const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
  const resolveTarget = () => Math.min(Math.max(getTarget(), 0), Math.max(0, root.scrollHeight - root.clientHeight))
  let target = resolveTarget()
  const settle = () => {
    if (finished) return
    const nextTarget = resolveTarget()
    // Layout may have moved the message while the browser owned the flight.
    // Continue smoothly only when the destination changed and native anchoring
    // has not already landed there. Keep cancellation armed across both legs.
    if (!reduced && Math.abs(nextTarget - target) >= 1 && Math.abs(root.scrollTop - nextTarget) >= 1) {
      target = nextTarget
      root.scrollTo({ top: target, behavior: 'smooth' })
      if (!Reflect.has(root, 'onscrollend')) onScroll()
      else armBackstop()
      return
    }
    finish()
  }
  // Older browsers have no scrollend. Observe idle time without driving frames.
  const onScroll = () => {
    clearTimeout(idleTimer)
    idleTimer = setTimeout(settle, 150)
  }
  // scrollend is expected on every terminal path, but nothing guarantees it:
  // if the engine swallows the event, finish would never fire and the caller's
  // "smooth scroll in flight" latch would stick. Well past any browser smooth-
  // scroll duration, so a false finish means the flight was already lost —
  // releasing is the right recovery either way.
  let armedTarget = 0
  let armedScrollTop = 0
  const onBackstop = () => {
    // Revalidate through settle() rather than releasing blindly: the live
    // destination may have moved during the flight, and these callers disable
    // bottom-following, so nothing downstream corrects the landing. If neither
    // the destination nor the position moved since the last arm, no flight is
    // making progress — finish instead of re-arming forever.
    if (root.scrollTop === armedScrollTop && resolveTarget() === armedTarget) {
      finish()
      return
    }
    settle()
  }
  const armBackstop = () => {
    clearTimeout(backstop)
    armedTarget = target
    armedScrollTop = root.scrollTop
    backstop = setTimeout(onBackstop, 2000)
  }
  const stationary = Math.abs(root.scrollTop - target) < 1
  root.addEventListener('scrollend', settle)
  if (!Reflect.has(root, 'onscrollend')) {
    root.addEventListener('scroll', onScroll, { passive: true })
    onScroll()
  } else {
    armBackstop()
  }
  root.scrollTo({ top: target, behavior: reduced ? 'instant' : 'smooth' })
  // No movement means no scrollend event, including reduced-motion jumps.
  if (stationary || reduced) queueMicrotask(settle)
  return () => {
    if (finished) return
    // Stop at the current position; do not let a cancelled send keep scrolling.
    root.scrollTo({ top: root.scrollTop, behavior: 'instant' })
    finish()
  }
}
