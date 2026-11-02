<template>
  <!-- Mobile main navigation as a full-width page to the left of the chat.
       The navigation and the chat are peers: opening slides both right by the
       page width, nothing dims and neither sits above the other. The chat
       column is NOT re-parented — it is the host's own element, passed in as
       mainPage — so switching between this and the sheet form never remounts
       the workspace. Ported from the mobile interaction prototype's room-list
       drawer (spring values and gesture thresholds unchanged). The page hosts
       the mobile home (sessions first), not the desktop rail's contents. -->
  <div
    ref="navPage"
    data-mobile-nav-page
    class="safe-area-frame absolute inset-0 bg-background text-foreground"
    :inert="!mobileNavOpen || undefined"
    :aria-label="t('chat.mobileNav.title')"
    role="navigation"
  >
    <MobileHome />
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { CompositorSpring, PAGE_SPRING, rubberBand, VelocityTracker } from '@/lib/compositor-spring'
import MobileHome from './mobile-home.vue'

const props = defineProps<{
  /** The chat column that slides out to the right while the navigation is open. */
  mainPage: HTMLElement | null
}>()

// Gesture thresholds from the prototype: a drag commits once it moves 8px and
// is mostly horizontal; on release the position is projected 140ms ahead and
// compared against a different threshold per direction, and a fast fling
// (500px/s) commits regardless of distance.
const SLOP = 8
const HORIZONTAL_RATIO = 1.2
const PROJECTION_S = 0.14
const OPEN_THRESHOLD = 0.42
const CLOSE_THRESHOLD = 0.55
const FLING_VELOCITY = 500
const BUTTON_VELOCITY = 400
const OVERDRAG_RANGE = 60

// Targets a drag must not start from: text entry, and anything that scrolls
// horizontally itself (code blocks, tables, tab strips).
const NO_DRAG = 'textarea, input, select, [contenteditable="true"], [data-slot="input-group"], [role="dialog"]'

const { t } = useI18n()
const workspaceTabs = useWorkspaceTabsStore()
const { mobileNavOpen } = storeToRefs(workspaceTabs)

const navPage = ref<HTMLElement | null>(null)
let width = 0

const spring = new CompositorSpring(0, () => [
  { el: navPage.value, prop: 'transform', format: x => `translate3d(${(x - width).toFixed(2)}px, 0, 0)` },
  { el: props.mainPage, prop: 'transform', format: x => `translate3d(${x.toFixed(2)}px, 0, 0)` },
])

function measure() {
  width = navPage.value?.offsetWidth || width
}

// Velocity to start the next store-driven spring with, set by a gesture
// release so the page continues at the finger's speed.
let releaseVelocity: number | null = null

watch(mobileNavOpen, (open) => {
  measure()
  if (open && document.activeElement instanceof HTMLElement) document.activeElement.blur()
  const velocity = releaseVelocity ?? (open ? BUTTON_VELOCITY : -BUTTON_VELOCITY)
  releaseVelocity = null
  spring.springTo(open ? width : 0, velocity, PAGE_SPRING)
})

watch(() => [mobileNavOpen.value, props.mainPage] as const, ([open, main]) => {
  if (main) main.inert = open
})

// ── Drag ─────────────────────────────────────────────────────────────────────
type GestureState = 'pending' | 'dragging' | 'ignored' | null
const tracker = new VelocityTracker()
let gesture: GestureState = null
let start = { x: 0, y: 0, value: 0 }
let suppressClickUntil = 0

function scrollsHorizontally(target: Element, stopAt: Element) {
  for (let el: Element | null = target; el && el !== stopAt; el = el.parentElement) {
    if (el.scrollWidth > el.clientWidth + 1) {
      const overflowX = getComputedStyle(el).overflowX
      if (overflowX === 'auto' || overflowX === 'scroll') return true
    }
  }
  return false
}

function onPointerDown(event: PointerEvent) {
  if (event.button !== 0 || !event.isPrimary || !(event.target instanceof Element)) return
  const page = mobileNavOpen.value ? navPage.value : props.mainPage
  if (!page || !page.contains(event.target)) return
  if (event.target.closest(NO_DRAG) || scrollsHorizontally(event.target, page)) return
  gesture = 'pending'
  start = { x: event.clientX, y: event.clientY, value: 0 }
  tracker.reset()
  tracker.add(event.clientX)
}

function onPointerMove(event: PointerEvent) {
  if (!gesture || gesture === 'ignored' || !event.isPrimary) return
  const dx = event.clientX - start.x
  const dy = event.clientY - start.y

  if (gesture === 'pending') {
    if (Math.abs(dx) < SLOP && Math.abs(dy) < SLOP) return
    const horizontal = Math.abs(dx) > Math.abs(dy) * HORIZONTAL_RATIO
    const towardChange = (dx > 0 && !mobileNavOpen.value) || (dx < 0 && mobileNavOpen.value)
    if (!horizontal || !towardChange) {
      gesture = 'ignored'
      return
    }
    gesture = 'dragging'
    measure()
    spring.sampleMidAirIfRunning()
    start = { x: event.clientX, y: event.clientY, value: spring.current }
    if (!mobileNavOpen.value && document.activeElement instanceof HTMLElement) document.activeElement.blur()
  }

  tracker.add(event.clientX)
  const raw = start.value + (event.clientX - start.x)
  const x = raw < 0 ? rubberBand(raw, OVERDRAG_RANGE) : Math.min(raw, width)
  spring.setImmediate(x, tracker.getVelocity())
}

function onPointerUp(event: PointerEvent) {
  if (!event.isPrimary) return
  if (gesture === 'dragging') {
    suppressClickUntil = performance.now() + 80
    const v = tracker.getVelocity()
    const projected = spring.current + v * PROJECTION_S
    const open = mobileNavOpen.value
    const nextOpen = open
      ? !(projected < width * CLOSE_THRESHOLD || v < -FLING_VELOCITY)
      : projected > width * OPEN_THRESHOLD || v > FLING_VELOCITY
    if (nextOpen === open) {
      spring.springTo(open ? width : 0, v, PAGE_SPRING)
    }
    else {
      releaseVelocity = v
      if (nextOpen) workspaceTabs.openMobileNav()
      else workspaceTabs.closeMobileNav()
    }
  }
  gesture = null
}

function onPointerCancel(event: PointerEvent) {
  if (!event.isPrimary) return
  if (gesture === 'dragging') spring.springTo(mobileNavOpen.value ? width : 0, 0, PAGE_SPRING)
  gesture = null
}

// A drag that ends over a button must not also click it.
function onClickCapture(event: MouseEvent) {
  if (performance.now() < suppressClickUntil) {
    event.stopPropagation()
    event.preventDefault()
  }
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && mobileNavOpen.value) workspaceTabs.closeMobileNav()
}

let resizeObserver: ResizeObserver | null = null
let borrowedMain: HTMLElement | null = null

onMounted(() => {
  measure()
  spring.setImmediate(mobileNavOpen.value ? width : 0)
  // The pager borrows the host's element for its lifetime; it does not own it.
  borrowedMain = props.mainPage
  if (borrowedMain) {
    borrowedMain.inert = mobileNavOpen.value
    borrowedMain.dataset.mobileMainPage = ''
  }
  resizeObserver = new ResizeObserver(() => {
    measure()
    if (gesture !== 'dragging') spring.setImmediate(mobileNavOpen.value ? width : 0)
  })
  if (navPage.value) resizeObserver.observe(navPage.value)
  document.addEventListener('pointerdown', onPointerDown, { capture: true, passive: true })
  window.addEventListener('pointermove', onPointerMove, { passive: true })
  window.addEventListener('pointerup', onPointerUp, { passive: true })
  window.addEventListener('pointercancel', onPointerCancel, { passive: true })
  document.addEventListener('click', onClickCapture, true)
  window.addEventListener('keydown', onKeydown)
})

// Leaving the pager form (or the mobile shell) hands the chat column back
// untouched: no transform, not inert.
onBeforeUnmount(() => {
  spring.cancelAnims()
  resizeObserver?.disconnect()
  document.removeEventListener('pointerdown', onPointerDown, { capture: true })
  window.removeEventListener('pointermove', onPointerMove)
  window.removeEventListener('pointerup', onPointerUp)
  window.removeEventListener('pointercancel', onPointerCancel)
  document.removeEventListener('click', onClickCapture, true)
  window.removeEventListener('keydown', onKeydown)
  // Held from mount: the host's template ref may already be cleared here.
  if (borrowedMain) {
    borrowedMain.style.transform = ''
    borrowedMain.inert = false
    delete borrowedMain.dataset.mobileMainPage
  }
})
</script>

<style>
/* Horizontal drags belong to the page switch. Without this the browser claims
   a sideways touch for its own panning and cancels the pointer stream halfway.
   Touch-action resolves up to the nearest scroll container, so the pages and
   their vertical scrollers are enough; horizontal scrollers (code blocks,
   tables) are scroll containers of their own and keep sideways panning. */
:is([data-mobile-nav-page], [data-mobile-main-page]),
:is([data-mobile-nav-page], [data-mobile-main-page]) :is(.overflow-y-auto, .overflow-auto, [data-slot="scroll-area-viewport"]) {
  touch-action: pan-y pinch-zoom;
}
</style>
