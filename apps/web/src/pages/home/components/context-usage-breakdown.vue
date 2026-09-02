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
      <div
        v-if="hardMarkLeft"
        class="absolute inset-y-0 w-px bg-destructive"
        :style="{ left: hardMarkLeft }"
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
      v-if="hardMarkLeft && hardCompactTokens != null"
      class="text-caption text-muted-foreground"
    >
      {{ $t('chat.infoHardCompactAt', { tokens: formatTokenCount(hardCompactTokens) }) }}
    </p>
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
import { CONTEXT_CATEGORY_IDS, formatTokenCount } from '../composables/context-categories'
import type { ContextCategoryId, ContextComposition } from '../composables/context-categories'

const props = withDefaults(defineProps<{
  composition: ContextComposition
  contextWindow: number | null
  outputReserve?: number | null
  autoCompactTokens?: number | null
  hardCompactTokens?: number | null
}>(), {
  outputReserve: null,
  autoCompactTokens: null,
  hardCompactTokens: null,
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

// The compaction trigger measures the conversation alone, so both marks are
// anchored where the conversation segment starts, not at the track origin.
const BEFORE_CONVERSATION = new Set<ContextCategoryId>(CONTEXT_CATEGORY_IDS.slice(0, CONTEXT_CATEGORY_IDS.indexOf('conversation')))
const conversationStart = computed(() => props.composition.categories
  .filter(category => BEFORE_CONVERSATION.has(category.id))
  .reduce((sum, category) => sum + category.tokens, 0))

function markLeft(tokens: number | null): string | null {
  if (props.contextWindow == null || tokens == null || denominator.value <= 0) return null
  return `${Math.min((conversationStart.value + tokens) / denominator.value, 1) * 100}%`
}

const autoMarkLeft = computed(() => markLeft(props.autoCompactTokens))
const hardMarkLeft = computed(() => markLeft(props.hardCompactTokens))

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
