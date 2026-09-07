<template>
  <div
    class="grid transition-[grid-template-rows] duration-[400ms] ease-[cubic-bezier(0.16,1,0.3,1)] motion-reduce:transition-none"
    :class="expanded ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]'"
  >
    <div class="min-h-0 overflow-hidden">
      <slot v-if="rendered" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch, nextTick } from 'vue'

const props = defineProps<{ open: boolean }>()

// Smoothly animate height:auto via the grid 0fr↔1fr technique. Content is
// mounted lazily (only after the section is first opened) so collapsed tool
// details don't run expensive work (e.g. Shiki) up front. On the first open we
// mount the content at 0fr, then flip to 1fr next frame so the height grows
// instead of snapping; collapsing animates back to 0fr and keeps the content
// mounted so re-opening also animates.
const rendered = ref(props.open)
const expanded = ref(props.open)

watch(
  () => props.open,
  async (open) => {
    if (open) {
      rendered.value = true
      await nextTick()
      // Double rAF so the freshly-mounted content is painted at 0fr for one
      // frame before flipping to 1fr — otherwise the height snaps open instead
      // of animating (this is why nested details didn't animate on first open).
      requestAnimationFrame(() => requestAnimationFrame(() => {
        // A second click can close the section before these frames run.
        if (props.open) expanded.value = true
      }))
    }
    else {
      expanded.value = false
    }
  },
)
</script>
