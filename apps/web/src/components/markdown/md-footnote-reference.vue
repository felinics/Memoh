<script setup lang="ts">
import { ArrowUpRight } from 'lucide-vue-next'

// In-body footnote marker (`[^1]`). Replaces markstream's bracketed "[1]" with
// a link-language marker: the number on a dotted underline plus a small up-right
// arrow. Solid foreground (black) at rest — it's real content, not a faint gray
// — tinting to the scarce brand purple on hover. The whole marker (number +
// arrow) is the click target. The id wiring mirrors markstream exactly so the
// existing scroll works: the marker is `#fnref-{id}` and it jumps to the
// definition at `#fnref--{id}`.
const props = defineProps<{
  node: { type: 'footnote_reference', id: string, raw: string }
}>()

function scrollToDefinition() {
  document.querySelector(`#fnref--${props.node.id}`)?.scrollIntoView({ behavior: 'smooth' })
}
</script>

<template>
  <sup
    :id="`fnref-${node.id}`"
    class="mx-px text-[0.72em] leading-none"
  >
    <a
      :href="`#fnref--${node.id}`"
      class="group/fnref inline-flex items-center gap-px align-middle text-foreground hover:text-brand"
      @click.prevent="scrollToDefinition"
    >
      <span class="underline decoration-dotted decoration-1 decoration-foreground underline-offset-2 group-hover/fnref:decoration-brand">{{ node.id }}</span>
      <ArrowUpRight
        class="size-[1.05em]"
        :stroke-width="2.5"
      />
    </a>
  </sup>
</template>
