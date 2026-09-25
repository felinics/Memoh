import { animate } from 'motion/mini'

// Keep the turn entrance and composer placement on the same curve:
// exponential ease-out — very fast start, extra-soft landing, no overshoot.
export const CHAT_SEND_MOTION = {
  ease: [0.16, 1, 0.3, 1],
  duration: 0.4,
} as const

// The farthest a turn travels on entrance or exit; the viewport scroll covers
// any longer distance.
export const TURN_MOTION_MAX_DISTANCE_PX = 80

export function animateTurnEntrance(
  turn: HTMLElement,
  fromY: number,
  onFinish: () => void,
): () => void {
  const content = turn.querySelector<HTMLElement>('[data-turn-motion]')
  if (!content || fromY <= 0 || window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) {
    onFinish()
    return () => {}
  }
  const previousOverflow = turn.style.overflow
  // Transformed children must not enlarge scrollHeight while the viewport scrolls.
  turn.style.overflow = 'clip'
  content.style.transform = `translateY(${fromY}px)`
  let finished = false
  const cleanup = () => {
    if (finished) return
    finished = true
    content.style.removeProperty('transform')
    turn.style.overflow = previousOverflow
    onFinish()
  }
  // Animate the DOM transform directly so Motion can use the compositor's
  // native animation instead of writing inline styles on every JS frame.
  const playback = animate(content, {
    transform: [`translateY(${fromY}px)`, 'translateY(0px)'],
  }, {
    ...CHAT_SEND_MOTION,
    onComplete: cleanup,
  })
  return () => {
    playback.stop()
    cleanup()
  }
}

// The reverse of animateTurnEntrance for a turn that is being withdrawn (a
// first send that failed before its reply started). It travels the same
// curve back down toward the composer and fades, since, unlike an entrance,
// nothing takes its place when it is removed. onFinish always runs once, so
// the caller can drop the element even when motion is reduced or stopped.
export function animateTurnExit(
  turn: HTMLElement,
  toY: number,
  onFinish: () => void,
): () => void {
  const content = turn.querySelector<HTMLElement>('[data-turn-motion]')
  if (!content || window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) {
    onFinish()
    return () => {}
  }
  const previousOverflow = turn.style.overflow
  turn.style.overflow = 'clip'
  let finished = false
  const cleanup = () => {
    if (finished) return
    finished = true
    turn.style.overflow = previousOverflow
    onFinish()
  }
  const playback = animate(content, {
    transform: ['translateY(0px)', `translateY(${Math.max(0, toY)}px)`],
    opacity: [1, 0],
  }, {
    ...CHAT_SEND_MOTION,
    onComplete: cleanup,
  })
  return () => {
    playback.stop()
    cleanup()
  }
}
