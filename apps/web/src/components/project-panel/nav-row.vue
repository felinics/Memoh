<template>
  <div
    :class="rowClass"
    :style="{ paddingLeft: `${0.5 + depth * 0.875}rem` }"
  >
    <button
      type="button"
      class="flex min-w-0 flex-1 cursor-pointer items-center gap-1.5 py-1 text-left"
      @click="emit('activate')"
    >
      <!-- ONE icon column for every row in the panel. It is a fixed size-4 box
           regardless of the glyph inside, which is what keeps Issues / Wiki /
           doc rows on the same vertical line — three hand-written rows had
           drifted into two different icon widths before this became a
           component. -->
      <span
        class="flex size-4 shrink-0 items-center justify-center text-muted-foreground"
        @click.stop="onIconClick"
      >
        <template v-if="expandable">
          <span class="flex group-hover/row:hidden"><slot name="icon" /></span>
          <ChevronRight
            class="hidden size-3.5 transition-transform duration-150 group-hover/row:block"
            :class="expanded ? 'rotate-90' : ''"
          />
        </template>
        <slot
          v-else
          name="icon"
        />
      </span>
      <span class="min-w-0 flex-1 truncate">{{ label }}</span>
    </button>
    <slot name="actions" />
  </div>
</template>

<script setup lang="ts">
import { ChevronRight } from 'lucide-vue-next'

// The one row shape of the Projects panel: fixed icon column, truncating
// label, hover-revealed trailing actions. On an expandable row the resting
// glyph swaps to a chevron while the row is hovered, so the tree needs no
// permanent second icon column.
withDefaults(defineProps<{
  label: string
  /** Indent level; 0 = a project's own rows (Issues / Wiki). */
  depth?: number
  expandable?: boolean
  expanded?: boolean
}>(), {
  depth: 0,
  expandable: false,
  expanded: false,
})

const emit = defineEmits<{
  activate: []
  toggle: []
}>()

// Hand-rolled nav row (no tree primitive in @felinic/ui): the hover fill is
// the row's own chrome, same family as the sidebar session rows.
const rowClass = 'group/row flex w-full min-w-0 items-center gap-0.5 rounded-md pr-1 text-label text-foreground hover:bg-[color:var(--sidebar-hover)]' /* ui-allow-style */

function onIconClick() {
  emit('toggle')
}
</script>
