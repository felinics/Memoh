<template>
  <!-- Truncating one-line label with hover marquee. -->
  <span
    ref="viewport"
    class="marquee-viewport"
    :class="{ 'is-truncated': motion.truncated }"
    :style="motionStyle"
  >
    <!-- Only --marquee-pos is ANIMATED (on the viewport; it inherits down).
         The content's transform and the viewport's mask are pure functions of
         it, so the scroll and both fades stay in lockstep by construction:
           left fade  = min(pos, 12px)          — grows as the head scrolls off
           right fade = min(travel - pos, 28px) — shrinks as the tail arrives
         Resting at home: right fade only (the ellipsis replacement). Resting
         at the tail: left fade only, tail crisp at the right edge with no
         dead gap before the actions slot. -->
    <span
      ref="content"
      class="marquee-content"
    >
      <slot />
    </span>
  </span>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { computeMarqueeMotion, MARQUEE_OVERSHOOT_PX } from './marquee-motion'

const viewport = ref<HTMLElement>()
const content = ref<HTMLElement>()
const overflowPx = ref(0)

let observer: ResizeObserver | undefined

function measure() {
  const box = viewport.value
  const inner = content.value
  if (!box || !inner) return
  overflowPx.value = inner.scrollWidth - box.clientWidth
}

const motion = computed(() => computeMarqueeMotion(overflowPx.value))

const motionStyle = computed(() => {
  if (!motion.value.truncated) return {}
  return {
    '--marquee-travel': `${motion.value.travelPx}px`,
    '--marquee-overshoot': `${MARQUEE_OVERSHOOT_PX}px`,
    '--marquee-duration': `${motion.value.durationMs}ms`,
  }
})

onMounted(() => {
  measure()
  // Sidebar width changes (drag), font loading, and title edits all resize
  // one of the two boxes — the observer catches every path, so nothing else
  // needs to trigger re-measurement.
  observer = new ResizeObserver(measure)
  if (viewport.value) observer.observe(viewport.value)
  if (content.value) observer.observe(content.value)
})

onBeforeUnmount(() => observer?.disconnect())
</script>

<!-- Unscoped by design: the hover trigger lives on the row (`.group`), an
     ancestor this component does not own — scoped + :global() collapses the
     compound selector down to the global part alone (verified: it compiled
     to a bare `.group:hover` rule and the scroll never fired). The marquee-*
     class names are unique to this component, so a plain block is exact. -->
<style>
/* Only --marquee-pos is animated (registered below so keyframes can
   interpolate it); the transform and mask are pure functions of it. */

.marquee-viewport {
  display: block;
  overflow: hidden;
  min-width: 0;
}

.marquee-content {
  display: inline-block;
  white-space: nowrap;
  /* The clamp turns the animated offset into the painted position and pins
     BOTH ends, so each overshoot is a true hold rather than a bounce. pos
     runs -Overshoot → travel+Overshoot; painted x = -pos while
     0 ≤ pos ≤ travel (the scroll). Outside that range the clamp snaps x to
     0 (home overshoot) or -travel (far overshoot) — WITHOUT the far clamp
     the tail slides Overshoot past its flush point and a gap flashes at the
     right edge. The pinned intervals ARE the dwells at each end. */
  transform: translateX(
    clamp(
      calc(-1 * var(--marquee-travel, 0px)),
      calc(-1 * var(--marquee-pos)),
      0px
    )
  );
}

/* The mask MUST live on the viewport, not the content: gradient percentages
   resolve against the masked element's own box. The content box is the full
   text width, so `100%` there points off-screen past the visible window and
   the right fade never appears — that exact bug shipped once. On the viewport
   `100%` is the visible window, and both fades are continuous functions of
   --marquee-pos, so they widen/narrow smoothly AS the text moves. */
.marquee-viewport.is-truncated {
  mask-image: linear-gradient(
    to right,
    transparent,
    #000 min(var(--marquee-pos), 12px),
    #000 calc(100% - min(var(--marquee-travel) - var(--marquee-pos), 28px)),
    transparent
  );
}

/* Scrolling keys off the row's emphasis state — :hover OR data-menu-open
   (class `group` on the row root). When the dropdown is open the cursor sits
   over the portaled menu, so :hover alone is gone from the row and the scroll
   would freeze mid-read; data-menu-open (set by the row while its dropdown is
   open) keeps the row emphasized until the menu closes. Pointing at the
   trailing actions keeps the text reachable for the same reason.
   The 0.2s delay is hover-intent gating — long enough that a passing cursor
   doesn't twitch every row, short enough to feel immediate. During the delay
   --marquee-pos is still 0, so the title shows the idle right-fade state with
   no special-case rule. */
@media (prefers-reduced-motion: no-preference) {
  .group:hover .marquee-viewport.is-truncated,
  .group[data-menu-open='true'] .marquee-viewport.is-truncated {
    animation: marquee-pingpong var(--marquee-duration, 4s) linear 0.2s infinite;
  }
}

/* The hold at each end is a CLAMP, not a timed stop: --marquee-pos is
   animated from -Overshoot to travel+Overshoot (a raw transform would give
   the same motion but couldn't feed the mask), so during the overshoots the
   clamped transform above pins the paint at the edge — that pinned interval
   IS the dwell, a constant 2·Overshoot/speed at each end (400ms at the current constants).
   Why clamp instead of percentage stops: the duration must grow with travel
   to keep the reading speed constant, and percentage dwells grow with it —
   a 400px title sat ~1.9s before its first pixel moved while a 30px one
   started in 143ms (the "hover forever before it scrolls" bug). A clamped
   overshoot is a fixed duration no matter how long the title is. */
@property --marquee-pos {
  syntax: '<length>';
  inherits: true;
  initial-value: 0px;
}

@keyframes marquee-pingpong {
  0% {
    --marquee-pos: calc(-1 * var(--marquee-overshoot, 6px));
  }
  50% {
    --marquee-pos: calc(var(--marquee-travel, 0px) + var(--marquee-overshoot, 6px));
  }
  100% {
    --marquee-pos: calc(-1 * var(--marquee-overshoot, 6px));
  }
}
</style>
