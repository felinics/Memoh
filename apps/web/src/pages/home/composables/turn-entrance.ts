import { spring } from 'motion'
import { animate } from 'motion/mini'

// Codex 26.901.51231, local-conversation-thread-40db5af470f5.js:
// GE applies y to the latest turn; UE animates it to zero with sD.
// Memoh retains its own geometry; turn and composer share Codex's
// original 500ms non-bouncing spring.
export const CHAT_SEND_MOTION = {
  type: spring,
  bounce: 0,
  duration: 0.5,
} as const

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
