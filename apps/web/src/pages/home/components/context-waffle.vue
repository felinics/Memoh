<template>
  <!-- One cell per percent of the window, so headroom reads as a count of
       empty cells rather than a number to interpret. -->
  <div
    class="grid gap-0.5"
    :class="COLUMN_CLASS[columns]"
    aria-hidden="true"
  >
    <span
      v-for="(cellClass, index) in cells"
      :key="index"
      class="aspect-square rounded-2xs"
      :class="cellClass"
    />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { contextPressureToneClass } from '../composables/context-categories'
import type { ContextGroup } from '../composables/context-groups'

const props = defineProps<{
  percent: number
  groups: ContextGroup[]
  // 20 → 5 rows for the popover; 50 → a 2-row strip for wide surfaces.
  columns: 20 | 50
}>()

// Literal classes so the Tailwind scanner can see them.
const COLUMN_CLASS = { 20: 'grid-cols-20', 50: 'grid-cols-50' } as const

const TOTAL_CELLS = 100

// Split the used cells across groups by largest remainder, giving every
// present group at least one cell so a small share never disappears.
const cells = computed(() => {
  const raw = Math.min(TOTAL_CELLS, Math.max(0, props.percent))
  const used = raw > 0 ? Math.max(1, Math.round(raw)) : 0
  const result: string[] = []
  const present = props.groups
  const total = present.reduce((sum, group) => sum + group.tokens, 0)

  if (!present.length || total <= 0 || used < present.length) {
    const fill = contextPressureToneClass(props.percent, 'bg')
    for (let i = 0; i < used; i++) result.push(fill)
  }
  else {
    const spare = used - present.length
    const shares = present.map(group => (group.tokens / total) * spare)
    const counts = shares.map(share => 1 + Math.floor(share))
    let left = used - counts.reduce((sum, n) => sum + n, 0)
    const order = shares
      .map((share, index) => ({ index, rest: share - Math.floor(share) }))
      .sort((a, b) => b.rest - a.rest)
    for (const { index } of order) {
      if (left <= 0) break
      counts[index]!++
      left--
    }
    present.forEach((group, index) => {
      for (let i = 0; i < counts[index]!; i++) result.push(group.colorClass)
    })
  }

  while (result.length < TOTAL_CELLS) result.push('bg-muted')
  return result
})
</script>
