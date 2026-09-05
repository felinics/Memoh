<template>
  <div class="space-y-1.5 text-body">
    <div class="relative flex h-1.5 w-full overflow-hidden rounded-full bg-accent">
      <div
        v-for="category in composition.categories"
        :key="category.id"
        class="h-full"
        :class="category.colorClass"
        :style="{ width: segmentWidth(category.tokens) }"
      />
      <template v-if="showReserve">
        <div class="h-full flex-1" />
        <div
          class="h-full bg-border"
          :style="{ width: segmentWidth(reserveTokens) }"
        />
      </template>
      <div
        v-if="autoMarkLeft"
        class="absolute inset-y-0 w-px bg-muted-foreground"
        :style="{ left: autoMarkLeft }"
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
    <p
      v-if="autoMarkLeft && autoCompactTokens != null"
      class="text-caption text-muted-foreground"
    >
      {{ $t('chat.infoAutoCompactAt', { tokens: formatTokenCount(autoCompactTokens) }) }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatTokenCount } from '../composables/context-categories'
import type { ContextCategoryId, ContextComposition } from '../composables/context-categories'

const props = withDefaults(defineProps<{
  composition: ContextComposition
  contextWindow: number | null
  outputReserve?: number | null
  autoCompactTokens?: number | null
}>(), {
  outputReserve: null,
  autoCompactTokens: null,
})

interface LegendRow {
  id: ContextCategoryId | 'reserve' | 'free'
  colorClass: string
  tokens: number
  muted: boolean
}

const showReserve = computed(() => props.contextWindow != null && props.outputReserve != null)
const reserveTokens = computed(() => (props.contextWindow == null ? 0 : props.outputReserve ?? 0))
const denominator = computed(() => Math.max(props.contextWindow ?? 0, props.composition.totalTokens + reserveTokens.value))

function segmentWidth(tokens: number): string {
  return denominator.value > 0 ? `${(tokens / denominator.value) * 100}%` : '0%'
}

// The trigger's measured quantity differs by turn path (provider input on the
// pipeline path, history estimate otherwise), so the mark states the threshold
// against the window and makes no claim about which segment reaches it.
const autoMarkLeft = computed(() => {
  if (props.contextWindow == null || props.autoCompactTokens == null || denominator.value <= 0) return null
  const ratio = props.autoCompactTokens / denominator.value
  return ratio >= 1 ? 'calc(100% - 1px)' : `${ratio * 100}%`
})

const legendRows = computed<LegendRow[]>(() => {
  const rows: LegendRow[] = props.composition.categories.map(category => ({
    id: category.id,
    colorClass: category.colorClass,
    tokens: category.tokens,
    muted: false,
  }))
  if (props.contextWindow == null) return rows
  if (showReserve.value) {
    rows.push({
      id: 'reserve',
      colorClass: 'bg-border',
      tokens: reserveTokens.value,
      muted: true,
    })
  }
  rows.push({
    id: 'free',
    colorClass: 'bg-accent',
    tokens: Math.max(0, props.contextWindow - props.composition.totalTokens - reserveTokens.value),
    muted: true,
  })
  return rows
})
</script>
