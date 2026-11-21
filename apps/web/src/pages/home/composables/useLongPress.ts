import { onBeforeUnmount } from 'vue'

// Hold time before a touch press counts as a long press — the platform norm
// (Android's long-press timeout, iOS context-menu hold) sits around half a second.
const HOLD_MS = 500
// A finger drifts while held; past this distance the gesture is a scroll, not a press.
const MOVE_TOLERANCE_PX = 10

/**
 * Touch long press as element event handlers, bound with v-on. Mouse pointers
 * are ignored — they have hover and a right-click menu. The press is cancelled
 * when the finger lifts, moves into a scroll, or the browser takes the pointer
 * over (pointercancel). The browser's own long-press menu (Android fires
 * contextmenu on a hold) is suppressed so only ours opens.
 */
export function useLongPress(onLongPress: () => void) {
  let timer: ReturnType<typeof setTimeout> | null = null
  let startX = 0
  let startY = 0

  function cancel() {
    if (timer === null) return
    clearTimeout(timer)
    timer = null
  }

  onBeforeUnmount(cancel)

  return {
    pointerdown(event: PointerEvent) {
      if (event.pointerType === 'mouse' || !event.isPrimary) return
      cancel()
      startX = event.clientX
      startY = event.clientY
      timer = setTimeout(() => {
        timer = null
        onLongPress()
      }, HOLD_MS)
    },
    pointermove(event: PointerEvent) {
      if (timer === null) return
      if (Math.hypot(event.clientX - startX, event.clientY - startY) > MOVE_TOLERANCE_PX) cancel()
    },
    pointerup: cancel,
    pointercancel: cancel,
    contextmenu(event: Event) {
      event.preventDefault()
    },
  }
}
