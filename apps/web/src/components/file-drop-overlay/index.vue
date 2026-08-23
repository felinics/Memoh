<template>
  <!-- Full-bleed drop feedback, Teleported to <body>.
       WHY not scoped to the region it belongs to (the first shape this took):
       a region-scoped sheet can only cover the region's CONTENT, never the
       region's own chrome, because that chrome is its ancestor. On the left the
       sidebar's panel host is overflow-hidden, so the sheet was clipped to the
       middle list and the Files/Chat/Schedule row above and Settings row below
       stayed sharp. On the right the tab strip is the dock library's own DOM,
       a sibling ABOVE our panel content — unreachable from inside at any depth.
       Both left a hard edge between treated and untreated, which in light mode
       reads as a colour seam. That was never a colour problem to tune; it was
       the covered area being smaller than the region. So the sheet leaves the
       region entirely and covers the window.

       Teleport, not position:fixed in place: main-section's root carries a
       resting `scale-100` from its entry animation, and any non-none transform
       makes that element the containing block for fixed descendants. It happens
       to be viewport-sized today, so fixed would look right — and would break
       silently the day that shell gains an inset or a transformed wrapper.

       WHICH HALF gets the files is then the one thing a window-wide sheet can no
       longer say by its own outline, so the anchor block is positioned over the
       live region instead of the window centre. Position carries the aim; the
       cover carries the state.

       NO transition in either direction. Enter: the user is holding a file over
       the window asking "will this land?" — any ramp reads as the app thinking
       about it. Leave: zones hand off at region edges (see useFileDropZone's
       nesting rules), and a fading leave briefly STACKS the old sheet over the
       new one — two 82% covers composite to ~97%, which in dark mode (where the
       cover is near-black) reads as a black flash at every boundary. Deactivation
       is already debounced there, so hard cuts are seam-free.

       pointer-events-none throughout so the sheet can never intercept the drop
       it is announcing — the region root underneath stays the drop target. -->
  <Teleport to="body">
    <div
      v-if="active"
      :class="scrimClass"
      aria-hidden="true"
    >
      <!-- Anchored to the region when its box is known; the first dragenter
           measures before this ever renders, so the centred fallback is only
           for a region that reports no box at all. -->
      <div
        :class="anchorClass"
        :style="anchorStyle"
      >
        <component
          :is="icon"
          :stroke-width="1.5"
          :class="compact ? 'size-10 text-accent-blue' : 'size-12 text-accent-blue'"
        />
        <span :class="compact ? 'text-title font-semibold text-foreground' : 'text-display font-semibold text-foreground'">{{ label }}</span>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import type { Component } from 'vue'
import { computed } from 'vue'
import type { DropZoneBounds } from '@/composables/useFileDropZone'

const props = defineProps<{
  active: boolean
  label: string
  icon: Component
  // Viewport box of the region being aimed at, from useFileDropZone.
  bounds?: DropZoneBounds | null
}>()

// One layer, so there is no internal edge left to read as a seam: the app's own
// background at 82%, NO blur, no black dim. Blur is out by standing instruction
// (banned once, re-added by me chasing 透亮, banned again on reaffirmation).
// Pure alpha only controls HOW MUCH shows through, never how sharp — so without
// blur the viable band is narrow and three points are rejected: 80% leaves
// sharp text ghosts at #CFCFCF (reads grey/busy); 95% hides everything (dead
// white sheet); 88% still mutes black text too much. 82% → ghosts #D3D3D3.
// Accepted tradeoff: with no blur those ghosts keep sharp edges; inherent.
// NOT --scrim: that token is a black dim for modals — the rejected "pressed
// dead" look.
const scrimClass = 'pointer-events-none fixed inset-0 z-(--z-top) bg-background/82' /* ui-allow-alpha: overlay scrim — a full-bleed cover layer is its own role (packages/ui/AGENTS.md § Alpha policy allowlist) */

// The ICON alone carries the blue; the label stays plain foreground. That split
// is straight from the reference: black text under a blue glyph. --accent-blue
// is the icon/text accent role (saturated, state-constant, tuned per scheme),
// NOT --accent-blue-fill — that fill token is the same blue but reserved for
// SOLID fills on selection controls (Checkbox / Switch). Blue reads as
// "selected / targeted", which is what the aimed drop region is. Deliberately
// NOT --brand purple: the contract reserves purple for brand CTAs.
// Narrow regions get the smaller rung so the label stays on one line: the
// sidebar is ~260–320px wide, where text-display wraps a sentence to three
// lines. 480px sits between the sidebar and any realistic chat split.
const compact = computed(() => (props.bounds?.width ?? Number.POSITIVE_INFINITY) < 480)

const anchorClass = computed(() => props.bounds
  ? `absolute flex flex-col items-center justify-center ${compact.value ? 'gap-3' : 'gap-4'} px-4 text-center`
  : 'absolute inset-0 flex flex-col items-center justify-center gap-4 px-4 text-center')

const anchorStyle = computed<Record<string, string> | undefined>(() => {
  const box = props.bounds
  if (!box) return undefined
  return {
    left: `${box.left}px`,
    top: `${box.top}px`,
    width: `${box.width}px`,
    height: `${box.height}px`,
  }
})
</script>
