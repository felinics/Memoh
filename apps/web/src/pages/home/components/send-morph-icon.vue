<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

// The send button's arrow and its busy ring are one drawing. On `busy` the
// arrow's stem bends into the ring's arc while the head folds into the arc's
// leading tip and the ring starts turning, so the icon reads as one shape
// changing rather than two icons swapped. Path data is interpolated in JS:
// CSS `d` transitions are not available in every browser the web app serves.
const props = defineProps<{ busy: boolean }>()
const { t } = useI18n()

// Both shapes are three cubic segments so each control point has a partner.
// The stem runs from the bottom (19) to just under the tip (5.75); the arc
// covers 72% of a r=9 circle, ending at the top (12, 3), which leads when the
// ring turns clockwise.
const STEM = [
  [12, 19],
  [12, 17.53], [12, 16.06], [12, 14.58],
  [12, 13.11], [12, 11.64], [12, 10.17],
  [12, 8.69], [12, 7.22], [12, 5.75],
] as const
const ARC = [
  [20.84, 13.69],
  [19.95, 18.35], [15.59, 21.52], [10.87, 20.93],
  [6.16, 20.33], [2.72, 16.18], [3.02, 11.43],
  [3.32, 6.69], [7.25, 3], [12, 3],
] as const
// Arrowhead wings and tip; at the end of the morph they collapse onto the
// arc's tip and fade out.
const HEAD = [[6.5, 10.5], [12, 5], [17.5, 10.5]] as const
const HEAD_END = [12, 3] as const

const MORPH_IN_MS = 200
const MORPH_OUT_MS = 170
const TURN_MS = 900
// The dim part of the ring: enough to close the circle, faint enough that
// the bright arc still reads as the moving part.
const TRACK_OPACITY = 0.18

const progress = ref(props.busy ? 1 : 0)
const angle = ref(0)

// The morph answers a click, so it moves at once and slows into the new
// shape; an ease-in start would read as lag at this length.
const easeOutQuart = (x: number) => 1 - (1 - x) ** 4
const easeOut = (x: number) => 1 - (1 - x) ** 3
const lerp = (a: number, b: number, t: number) => a + (b - a) * t
const fmt = (n: number) => n.toFixed(2)

const reducedMotion = typeof window !== 'undefined'
  && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches

let frame = 0
let last = 0

function stop() {
  if (frame) cancelAnimationFrame(frame)
  frame = 0
}

// One loop drives both the morph and the turn. The ring's speed follows the
// morph, so it spins up as the arc forms instead of starting at full speed.
// Going back, the angle eases to the next upright turn so the arrow lands
// straight.
function run() {
  stop()
  if (reducedMotion) {
    progress.value = props.busy ? 1 : 0
    angle.value = 0
    return
  }
  const from = progress.value
  const to = props.busy ? 1 : 0
  const duration = (props.busy ? MORPH_IN_MS : MORPH_OUT_MS) * Math.abs(to - from)
  const angleFrom = angle.value
  const angleTo = Math.ceil(angleFrom / 360) * 360
  const start = performance.now()
  last = start
  const tick = (now: number) => {
    const elapsed = now - start
    const linear = duration > 0 ? Math.min(1, elapsed / duration) : 1
    progress.value = lerp(from, to, linear)
    if (props.busy) {
      angle.value += (360 * (now - last) / TURN_MS) * eased.value
    } else {
      angle.value = lerp(angleFrom, angleTo, easeOut(linear))
    }
    last = now
    if (props.busy || linear < 1) {
      frame = requestAnimationFrame(tick)
      return
    }
    angle.value = 0
    frame = 0
  }
  frame = requestAnimationFrame(tick)
}

watch(() => props.busy, run)
if (props.busy) run()
onBeforeUnmount(stop)

// Progress is time-linear in both directions; the curve is applied here, so a
// reversal mid-morph continues from the shape on screen.
const eased = computed(() => easeOutQuart(progress.value))

const bodyPath = computed(() => {
  const t = eased.value
  const p = STEM.map(([x, y], i) => [lerp(x, ARC[i]![0], t), lerp(y, ARC[i]![1], t)])
  const at = (i: number) => `${fmt(p[i]![0]!)} ${fmt(p[i]![1]!)}`
  return `M${at(0)} C${at(1)} ${at(2)} ${at(3)} C${at(4)} ${at(5)} ${at(6)} C${at(7)} ${at(8)} ${at(9)}`
})

const headPath = computed(() => {
  const t = eased.value
  const pts = HEAD.map(([x, y]) => `${fmt(lerp(x, HEAD_END[0], t))} ${fmt(lerp(y, HEAD_END[1], t))}`)
  return `M${pts[0]} L${pts[1]} L${pts[2]}`
})

// The head is gone by the time the arc settles; the track fades in only in
// the second half so the arrow never sits on a visible circle.
const headOpacity = computed(() => Math.max(0, 1 - eased.value / 0.7))
const trackOpacity = computed(() => TRACK_OPACITY * Math.max(0, (eased.value - 0.5) / 0.5))
const rotation = computed(() => `rotate(${fmt(angle.value)} 12 12)`)
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2.5"
    stroke-linecap="round"
    stroke-linejoin="round"
    :role="busy ? 'status' : undefined"
    :aria-label="busy ? t('common.loading') : undefined"
  >
    <circle
      cx="12"
      cy="12"
      r="9"
      :stroke-opacity="trackOpacity"
    />
    <g :transform="rotation">
      <path :d="bodyPath" />
      <path
        v-if="headOpacity > 0"
        :d="headPath"
        :stroke-opacity="headOpacity"
      />
    </g>
  </svg>
</template>
