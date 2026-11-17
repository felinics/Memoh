<script setup lang="ts">
import { ArrowUpRight } from 'lucide-vue-next'

// The back-link inside a footnote definition (markstream's "↩︎"). Rendered as
// the same up-right arrow as the in-body reference — foreground at rest, brand
// purple on hover — so the two ends of a footnote read as one affordance. Jumps
// back to the reference at `#fnref-{id}` (single dash), matching markstream's id.
const props = defineProps<{
  node: { type: 'footnote_anchor', id: string, raw?: string }
}>()

function scrollToReference() {
  document.getElementById(`fnref-${props.node.id}`)?.scrollIntoView({ behavior: 'smooth' })
}
</script>

<template>
  <a
    :href="`#fnref-${node.id}`"
    class="ml-1 inline-flex items-center align-middle text-foreground hover:text-brand"
    @click.prevent="scrollToReference"
  >
    <ArrowUpRight
      class="size-3.5"
      :stroke-width="2.5"
    />
  </a>
</template>
