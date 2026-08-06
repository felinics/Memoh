import { onBeforeUnmount, onMounted, readonly, ref } from 'vue'

/**
 * Tracks the on-screen virtual keyboard via the Visual Viewport API and
 * exposes its height in CSS pixels. 0 whenever no keyboard is up.
 *
 * Why this exists: the app shell is an `h-dvh overflow-hidden` chain with the
 * composer absolutely anchored to the layout viewport's bottom. iOS Safari
 * never resizes the layout viewport (or `dvh`) for the software keyboard, so
 * without this the keyboard simply slides up OVER the composer. Consumers add
 * the reported height as a bottom offset on the composer dock (and to the
 * message list's bottom padding) to lift both above the keyboard.
 *
 * The formula `innerHeight - visualViewport.height - visualViewport.offsetTop`
 * is robust against the mobile URL bar: when the bar collapses, BOTH
 * innerHeight and visualViewport.height grow together and the difference
 * stays 0 — only the keyboard eats into the visual viewport alone.
 *
 * Pinch-zoom guard: while the page is zoomed (scale !== 1) the same formula
 * would report a phantom "keyboard", so we force 0. A zoomed user has
 * explicitly taken over the viewport; leaving the composer put is the least
 * surprising behavior.
 *
 * Side channel: every change re-dispatches a synthetic window `resize`.
 * floating-ui's autoUpdate (the engine under our popovers/menus) listens to
 * window resize but NOT to visualViewport, so without this an open popover
 * would stay anchored to the composer's pre-lift position.
 *
 * Android note: Chrome honors `interactive-widget=resizes-content` (set in
 * index.html), which shrinks the layout viewport itself — innerHeight then
 * tracks visualViewport.height and this composable naturally reports 0 there.
 * The lift comes from the resized h-dvh chain instead. Both paths coexist.
 */
export function useVirtualKeyboard() {
  const keyboardHeight = ref(0)

  function measure(): void {
    const vv = window.visualViewport
    const next = !vv || vv.scale !== 1
      ? 0
      : Math.max(0, Math.round(window.innerHeight - vv.height - vv.offsetTop))
    if (next === keyboardHeight.value) return
    keyboardHeight.value = next
    // See header: re-anchor open floating-ui popovers to the lifted composer.
    // Only on an actual change — pinch-zoom at scale !== 1 reports 0 and must
    // not turn into a synthetic-resize storm.
    window.dispatchEvent(new Event('resize'))
  }

  onMounted(() => {
    // No visualViewport (older WebViews, some test runtimes): keyboard lift is
    // simply inert — the composer behaves exactly as before this composable.
    if (typeof window === 'undefined' || !window.visualViewport) return
    window.visualViewport.addEventListener('resize', measure)
    window.visualViewport.addEventListener('scroll', measure)
  })

  onBeforeUnmount(() => {
    window.visualViewport?.removeEventListener('resize', measure)
    window.visualViewport?.removeEventListener('scroll', measure)
  })

  return readonly(keyboardHeight)
}
