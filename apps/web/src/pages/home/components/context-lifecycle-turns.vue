<template>
  <p
    v-if="rows.length === 0"
    class="py-10 text-center text-body text-muted-foreground"
  >
    {{ $t('chat.lifecycle.empty') }}
  </p>

  <div
    v-else
    class="space-y-1.5"
  >
    <Collapsible
      v-for="row in rows"
      :key="row.key"
      :open="openKeys.has(row.key)"
      class="rounded-md border border-border"
    >
      <CollapsibleTrigger
        data-testid="turn-row"
        class="flex w-full items-center gap-2 px-3 py-2 text-left text-body"
        @click="toggle(row.key)"
      >
        <ChevronRight
          class="size-3.5 shrink-0 text-muted-foreground transition-transform"
          :class="{ 'rotate-90': openKeys.has(row.key) }"
        />
        <span class="shrink-0 text-muted-foreground tabular-nums">{{ row.time }}</span>
        <span class="min-w-0 flex-1 truncate text-muted-foreground">{{ row.model }}</span>
        <span
          data-testid="turn-status"
          class="shrink-0"
          :class="row.statusTone"
        >{{ row.statusLabel }}</span>
        <span
          data-testid="turn-total"
          class="shrink-0 font-medium text-foreground tabular-nums"
        >{{ row.total }}</span>
      </CollapsibleTrigger>

      <CollapsibleContent>
        <div
          data-testid="turn-detail"
          class="space-y-3 border-t border-border px-3 py-2 text-body"
        >
          <div
            v-if="row.composition"
            class="space-y-1.5"
          >
            <p :class="sectionLabelClass">
              {{ $t('chat.lifecycle.composition') }}
            </p>
            <ContextUsageBreakdown
              :composition="row.composition"
              :context-window="row.contextWindow"
              :output-reserve="row.outputReserve"
              :auto-compact-tokens="null"
            />
          </div>

          <div
            v-if="row.selection || row.metrics.length"
            class="divide-y divide-border"
          >
            <div
              v-if="row.selection"
              class="flex items-center justify-between gap-2 py-1.5"
            >
              <span class="text-muted-foreground">{{ $t('chat.lifecycle.selection') }}</span>
              <span class="font-medium text-foreground tabular-nums">{{ row.selection }}</span>
            </div>
            <div
              v-for="metric in row.metrics"
              :key="metric.key"
              class="flex items-center justify-between gap-2 py-1.5"
            >
              <span
                data-testid="metric-label"
                class="text-muted-foreground"
              >{{ metric.label }}</span>
              <span
                data-testid="metric-value"
                class="font-medium text-foreground tabular-nums"
              >{{ metric.value }}</span>
            </div>
          </div>

          <div
            v-if="row.dropReasons.length"
            class="space-y-0.5"
          >
            <p :class="sectionLabelClass">
              {{ $t('chat.lifecycle.dropReasons') }}
            </p>
            <div
              v-for="reason in row.dropReasons"
              :key="reason.key"
              class="flex items-center justify-between gap-2 py-0.5"
            >
              <span
                data-testid="drop-reason"
                class="min-w-0 flex-1 truncate text-muted-foreground"
              >{{ reason.label }}</span>
              <span
                data-testid="drop-reason-value"
                class="shrink-0 text-foreground tabular-nums"
              >{{ reason.value }}</span>
            </div>
          </div>

          <div
            v-if="row.trust.length"
            class="space-y-0.5"
          >
            <p :class="sectionLabelClass">
              {{ $t('chat.lifecycle.trust') }}
            </p>
            <div
              v-for="entry in row.trust"
              :key="entry.key"
              class="flex items-center justify-between gap-2 py-0.5"
            >
              <span
                data-testid="trust-label"
                class="min-w-0 flex-1 truncate text-muted-foreground"
              >{{ entry.label }}</span>
              <span class="shrink-0 text-foreground tabular-nums">{{ entry.value }}</span>
            </div>
          </div>

          <div
            v-if="row.mutations.length"
            class="space-y-0.5"
          >
            <p :class="sectionLabelClass">
              {{ $t('chat.lifecycle.mutations') }}
            </p>
            <div
              v-for="mutation in row.mutations"
              :key="mutation.key"
              class="flex items-center gap-2 py-0.5"
            >
              <span
                data-testid="mutation-kind"
                class="shrink-0 font-mono text-caption text-foreground"
              >{{ mutation.kind }}</span>
              <span
                data-testid="mutation-detail"
                class="min-w-0 flex-1 truncate text-muted-foreground"
              >{{ mutation.detail }}</span>
            </div>
          </div>

          <div
            v-if="row.steps.length"
            class="space-y-0.5"
          >
            <p :class="sectionLabelClass">
              {{ $t('chat.lifecycle.steps') }}
            </p>
            <div
              v-for="step in row.steps"
              :key="step.key"
              class="flex items-center justify-between gap-2 py-0.5"
            >
              <span
                data-testid="step-label"
                class="shrink-0 font-mono text-caption text-muted-foreground"
              >{{ step.label }}</span>
              <span
                data-testid="step-value"
                class="min-w-0 truncate text-right text-muted-foreground tabular-nums"
              >{{ step.value }}</span>
            </div>
          </div>
        </div>
      </CollapsibleContent>
    </Collapsible>
  </div>
</template>

<script setup lang="ts">
import { computed, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@felinic/ui'
import { ChevronRight } from 'lucide-vue-next'
import type { ContextfragLifecycleSnapshot, HandlersContextLifecycleTurn } from '@memohai/sdk'
import { formatCalendarTime } from '@/utils/date-time'
import { formatTokenCount } from '../composables/context-categories'
import type { ContextComposition } from '../composables/context-categories'
import {
  compositionFromSnapshot,
  groupDroppedDecisions,
  lifecycleStatusLabelKey,
  lifecycleStatusToneClass,
} from '../composables/context-lifecycle-view'
import ContextUsageBreakdown from './context-usage-breakdown.vue'

const props = defineProps<{
  turns: HandlersContextLifecycleTurn[]
}>()

const { t, locale } = useI18n()

const sectionLabelClass = 'text-caption font-medium uppercase tracking-wider text-muted-foreground'

const TRUST_LABEL_KEY: Record<string, string> = {
  system: 'chat.lifecycle.trustSystem',
  workspace: 'chat.lifecycle.trustWorkspace',
  user: 'chat.lifecycle.trustUser',
  external: 'chat.lifecycle.trustExternal',
}

interface LabeledValue {
  key: string
  label: string
  value: string
}

interface TurnRow {
  key: string
  time: string
  model: string
  statusLabel: string
  statusTone: string
  total: string
  composition: ContextComposition | null
  contextWindow: number | null
  outputReserve: number | null
  selection: string
  dropReasons: LabeledValue[]
  trust: LabeledValue[]
  metrics: LabeledValue[]
  mutations: { key: string, kind: string, detail: string }[]
  steps: LabeledValue[]
}

function positive(value: number | undefined): number | null {
  return value != null && value > 0 ? value : null
}

function selectionText(snapshot: ContextfragLifecycleSnapshot): string {
  const selection = snapshot.selection
  if (!selection) return ''
  return [
    t('chat.lifecycle.selectedCount', { n: selection.selected ?? 0 }),
    t('chat.lifecycle.droppedCount', { n: selection.dropped ?? 0 }),
  ].join(' · ')
}

function dropReasonRows(snapshot: ContextfragLifecycleSnapshot): LabeledValue[] {
  return groupDroppedDecisions(snapshot.selection_decisions).map(group => ({
    key: group.reason,
    label: group.reason,
    value: `${group.count} · ${formatTokenCount(group.tokens)}`,
  }))
}

function trustRows(snapshot: ContextfragLifecycleSnapshot): LabeledValue[] {
  return (snapshot.trust_breakdown ?? []).map((entry, index) => {
    const trust = entry.trust ?? ''
    const labelKey = TRUST_LABEL_KEY[trust]
    return {
      key: trust || `trust-${index}`,
      label: labelKey ? t(labelKey) : trust || 'unknown',
      value: formatTokenCount(entry.token_estimate ?? 0),
    }
  })
}

function metricRows(snapshot: ContextfragLifecycleSnapshot): LabeledValue[] {
  const entries: [string, string, number | undefined][] = [
    ['window', 'chat.lifecycle.window', snapshot.budget_plan?.window],
    ['stablePrefix', 'chat.lifecycle.stablePrefix', snapshot.stable_prefix_token_estimate],
    ['cacheRead', 'chat.lifecycle.cacheRead', snapshot.cache_read_tokens],
    ['cacheWrite', 'chat.lifecycle.cacheWrite', snapshot.cache_write_tokens],
  ]
  return entries.flatMap(([key, labelKey, value]) => {
    const tokens = positive(value)
    return tokens == null ? [] : [{ key, label: t(labelKey), value: formatTokenCount(tokens) }]
  })
}

function mutationRows(snapshot: ContextfragLifecycleSnapshot): TurnRow['mutations'] {
  return (snapshot.mutations ?? []).flatMap((mutation, index) => {
    const kind = mutation.kind?.trim() ?? ''
    if (!kind) return []
    return [{ key: `${kind}-${index}`, kind, detail: mutation.detail?.trim() ?? '' }]
  })
}

// A lone clean step is the normal shape and says nothing; the block only earns
// its space once the loop re-ran or a step lost fragments.
function stepRows(snapshot: ContextfragLifecycleSnapshot): LabeledValue[] {
  const steps = snapshot.steps ?? []
  const noteworthy = steps.length > 1
    || steps.some(step => (step.dropped ?? 0) > 0 || (step.truncated ?? 0) > 0 || step.reselection_applied === true)
  if (!noteworthy) return []

  return steps.map((step, index) => ({
    key: `${step.step_index ?? index}-${step.attempt ?? 0}`,
    label: `#${step.step_index ?? index}`,
    value: [
      (step.dropped ?? 0) > 0 ? t('chat.lifecycle.droppedCount', { n: step.dropped }) : '',
      (step.truncated ?? 0) > 0 ? String(step.truncated) : '',
      step.reselection_outcome?.trim() ?? '',
    ].filter(Boolean).join(' · '),
  }))
}

const rows = computed<TurnRow[]>(() => props.turns.map((turn, index) => {
  const snapshot = turn.snapshot ?? {}
  const composition = compositionFromSnapshot(snapshot)
  const statusKey = lifecycleStatusLabelKey(turn.status)
  return {
    key: turn.run_id || turn.assistant_message_id || `turn-${index}`,
    time: formatCalendarTime(turn.created_at, { locale: locale.value }),
    model: snapshot.model ?? '',
    statusLabel: statusKey ? t(statusKey) : turn.status ?? '',
    statusTone: lifecycleStatusToneClass(turn.status),
    total: formatTokenCount(composition?.totalTokens ?? snapshot.counts?.token_estimate ?? 0),
    composition,
    contextWindow: positive(snapshot.budget_plan?.window),
    outputReserve: positive(snapshot.budget_plan?.output_reserve),
    selection: selectionText(snapshot),
    dropReasons: dropReasonRows(snapshot),
    trust: trustRows(snapshot),
    metrics: metricRows(snapshot),
    mutations: mutationRows(snapshot),
    steps: stepRows(snapshot),
  }
}))

const openKeys = shallowRef<Set<string>>(new Set())

watch(() => rows.value[0]?.key, (key) => {
  openKeys.value = key ? new Set([key]) : new Set()
}, { immediate: true })

function toggle(key: string) {
  const next = new Set(openKeys.value)
  if (!next.delete(key)) next.add(key)
  openKeys.value = next
}
</script>
