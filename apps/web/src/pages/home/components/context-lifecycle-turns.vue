<template>
  <Empty
    v-if="rows.length === 0"
    class="min-h-40"
  >
    <EmptyDescription>{{ $t('chat.lifecycle.empty') }}</EmptyDescription>
  </Empty>

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
        @click="toggle(row)"
      >
        <ChevronRight
          class="size-3.5 shrink-0 text-muted-foreground transition-transform"
          :class="{ 'rotate-90': openKeys.has(row.key) }"
        />
        <span class="shrink-0 text-muted-foreground tabular-nums">{{ row.time }}</span>
        <span class="min-w-0 flex-1 truncate text-muted-foreground">{{ row.model }}</span>
        <span
          v-if="row.diffKey"
          data-testid="turn-diff"
          class="shrink-0 rounded-sm bg-accent px-1.5 text-caption text-muted-foreground"
        >{{ $t(row.diffKey) }}</span>
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
            v-for="section in row.sections"
            :key="section.key"
            class="space-y-0.5"
          >
            <p :class="sectionLabelClass">
              {{ $t(section.titleKey) }}
            </p>
            <div
              v-for="entry in section.rows"
              :key="entry.key"
              class="flex items-center justify-between gap-2 py-0.5"
            >
              <span
                :data-testid="`${section.testId}-label`"
                class="min-w-0 flex-1 truncate"
                :class="entry.mono ? 'font-mono text-caption text-foreground' : 'text-muted-foreground'"
              >{{ entry.label }}</span>
              <span
                :data-testid="`${section.testId}-value`"
                class="min-w-0 truncate text-right text-foreground tabular-nums"
              >{{ entry.value }}</span>
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
import { Collapsible, CollapsibleContent, CollapsibleTrigger, Empty, EmptyDescription } from '@felinic/ui'
import { ChevronRight } from 'lucide-vue-next'
import type { HandlersContextLifecycleTurn } from '@memohai/sdk'
import { formatCalendarTime } from '@/utils/date-time'
import { buildTurnRow, type TurnRow } from '../composables/context-lifecycle-view'
import ContextUsageBreakdown from './context-usage-breakdown.vue'

const props = withDefaults(defineProps<{
  turns: HandlersContextLifecycleTurn[]
  hasOlder?: boolean
}>(), {
  hasOlder: false,
})

const { t, locale } = useI18n()

const sectionLabelClass = 'text-caption font-medium uppercase tracking-wider text-muted-foreground'

const rows = computed<TurnRow[]>(() => props.turns.map((turn, index) => {
  const older = props.turns[index + 1]?.snapshot
  return buildTurnRow(turn, {
    index,
    t,
    formatTime: iso => formatCalendarTime(iso, { locale: locale.value }),
    previous: older ?? (props.hasOlder ? undefined : null),
  })
}))

const openKeys = shallowRef<Set<string>>(new Set())

// The newest turn opens by default; rows the reader expanded stay open when a
// finished turn prepends a new row or older turns append.
watch(() => rows.value.map(row => row.key), (keys, previous) => {
  const next = new Set([...openKeys.value].filter(key => keys.includes(key)))
  if (keys[0] && keys[0] !== previous?.[0]) next.add(keys[0])
  openKeys.value = next
}, { immediate: true })

function toggle(row: TurnRow) {
  const next = new Set(openKeys.value)
  if (!next.delete(row.key)) next.add(row.key)
  openKeys.value = next
}
</script>
