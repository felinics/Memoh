<template>
  <Dialog v-model:open="open">
    <DialogPanel @close-auto-focus="emit('closeAutoFocus', $event)">
      <DialogHeader>
        <DialogTitle>{{ t('chat.lifecycle.title') }}</DialogTitle>
        <!-- The session's time range belongs with the turn count, apart from
             the selected turn's own timestamp in the body. -->
        <DialogDescription>
          {{ turnCountLabel }}
          <span
            v-if="turns.length > 1"
            class="ml-3 tabular-nums"
          >{{ rangeLabel }}</span>
          <template v-if="data?.legacy_source">
            · {{ t('chat.lifecycle.legacySource') }}
          </template>
        </DialogDescription>
      </DialogHeader>

      <DialogBody>
        <div
          v-if="status === 'pending' && !data"
          class="flex flex-col gap-4"
        >
          <Skeleton class="h-24 w-full" />
          <Skeleton class="h-5 w-40" />
          <Skeleton class="h-16 w-full" />
        </div>

        <Empty v-else-if="status === 'error'">
          <EmptyDescription>{{ t('chat.lifecycle.loadFailed') }}</EmptyDescription>
        </Empty>

        <Empty v-else-if="!turns.length">
          <EmptyDescription>{{ t('chat.lifecycle.empty') }}</EmptyDescription>
        </Empty>

        <div
          v-else
          class="flex flex-col gap-6"
        >
          <!-- One column per turn, oldest first, scaled to the largest turn on
               the page: the session's growth reads left to right. Pick a
               column to inspect that turn below. -->
          <section
            v-if="turns.length > 1 || canLoadOlder"
            class="flex flex-col gap-2"
          >
            <div
              v-if="turns.length > 1"
              class="flex h-16 items-end gap-0.5"
            >
              <button
                v-for="(turn, index) in turns"
                :key="turn.key"
                type="button"
                class="flex h-full w-6 min-w-px shrink cursor-pointer flex-col-reverse overflow-hidden rounded-2xs bg-muted outline-none transition-opacity focus-visible:ring-2 focus-visible:ring-ring/30"
                :class="index === selectedIndex ? '' : 'opacity-50 hover:opacity-100'"
                :aria-label="`${turn.timeLabel} · ${turn.tokensLabel}`"
                :aria-pressed="index === selectedIndex"
                @click="selectedIndex = index"
              >
                <span
                  v-for="group in turn.groups"
                  :key="group.id"
                  class="w-full shrink-0"
                  :class="group.colorClass"
                  :style="{ height: `${(group.tokens / pageMaxTokens) * 100}%` }"
                />
              </button>
            </div>
            <Button
              v-if="canLoadOlder"
              variant="ghost"
              size="sm"
              class="self-start"
              @click="loadOlder"
            >
              {{ t('chat.lifecycle.loadOlder', { n: maxLimit }) }}
            </Button>
          </section>

          <section
            v-if="selected"
            class="flex flex-col gap-4"
          >
            <div class="flex flex-col gap-1">
              <div class="flex items-center gap-2 text-body text-muted-foreground">
                <span class="truncate">{{ selected.timeLabel }}</span>
                <span v-if="selected.model">·</span>
                <span
                  v-if="selected.model"
                  class="truncate"
                >{{ selected.model }}</span>
                <Badge
                  v-if="selected.statusLabel"
                  :variant="selected.status === 'fallback' ? 'warning' : 'destructive'"
                  class="ml-auto"
                >
                  {{ selected.statusLabel }}
                </Badge>
              </div>
              <div class="flex items-baseline gap-2">
                <span
                  v-if="selected.percent != null"
                  class="text-control font-medium tabular-nums"
                  :class="selected.percent >= 70 ? contextPressureToneClass(selected.percent, 'text') : ''"
                >{{ Math.round(selected.percent) }}%</span>
                <span class="text-body text-muted-foreground tabular-nums">{{ selected.tokensLabel }}</span>
              </div>
            </div>

            <ContextWaffle
              v-if="selected.percent != null"
              :percent="selected.percent"
              :groups="selected.groups"
              :columns="50"
            />

            <!-- A single legend line under the grid, read left to right like
                 the grid itself; the per-category split stays out of it. -->
            <ul
              v-if="selected.groups.length"
              class="flex flex-wrap gap-x-6 gap-y-1 text-body"
            >
              <li
                v-for="group in selected.groups"
                :key="group.id"
                class="flex items-center gap-2"
              >
                <span
                  class="size-2 shrink-0 rounded-2xs"
                  :class="group.colorClass"
                />
                <span class="text-muted-foreground">{{ t(`chat.contextGroup.${group.id}`) }}</span>
                <span class="text-foreground tabular-nums">{{ formatTokenCount(group.tokens) }}</span>
                <span class="text-muted-foreground tabular-nums">{{ shareOfUsed(selected, group.tokens) }}%</span>
              </li>
            </ul>

            <!-- What the model did not see this turn — the one thing the
                 composition alone cannot tell. -->
            <div
              v-if="selected.dropped > 0 || selected.trimmed > 0"
              class="flex flex-col gap-1 text-body"
            >
              <p
                v-if="selected.dropped > 0"
                class="text-foreground"
              >
                {{ t('chat.lifecycle.leftOut', { n: selected.dropped, tokens: formatTokenCount(selected.droppedTokens) }) }}
              </p>
              <p
                v-for="reason in selected.reasons"
                :key="reason.id"
                class="flex items-center gap-2 text-muted-foreground"
              >
                <span class="truncate">{{ reason.label }}</span>
                <span class="tabular-nums">{{ reason.count }}</span>
              </p>
              <p
                v-if="selected.trimmed > 0"
                class="text-muted-foreground"
              >
                {{ t('chat.lifecycle.trimmed', { n: selected.trimmed }) }}
              </p>
            </div>

            <p
              v-if="selected.memories > 0"
              class="text-body text-muted-foreground"
            >
              {{ t('chat.lifecycle.memoryRecalled', { n: selected.memories }) }}
            </p>
          </section>
        </div>
      </DialogBody>
    </DialogPanel>
  </Dialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Badge, Button, Dialog, DialogBody, DialogDescription, DialogHeader, DialogPanel, DialogTitle, Empty, EmptyDescription, Skeleton } from '@felinic/ui'
import type { HandlersContextLifecycleTurn } from '@memohai/sdk'
import { formatCalendarTime } from '@/utils/date-time'
import { useContextLifecycle } from '../composables/useContextLifecycle'
import { computeContextComposition, contextPressureToneClass, formatTokenCount } from '../composables/context-categories'
import { groupContextCategories, type ContextGroup } from '../composables/context-groups'
import ContextWaffle from './context-waffle.vue'

const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ closeAutoFocus: [event: Event] }>()

const { t } = useI18n()
const { data, status, canLoadOlder, loadOlder, maxLimit } = useContextLifecycle()

interface TurnView {
  key: string
  createdAt: string
  timeLabel: string
  model: string
  status: string
  statusLabel: string
  window: number | null
  tokens: number
  percent: number | null
  tokensLabel: string
  groups: ContextGroup[]
  dropped: number
  droppedTokens: number
  trimmed: number
  reasons: Array<{ id: string, label: string, count: number }>
  memories: number
}

const STATUS_KEY: Record<string, string> = {
  fallback: 'chat.lifecycle.statusFallback',
  failed_budget: 'chat.lifecycle.statusFailedBudget',
  failed_provider: 'chat.lifecycle.statusFailedProvider',
  aborted: 'chat.lifecycle.statusAborted',
}

const REASON_KEY: Record<string, string> = {
  history_budget: 'chat.lifecycle.reasonHistoryBudget',
  system_budget: 'chat.lifecycle.reasonSystemBudget',
  protected_context_overflow: 'chat.lifecycle.reasonProtectedOverflow',
  summary_coverage: 'chat.lifecycle.reasonSummaryCoverage',
  budget: 'chat.lifecycle.reasonBudget',
}

function toTurnView(turn: HandlersContextLifecycleTurn, index: number): TurnView {
  const snapshot = turn.snapshot
  const composition = computeContextComposition(snapshot)
  const window = snapshot?.budget_plan?.window && snapshot.budget_plan.window > 0 ? snapshot.budget_plan.window : null
  const tokens = composition?.totalTokens ?? snapshot?.counts?.token_estimate ?? 0
  const selection = snapshot?.selection
  const used = formatTokenCount(tokens)
  return {
    key: turn.run_id || turn.assistant_message_id || String(index),
    createdAt: turn.created_at ?? '',
    timeLabel: formatCalendarTime(turn.created_at),
    model: snapshot?.model ?? '',
    status: turn.status ?? '',
    statusLabel: STATUS_KEY[turn.status ?? ''] ? t(STATUS_KEY[turn.status ?? '']!) : '',
    window,
    tokens,
    percent: window != null ? (tokens / window) * 100 : null,
    tokensLabel: window != null
      ? t('chat.infoContextTokensEstimate', { used, window: formatTokenCount(window) })
      : t('chat.infoContextTokensEstimateNoWindow', { used }),
    groups: groupContextCategories(composition?.categories),
    dropped: selection?.dropped ?? 0,
    droppedTokens: Object.values(selection?.drop_reason_tokens ?? {}).reduce((sum, n) => sum + n, 0),
    trimmed: selection?.trimmed ?? 0,
    reasons: Object.entries(selection?.drop_reasons ?? {})
      .filter(([, count]) => count > 0)
      .map(([id, count]) => ({ id, count, label: REASON_KEY[id] ? t(REASON_KEY[id]!) : id })),
    memories: snapshot?.memory_recall?.result?.count ?? 0,
  }
}

const turns = computed(() => [...(data.value?.turns ?? [])]
  .sort((a, b) => (a.created_at ?? '').localeCompare(b.created_at ?? ''))
  .map(toTurnView))

function shareOfUsed(turn: TurnView, tokens: number) {
  return turn.tokens > 0 ? Math.round((tokens / turn.tokens) * 100) : 0
}

const pageMaxTokens = computed(() => Math.max(1, ...turns.value.map(turn => turn.tokens)))

const selectedIndex = ref(0)
// Follow the newest turn whenever the page changes (open, new turn, load older).
watch(() => turns.value.length, (length) => {
  selectedIndex.value = Math.max(0, length - 1)
}, { immediate: true })
const selected = computed(() => turns.value[selectedIndex.value])

// Across days only the days are named; within one day only the times. The
// exact time of any turn is shown with that turn below.
function dayLabel(date: Date) {
  const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
  const diff = Math.round((startOfDay(date) - startOfDay(new Date())) / 86_400_000)
  if (diff === 0 || diff === -1) {
    const day = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' }).format(diff, 'day')
    return `${day.charAt(0).toUpperCase()}${day.slice(1)}`
  }
  return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

const rangeLabel = computed(() => {
  const first = turns.value[0]?.createdAt
  const last = turns.value[turns.value.length - 1]?.createdAt
  if (!first || !last) return ''
  const from = new Date(first)
  const to = new Date(last)
  if (from.toDateString() === to.toDateString()) {
    const time = (d: Date) => d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })
    return `${time(from)} ~ ${time(to)}`
  }
  return `${dayLabel(from)} ~ ${dayLabel(to)}`
})

const turnCountLabel = computed(() => {
  const n = turns.value.length
  return n === 1 ? t('chat.lifecycle.turnCountOne') : t('chat.lifecycle.turnCount', { n })
})
</script>
