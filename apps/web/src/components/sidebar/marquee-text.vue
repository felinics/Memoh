<template>
  <span
    ref="viewport"
    class="marquee-viewport"
    :class="{ 'is-truncated': motion.truncated, 'is-overlay-truncated': overlayMotion.truncated }"
    :style="motionStyle"
  >
    <span
      ref="content"
      class="marquee-content"
    >
      <slot />
    </span>
  </span>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { computeMarqueeMotion, MARQUEE_OVERSHOOT_PX } from './marquee-motion'

const props = defineProps<{
  overlay?: HTMLElement
}>()

const viewport = ref<HTMLElement>()
const content = ref<HTMLElement>()
const overflowPx = ref(0)
const overlayWidthPx = ref(0)

let observer: ResizeObserver | undefined

function measure() {
  const box = viewport.value
  const inner = content.value
  if (!box || !inner) return
  // Fractional glyph widths must reach the mask edge without clipping a tail pixel.
  overflowPx.value = inner.getBoundingClientRect().width - box.getBoundingClientRect().width
  overlayWidthPx.value = props.overlay?.getBoundingClientRect().width ?? 0
}

// Both endpoints are measured before hover. CSS picks the covered endpoint
// without changing the viewport width or scheduling a Vue update.
const motion = computed(() => computeMarqueeMotion(overflowPx.value))
const overlayMotion = computed(() => computeMarqueeMotion(overflowPx.value, overlayWidthPx.value))
const motionStyle = computed(() => ({
  '--marquee-idle-travel': `${motion.value.travelPx}px`,
  '--marquee-idle-duration': `${motion.value.durationMs}ms`,
  '--marquee-overlay-travel': `${overlayMotion.value.travelPx}px`,
  '--marquee-overlay-duration': `${overlayMotion.value.durationMs}ms`,
  '--marquee-overlay-width': `${overlayWidthPx.value}px`,
  '--marquee-overshoot': `${MARQUEE_OVERSHOOT_PX}px`,
}))

// The parent's overlay ref becomes available after this child mounts. Observe
// its actual size too, so rem-based controls and custom font sizes stay aligned.
watch(() => props.overlay, (overlay, previous) => {
  if (previous) observer?.unobserve(previous)
  if (overlay) observer?.observe(overlay)
  measure()
}, { flush: 'post' })

onMounted(() => {
  measure()
  observer = new ResizeObserver(measure)
  if (viewport.value) observer.observe(viewport.value)
  if (content.value) observer.observe(content.value)
  if (props.overlay) observer.observe(props.overlay)
})

onBeforeUnmount(() => observer?.disconnect())
</script>

<!-- The row owns emphasis, so these selectors intentionally cross the
     component boundary. Unique marquee classes keep the rules local. -->
<style>
.marquee-viewport {
  display: block;
  overflow: hidden;
  min-width: 0;
  --marquee-travel: var(--marquee-idle-travel, 0px);
  --marquee-duration: var(--marquee-idle-duration, 0ms);
  --marquee-end-inset: 0px;
  /* Keep text covered until the outgoing actions finish their opacity fade. */
  transition: --marquee-end-inset 0s linear 150ms;
}

.marquee-content {
  display: inline-block;
  white-space: nowrap;
  /* Overshoot becomes a fixed reading pause, while both endpoints stay pinned. */
  transform: translateX(clamp(calc(-1 * var(--marquee-travel)), calc(-1 * var(--marquee-pos)), 0px));
}

.marquee-viewport:is(.is-truncated, .is-overlay-truncated) {
  /* The solid end mask protects the controls; the adjacent fade shrinks to
     zero as the title tail reaches that edge, so its final letters stay crisp. */
  mask-image: linear-gradient(
    to right,
    transparent,
    #000 clamp(0px, var(--marquee-pos), 12px),
    #000 calc(100% - var(--marquee-end-inset) - clamp(0px, calc(var(--marquee-travel) - var(--marquee-pos)), 28px)),
    transparent calc(100% - var(--marquee-end-inset)),
    transparent
  );
}

[data-slot='sidebar-session-row']:is([data-menu-open='true'], [data-streaming='true'], :has([data-slot='sidebar-session-overlay'] button:focus-visible)) .marquee-viewport {
  --marquee-end-inset: var(--marquee-overlay-width);
  --marquee-travel: var(--marquee-overlay-travel);
  --marquee-duration: var(--marquee-overlay-duration);
  transition-delay: 0s;
}

/* Match the actions' hover capability: a synthetic touch hover must not
   reserve space for a button that remains hidden on a touch-only device. */
@media (hover: hover) {
  [data-slot='sidebar-session-row']:hover .marquee-viewport {
    --marquee-end-inset: var(--marquee-overlay-width);
    --marquee-travel: var(--marquee-overlay-travel);
    --marquee-duration: var(--marquee-overlay-duration);
    transition-delay: 0s;
  }
}

@media (prefers-reduced-motion: no-preference) {
  [data-slot='sidebar-session-row']:is([data-menu-open='true'], :has([data-slot='sidebar-session-overlay'] button:focus-visible)) .marquee-viewport.is-overlay-truncated {
    animation: marquee-pingpong var(--marquee-duration) linear 0.2s infinite;
  }

  @media (hover: hover) {
    [data-slot='sidebar-session-row']:hover .marquee-viewport.is-overlay-truncated {
      animation: marquee-pingpong var(--marquee-duration) linear 0.2s infinite;
    }
  }
}

@property --marquee-pos {
  syntax: '<length>';
  inherits: true;
  initial-value: 0px;
}

@property --marquee-end-inset {
  syntax: '<length>';
  inherits: false;
  initial-value: 0px;
}

@keyframes marquee-pingpong {
  0%, 100% {
    --marquee-pos: calc(-1 * var(--marquee-overshoot));
  }
  50% {
    --marquee-pos: calc(var(--marquee-travel) + var(--marquee-overshoot));
  }
}
</style>
