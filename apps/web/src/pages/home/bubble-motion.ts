import { ref } from 'vue'
import { BUBBLE_SPRING, CompositorSpring } from '@/lib/compositor-spring'

// Whether a bubble the user just sent springs in. Off in production; only the
// dev-only mobile direction comparison (pages/dev/mobile-directions) turns it
// on, so the motion can be tried on a phone before it is adopted.
export const bubbleSpringEnabled = ref(false)

const scaleFormat = (s: number) => `scale3d(${s.toFixed(4)}, ${s.toFixed(4)}, 1)`

// A newly sent bubble starts slightly small and springs to full size, scaling
// from its centre. Values are the mobile interaction prototype's, unchanged:
// start at 0.85 with an initial velocity of 3 scale units per second.
export function playBubbleAppear(el: HTMLElement) {
  if (!bubbleSpringEnabled.value) return
  if (matchMedia('(prefers-reduced-motion: reduce)').matches) return
  el.style.transformOrigin = 'center'
  const spring = new CompositorSpring(0.85, () => [{ el, prop: 'transform', format: scaleFormat }])
  // Clear the resting inline styles once settled, so the bubble is left as the
  // template rendered it.
  const onRest = () => {
    el.style.transform = ''
    el.style.transformOrigin = ''
  }
  spring.springTo(1, 3, { ...BUBBLE_SPRING, onRest })
}
