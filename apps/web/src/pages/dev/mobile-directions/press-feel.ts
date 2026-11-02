// Platform press feedback for the mobile shell, applied by one delegated
// listener so no component carries platform branches.
//
// The rule both feels follow: feedback fills exactly the shape the user sees
// as the control. A ripple or highlight therefore lives in a layer that
// inherits the pressed element's border-radius and clips to it; it never paints
// a rectangle the eye cannot attribute to a visible shape.
//
//   ripple — Android / Compose. Timing matches Compose's RippleAnimation: the
//            wave fades in over 75ms to 10% of the content color while it grows
//            for 225ms (FastOutSlowIn) from 0.3 × the longer side to half the
//            diagonal + 10, its center drifting linearly from the touch point to
//            the element center; it fades out over 150ms once the grow has
//            finished AND the finger is up.
//   ios    — UIKit. A full-width row (table cell) shows a solid highlight fill
//            and fades it out on release; anything narrower (bar buttons, icon
//            buttons, chips) dims its content instead, the way UIButton does.
//
// The library's own press chrome (press-scale, pressed fills) is switched off
// for these feels in mobile-directions.css so the two never stack.
import { feel, type PressFeel } from './state'

const PRESSABLE = [
  'button',
  'a[href]',
  '[role="button"]',
  '[role="menuitem"]',
  '[role="menuitemradio"]',
  '[role="menuitemcheckbox"]',
  '[role="option"]',
  '[role="tab"]',
  '[role="radio"]',
  '[role="checkbox"]',
].join(', ')

// Controls that already animate their own state, and text entry.
const EXCLUDED = '[role="switch"], [data-slot="switch"], [data-press-none], input, textarea, select, [contenteditable="true"]'

const MOBILE_QUERY = '(width < 48rem)'

const RIPPLE_ALPHA = 0.1
const RIPPLE_FADE_IN_MS = 75
const RIPPLE_GROW_MS = 225
const RIPPLE_FADE_OUT_MS = 150
const RIPPLE_EXTRA_RADIUS = 10
const FAST_OUT_SLOW_IN = 'cubic-bezier(0.4, 0, 0.2, 1)'

// A UIKit cell highlights when a touch rests briefly, so a scroll that starts
// on a row does not flash it; a quick tap still flashes on release.
const IOS_HIGHLIGHT_DELAY_MS = 80
const IOS_HIGHLIGHT_FADE_MS = 300
const IOS_DIM_OPACITY = 0.35
const IOS_DIM_RELEASE_MS = 200
// Wider than this share of the viewport reads as a row rather than a button.
const IOS_ROW_WIDTH_RATIO = 0.6

interface ActivePress {
  release: () => void
}

let active: ActivePress | null = null

function findTarget(event: PointerEvent): HTMLElement | null {
  if (!(event.target instanceof Element)) return null
  const target = event.target.closest<HTMLElement>(PRESSABLE)
  if (!target || target.matches(EXCLUDED) || target.closest(EXCLUDED)) return null
  if (target.matches(':disabled, [aria-disabled="true"], [data-disabled]')) return null
  return target
}

// The feedback layer: absolutely positioned over the target, clipped to its
// rounded shape, ignoring pointer events. For the duration of the press only, a
// static target is made relative and every target is isolated, so the iOS
// highlight (z-index -1, under the row's content like a cell's selected
// background) stays inside the row instead of sinking behind an ancestor card.
function attachLayer(target: HTMLElement) {
  const restorePosition = getComputedStyle(target).position === 'static'
  if (restorePosition) target.style.position = 'relative'
  const previousIsolation = target.style.isolation
  target.style.isolation = 'isolate'
  const layer = document.createElement('span')
  layer.dataset.pressLayer = ''
  // Copy the target's corners clamped to half its shorter side. Inheriting a
  // fully-rounded control's radius (calc(infinity), ~3.4e7px) makes Chrome
  // drop the rounded overflow clip and paint nothing at all.
  const style = getComputedStyle(target)
  const rect = target.getBoundingClientRect()
  const maxRadius = Math.min(rect.width, rect.height) / 2
  layer.style.borderRadius = [style.borderTopLeftRadius, style.borderTopRightRadius, style.borderBottomRightRadius, style.borderBottomLeftRadius]
    .map(radius => `${Math.min(Number.parseFloat(radius) || 0, maxRadius)}px`)
    .join(' ')
  target.append(layer)
  return () => {
    layer.remove()
    target.style.isolation = previousIsolation
    if (restorePosition) target.style.position = ''
  }
}

function startRipple(target: HTMLElement, event: PointerEvent): ActivePress {
  const rect = target.getBoundingClientRect()
  const detach = attachLayer(target)
  const layer = target.lastElementChild as HTMLElement
  const wave = document.createElement('span')
  wave.dataset.pressRipple = ''
  layer.append(wave)

  const targetRadius = Math.hypot(rect.width, rect.height) / 2 + RIPPLE_EXTRA_RADIUS
  const startRadius = Math.max(rect.width, rect.height) * 0.3
  const centerX = rect.width / 2
  const centerY = rect.height / 2
  const pressX = event.clientX - rect.left
  const pressY = event.clientY - rect.top
  wave.style.width = `${targetRadius * 2}px`
  wave.style.height = `${targetRadius * 2}px`
  wave.style.left = `${centerX - targetRadius}px`
  wave.style.top = `${centerY - targetRadius}px`

  const fadeIn = wave.animate(
    [{ opacity: 0 }, { opacity: RIPPLE_ALPHA }],
    { duration: RIPPLE_FADE_IN_MS, easing: 'linear', fill: 'forwards' },
  )
  const grow = wave.animate(
    [{ scale: `${startRadius / targetRadius}` }, { scale: '1' }],
    { duration: RIPPLE_GROW_MS, easing: FAST_OUT_SLOW_IN, fill: 'forwards' },
  )
  wave.animate(
    [{ translate: `${pressX - centerX}px ${pressY - centerY}px` }, { translate: '0px 0px' }],
    { duration: RIPPLE_GROW_MS, easing: 'linear', fill: 'forwards' },
  )

  return {
    release() {
      void Promise.all([fadeIn.finished, grow.finished]).catch(() => {}).then(() => {
        const fadeOut = wave.animate(
          [{ opacity: RIPPLE_ALPHA }, { opacity: 0 }],
          { duration: RIPPLE_FADE_OUT_MS, easing: 'linear', fill: 'forwards' },
        )
        void fadeOut.finished.catch(() => {}).then(detach)
      })
    },
  }
}

function startIosHighlight(target: HTMLElement): ActivePress {
  const detach = attachLayer(target)
  const layer = target.lastElementChild as HTMLElement
  layer.dataset.pressHighlight = ''
  let shown = false
  const show = () => {
    shown = true
    layer.style.opacity = '1'
  }
  const timer = window.setTimeout(show, IOS_HIGHLIGHT_DELAY_MS)
  return {
    release() {
      window.clearTimeout(timer)
      if (!shown) show()
      const fadeOut = layer.animate(
        [{ opacity: 1 }, { opacity: 0 }],
        { duration: IOS_HIGHLIGHT_FADE_MS, easing: 'ease-out', fill: 'forwards' },
      )
      void fadeOut.finished.catch(() => {}).then(detach)
    },
  }
}

function startIosDim(target: HTMLElement): ActivePress {
  const dim = target.animate(
    [{ opacity: IOS_DIM_OPACITY }],
    { duration: 0, fill: 'forwards' },
  )
  return {
    release() {
      dim.cancel()
      target.animate(
        [{ opacity: IOS_DIM_OPACITY }, { opacity: 1 }],
        { duration: IOS_DIM_RELEASE_MS, easing: 'ease-out' },
      )
    },
  }
}

function startPress(mode: PressFeel, target: HTMLElement, event: PointerEvent): ActivePress | null {
  if (mode === 'ripple') return startRipple(target, event)
  if (mode === 'ios') {
    const isRow = target.getBoundingClientRect().width >= window.innerWidth * IOS_ROW_WIDTH_RATIO
    return isRow ? startIosHighlight(target) : startIosDim(target)
  }
  return null
}

function releaseActive() {
  active?.release()
  active = null
}

export function installPressFeel() {
  const mobile = window.matchMedia(MOBILE_QUERY)
  document.addEventListener('pointerdown', (event) => {
    if (feel.value === 'baseline' || !mobile.matches || !event.isPrimary || event.button !== 0) return
    const target = findTarget(event)
    if (!target) return
    releaseActive()
    active = startPress(feel.value, target, event)
  }, { capture: true, passive: true })
  // pointercancel covers a touch that turns into a scroll: the press is
  // abandoned and the feedback retracts, as both platforms do.
  for (const type of ['pointerup', 'pointercancel'] as const) {
    document.addEventListener(type, (event) => {
      if (event.isPrimary) releaseActive()
    }, { capture: true, passive: true })
  }
}
