<template>
  <div class="space-y-1.5 text-body">
    <div class="flex h-1.5 w-full overflow-hidden rounded-full bg-accent">
      <div
        v-for="category in categories"
        :key="category.id"
        class="h-full"
        :class="category.colorClass"
        :style="{ width: segmentWidth(category.tokens) }"
      />
    </div>
    <div>
      <div
        v-for="row in legendRows"
        :key="row.id"
        class="flex items-center gap-1.5 py-1"
      >
        <span
          class="size-2 shrink-0 rounded-full"
          :class="row.colorClass"
        />
        <span class="min-w-0 flex-1 truncate text-muted-foreground">{{ $t(`chat.contextBreakdown.${row.id}`) }}</span>
        <span
          class="font-medium tabular-nums"
          :class="row.muted ? 'text-muted-foreground' : 'text-foreground'"
        >{{ formatTokenCount(row.tokens) }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatTokenCount } from '../composables/context-categories'
import type { ContextCategoryId, ContextCategoryStat } from '../composables/context-categories'

const props = defineProps<{
  categories: ContextCategoryStat[]
  totalTokens: number
  contextWindow: number | null
}>()

interface LegendRow {
  id: ContextCategoryId | 'free'
  colorClass: string
  tokens: number
  muted: boolean
}

const denominator = computed(() => Math.max(props.contextWindow ?? 0, props.totalTokens))

function segmentWidth(tokens: number): string {
  return denominator.value > 0 ? `${(tokens / denominator.value) * 100}%` : '0%'
}

const legendRows = computed<LegendRow[]>(() => {
  const rows: LegendRow[] = props.categories.map(category => ({
    id: category.id,
    colorClass: category.colorClass,
    tokens: category.tokens,
    muted: false,
  }))
  if (props.contextWindow != null) {
    rows.push({
      id: 'free',
      colorClass: 'bg-accent',
      tokens: Math.max(0, props.contextWindow - props.totalTokens),
      muted: true,
    })
  }
  return rows
})
</script>
