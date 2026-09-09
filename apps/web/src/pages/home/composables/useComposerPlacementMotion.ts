import { nextTick, onScopeDispose, watch, type Ref } from 'vue'
import { animate } from 'motion/mini'
import { CHAT_SEND_MOTION } from './turn-entrance'

export function useComposerPlacementMotion(
  element: Ref<HTMLElement | null>,
  isWelcome: Ref<boolean>,
) {
  let cancel: (() => void) | undefined
  let revision = 0
  const reset = () => {
    revision++
    cancel?.()
    cancel = undefined
  }
  watch(isWelcome, async (welcome, wasWelcome) => {
    reset()
    const el = element.value
    if (welcome || !wasWelcome || !el
      || window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return
    const attempt = revision
    // Measure before Vue changes the welcome layout, then invert the movement.
    // Only the visual offset animates; dock/composer measurements see final layout.
    const before = el.getBoundingClientRect()
    await nextTick()
    if (revision !== attempt || element.value !== el) return
    const after = el.getBoundingClientRect()
    const x = before.left - after.left
    const y = before.top - after.top
    if (!x && !y) return
    const previousTransform = el.style.transform
    const restore = () => { el.style.transform = previousTransform }
    const from = `translate(${x}px, ${y}px)`
    el.style.transform = from
    // A native transform keeps the movement independent of message rendering.
    const playback = animate(el, {
      transform: [from, previousTransform || 'translate(0px, 0px)'],
    }, {
      ...CHAT_SEND_MOTION,
      onComplete: restore,
    })
    cancel = () => { playback.stop(); restore() }
  }, { flush: 'pre' })
  onScopeDispose(reset)
}
