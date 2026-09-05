<template>
  <ScrollArea class="h-full">
    <div class="space-y-3 px-3 py-2 text-body">
      <div class="flex items-center justify-between gap-2">
        <span
          class="text-caption font-medium"
          :class="KIND_TONE_CLASS[row.kind]"
        >
          {{ $t(KIND_LABEL_KEY[row.kind]) }}
          <template v-if="row.stepIndex != null"> · {{ $t('chat.trajectory.step', { n: row.stepIndex }) }}</template>
        </span>
        <Button
          variant="ghost"
          size="icon-sm"
          :aria-label="$t('chat.trajectory.closeInspector')"
          @click="emit('close')"
        >
          <X />
        </Button>
      </div>

      <template v-if="row.detail.kind === 'system'">
        <ContextLifecycleTurns
          :turns="systemTurns"
          :has-older="row.detail.previous === undefined"
          :show-count="1"
        />
        <p class="text-caption text-muted-foreground">
          {{ $t('chat.trajectory.inspectorPromptChanges') }}
        </p>
        <p
          v-if="row.detail.previous === null"
          class="text-caption text-muted-foreground"
          data-testid="trajectory-inspector-prompt-first"
        >
          {{ $t('chat.trajectory.inspectorPromptFirstRun') }}
        </p>
        <p
          v-else-if="row.detail.previous === undefined"
          class="text-caption text-muted-foreground"
        >
          {{ $t('chat.trajectory.inspectorPromptPreviousUnknown') }}
        </p>
        <p
          v-else-if="promptChanges.length === 0"
          class="text-caption text-muted-foreground"
          data-testid="trajectory-inspector-prompt-same"
        >
          {{ $t('chat.trajectory.inspectorPromptSame') }}
        </p>
        <div
          v-else
          class="divide-y divide-border"
          data-testid="trajectory-inspector-prompt-changes"
        >
          <div
            v-for="change in promptChanges"
            :key="change.key"
            class="space-y-1 py-1"
          >
            <div class="flex items-center gap-2 text-caption">
              <span
                class="w-14 shrink-0 font-medium"
                :class="PROMPT_CHANGE_TONE[change.change]"
              >{{ $t(`chat.trajectory.promptChange.${change.change}`) }}</span>
              <span class="min-w-0 flex-1 truncate font-mono text-foreground">{{ change.label }}</span>
            </div>
            <template v-if="change.change === 'changed'">
              <pre
                v-if="diffFor(change)"
                class="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md bg-accent p-2 font-mono text-caption"
                data-testid="trajectory-inspector-diff"
              ><template
                v-for="(line, index) in diffFor(change)"
                :key="index"
              ><span
                v-if="line.type === 'skip'"
                class="text-muted-foreground"
              >… {{ $t('chat.trajectory.inspectorDiffSkipped', { n: line.count }) }}
</span><span
                v-else
                :class="DIFF_LINE_CLASS[line.type]"
              >{{ DIFF_PREFIX[line.type] }}{{ line.text }}
</span></template></pre>
              <Skeleton
                v-else-if="diffStatus === 'pending' || fragmentStatus === 'pending'"
                class="h-4 w-full"
              />
              <p
                v-else
                class="text-caption text-muted-foreground"
              >
                {{ $t('chat.trajectory.inspectorDiffUnavailable') }}
              </p>
            </template>
          </div>
        </div>
      </template>

      <template v-else-if="row.detail.kind === 'context'">
        <div
          v-if="detailRows.length"
          class="divide-y divide-border"
        >
          <div
            v-for="entry in detailRows"
            :key="entry.key"
            class="flex items-center justify-between gap-3 py-1"
          >
            <span class="text-muted-foreground">{{ entry.label }}</span>
            <span
              class="min-w-0 truncate text-right font-medium tabular-nums text-foreground"
              :class="entry.mono ? 'font-mono' : ''"
            >{{ entry.value }}</span>
          </div>
        </div>
        <template v-if="listRows.length">
          <p class="text-caption text-muted-foreground">
            {{ $t(listTitleKey) }}
          </p>
          <div
            class="divide-y divide-border"
            data-testid="trajectory-inspector-list"
          >
            <div
              v-for="entry in listRows"
              :key="entry.key"
              class="flex items-center justify-between gap-3 py-0.5"
            >
              <span class="min-w-0 flex-1 truncate font-mono text-caption text-foreground">{{ entry.label }}</span>
              <span class="shrink-0 text-right text-caption tabular-nums text-muted-foreground">{{ entry.value }}</span>
            </div>
          </div>
        </template>
      </template>

      <template v-else-if="row.detail.kind === 'compaction'">
        <div
          class="divide-y divide-border"
          data-testid="trajectory-inspector-compaction"
        >
          <div
            v-for="entry in compactionRows"
            :key="entry.key"
            class="flex items-center justify-between gap-3 py-1"
          >
            <span class="text-muted-foreground">{{ entry.label }}</span>
            <span class="min-w-0 truncate text-right font-medium tabular-nums text-foreground">{{ entry.value }}</span>
          </div>
        </div>
        <p class="text-caption text-muted-foreground">
          {{ $t('chat.trajectory.inspectorSummary') }}
        </p>
        <pre
          class="whitespace-pre-wrap break-words rounded-md bg-accent p-2 font-mono text-body text-foreground"
          data-testid="trajectory-inspector-summary"
        >{{ row.detail.compaction.summary }}</pre>
      </template>

      <template v-else-if="row.detail.kind === 'user'">
        <pre
          class="whitespace-pre-wrap break-words font-mono text-body text-foreground"
          data-testid="trajectory-inspector-text"
        >{{ row.detail.turn.text }}</pre>
      </template>

      <template v-else>
        <div
          v-if="timingRows.length"
          class="divide-y divide-border"
        >
          <div
            v-for="entry in timingRows"
            :key="entry.key"
            class="flex items-center justify-between gap-3 py-1"
          >
            <span class="text-muted-foreground">{{ entry.label }}</span>
            <span class="font-medium tabular-nums text-foreground">{{ entry.value }}</span>
          </div>
        </div>
        <p
          v-else-if="row.kind !== 'tool'"
          class="text-caption text-muted-foreground"
        >
          {{ $t('chat.trajectory.usageNotReported') }}
        </p>

        <template v-if="row.detail.block.type === 'tool'">
          <p class="text-caption text-muted-foreground">
            {{ $t('chat.trajectory.inspectorInput') }}
          </p>
          <pre
            class="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md bg-accent p-2 font-mono text-body"
            data-testid="trajectory-inspector-input"
          >{{ pretty(row.detail.block.input) }}</pre>
          <p class="text-caption text-muted-foreground">
            {{ $t('chat.trajectory.inspectorOutput') }}
          </p>
          <pre
            class="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md bg-accent p-2 font-mono text-body"
            data-testid="trajectory-inspector-output"
          >{{ pretty(row.detail.block.result ?? row.detail.block.output) }}</pre>
        </template>
        <pre
          v-else-if="'content' in row.detail.block"
          class="whitespace-pre-wrap break-words font-mono text-body text-foreground"
          data-testid="trajectory-inspector-text"
        >{{ row.detail.block.content }}</pre>
      </template>

      <template v-if="textRefs.length">
        <p class="text-caption text-muted-foreground">
          {{ $t('chat.trajectory.inspectorTexts') }}
        </p>
        <div
          v-if="fragmentStatus === 'pending'"
          class="space-y-1"
        >
          <Skeleton
            v-for="line in 3"
            :key="line"
            class="h-4 w-full"
          />
        </div>
        <p
          v-else-if="textsForbidden"
          class="text-caption text-muted-foreground"
        >
          {{ $t('chat.trajectory.inspectorTextsForbidden') }}
        </p>
        <p
          v-else-if="fragmentStatus === 'error'"
          class="text-caption text-destructive"
        >
          {{ $t('chat.trajectory.inspectorTextsFailed') }}
        </p>
        <div
          v-else
          class="space-y-2"
          data-testid="trajectory-inspector-texts"
        >
          <div
            v-for="entry in textRows"
            :key="entry.key"
            class="space-y-1"
          >
            <div class="flex items-center justify-between gap-2 text-caption">
              <span class="min-w-0 truncate font-mono text-foreground">{{ entry.id }}</span>
              <span class="shrink-0 tabular-nums text-muted-foreground">
                {{ entry.tokens }}<template v-if="entry.truncated"> · {{ $t('chat.trajectory.inspectorTruncated') }}</template>
              </span>
            </div>
            <pre
              v-if="entry.available"
              class="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md bg-accent p-2 font-mono text-body"
            >{{ entry.text }}</pre>
            <p
              v-else
              class="text-caption text-muted-foreground"
            >
              {{ $t('chat.trajectory.inspectorTextUnavailable') }}
            </p>
          </div>
        </div>
      </template>

      <template v-if="decisionScope">
        <p class="text-caption text-muted-foreground">
          {{ $t('chat.trajectory.inspectorDecisions') }}
        </p>
        <div
          v-if="decisionStatus === 'pending'"
          class="space-y-1"
        >
          <Skeleton
            v-for="line in 3"
            :key="line"
            class="h-4 w-full"
          />
        </div>
        <p
          v-else-if="decisionStatus === 'error'"
          class="text-caption text-destructive"
        >
          {{ $t('chat.trajectory.inspectorDecisionsFailed') }}
        </p>
        <p
          v-else-if="decisionRows.length === 0"
          class="text-caption text-muted-foreground"
        >
          {{ $t('chat.trajectory.inspectorDecisionsEmpty') }}
        </p>
        <div
          v-else
          class="divide-y divide-border"
          data-testid="trajectory-inspector-decisions"
        >
          <div
            v-for="entry in decisionRows"
            :key="entry.key"
            class="flex items-center gap-2 py-0.5 text-caption"
          >
            <span
              class="w-14 shrink-0 font-medium"
              :class="entry.tone"
            >{{ entry.decision }}</span>
            <span class="min-w-0 flex-1 truncate font-mono text-foreground">{{ entry.label }}</span>
            <span class="shrink-0 truncate text-muted-foreground">{{ entry.reason }}</span>
            <span class="shrink-0 tabular-nums text-muted-foreground">{{ entry.tokens }}</span>
          </div>
          <p
            v-if="hiddenDecisions > 0"
            class="py-0.5 text-caption text-muted-foreground"
          >
            {{ $t('chat.trajectory.inspectorDecisionsMore', { n: hiddenDecisions }) }}
          </p>
        </div>
      </template>
    </div>
  </ScrollArea>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { X } from 'lucide-vue-next'
import { Button, ScrollArea, Skeleton } from '@felinic/ui'
import type { ContextfragSelectionDecision } from '@memohai/sdk'
import { entryRefs, type TrajectoryRow } from '../../composables/trajectory-model'
import { compactionDetailRows, contextDetailRows, contextListRows, decisionScopeOf, formatDurationMs, KIND_LABEL_KEY, KIND_TONE_CLASS, lineDiff, promptFragmentChanges, type DecisionScope, type DiffLine, type FragmentPreviews, type PromptChange, type PromptChangeKind } from '../../composables/trajectory-view'
import { formatTokenCount } from '../../composables/context-categories'
import { useContextLifecycleDecisions } from '../../composables/useContextLifecycleDecisions'
import { useContextLifecycleFragments } from '../../composables/useContextLifecycleFragments'
import { apiErrorStatus } from '@/utils/api-error'
import ContextLifecycleTurns from '../context-lifecycle-turns.vue'

const DECISION_ROW_LIMIT = 200

const props = defineProps<{ row: TrajectoryRow, previews?: FragmentPreviews | null }>()
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()

function clock(ms: number): string {
  return new Date(ms).toLocaleTimeString(undefined, { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3 })
}

function pretty(value: unknown): string {
  if (value == null) return ''
  if (typeof value === 'string') return value
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

const detailRows = computed(() => (props.row.detail.kind === 'context' ? contextDetailRows(props.row.detail.entry, t) : []))
const compactionRows = computed(() => (props.row.detail.kind === 'compaction' ? compactionDetailRows(props.row.detail.compaction, t, clock) : []))
const listRows = computed(() => (props.row.detail.kind === 'context' ? contextListRows(props.row.detail.entry, props.row.detail.lifecycle.snapshot, t) : []))
const listTitleKey = computed(() => {
  const detail = props.row.detail
  if (detail.kind !== 'context') return ''
  switch (detail.entry.kind) {
    case 'tool_defs':
      return 'chat.trajectory.inspectorTools'
    case 'fragments':
    case 'memory_recall':
      return 'chat.trajectory.inspectorRefs'
    default:
      return 'chat.trajectory.inspectorDropReasons'
  }
})

// Rows that stand for injected fragments show the texts the store kept for
// them, in the run's own order.
const textRefs = computed(() => {
  const detail = props.row.detail
  return detail.kind === 'system' || detail.kind === 'context' ? entryRefs(detail.entry) : []
})
const textRunId = computed(() => {
  const detail = props.row.detail
  if (textRefs.value.length === 0 || (detail.kind !== 'system' && detail.kind !== 'context')) return null
  return detail.lifecycle.run_id ?? null
})
const { fragments, status: fragmentStatus, error: fragmentError } = useContextLifecycleFragments(textRunId)
// The texts carry workspace files, so a reader without workspace access is
// told so instead of seeing a load failure.
const textsForbidden = computed(() => fragmentStatus.value === 'error' && apiErrorStatus(fragmentError.value) === 403)
const textRows = computed(() => {
  const byHash = new Map(fragments.value.map(fragment => [fragment.text_hash ?? '', fragment]))
  return textRefs.value.map((ref, index) => {
    const stored = ref.textHash ? byHash.get(ref.textHash) : undefined
    return {
      key: `${ref.textHash || ref.id}/${index}`,
      id: stored?.label || ref.id || t(`chat.trajectory.contextKind.${ref.kind}`),
      tokens: ref.tokens ? formatTokenCount(ref.tokens) : '',
      text: stored?.text ?? '',
      truncated: stored?.truncated === true,
      available: stored?.available === true,
    }
  })
})

// A system row compares its prompt with the run before it: the lifecycle
// card labels the change, the change list names the fragments, and a
// changed fragment shows a line diff once both runs' texts are loaded.
const systemTurns = computed(() => {
  const detail = props.row.detail
  if (detail.kind !== 'system') return []
  return detail.previous ? [detail.lifecycle, detail.previous] : [detail.lifecycle]
})
const promptChanges = computed<PromptChange[]>(() => {
  const detail = props.row.detail
  if (detail.kind !== 'system' || !detail.previous) return []
  return promptFragmentChanges(detail.lifecycle.snapshot, detail.previous.snapshot, props.previews)
})
const previousRunId = computed(() => {
  const detail = props.row.detail
  if (detail.kind !== 'system' || !detail.previous || !promptChanges.value.some(change => change.change === 'changed')) return null
  return detail.previous.run_id ?? null
})
const { fragments: previousFragments, status: diffStatus } = useContextLifecycleFragments(previousRunId)
const diffs = computed(() => {
  const current = new Map(fragments.value.map(fragment => [fragment.text_hash ?? '', fragment]))
  const previous = new Map(previousFragments.value.map(fragment => [fragment.text_hash ?? '', fragment]))
  const out = new Map<string, DiffLine[] | null>()
  for (const change of promptChanges.value) {
    if (change.change !== 'changed') continue
    const before = previous.get(change.previousHash)
    const after = current.get(change.currentHash)
    out.set(change.key, before?.available && after?.available ? lineDiff(before.text ?? '', after.text ?? '') : null)
  }
  return out
})
function diffFor(change: PromptChange): DiffLine[] | null {
  return diffs.value.get(change.key) ?? null
}

const PROMPT_CHANGE_TONE: Record<PromptChangeKind, string> = {
  added: 'text-accent-green',
  removed: 'text-destructive',
  changed: 'text-warning',
}
const DIFF_LINE_CLASS: Record<'same' | 'add' | 'remove', string> = {
  same: 'text-muted-foreground',
  add: 'text-accent-green',
  remove: 'text-destructive',
}
const DIFF_PREFIX: Record<'same' | 'add' | 'remove', string> = { same: '  ', add: '+ ', remove: '- ' }

// The per-fragment audit is read only for the rows it can explain: what the
// system slot holds, and which history rows were kept, trimmed or dropped.
const decisionScope = computed<DecisionScope | null>(() => decisionScopeOf(props.row))
const decisionRunId = computed(() => {
  const detail = props.row.detail
  if (!decisionScope.value || (detail.kind !== 'system' && detail.kind !== 'context')) return null
  return detail.lifecycle.run_id ?? null
})
const { decisions, status: decisionStatus } = useContextLifecycleDecisions(decisionRunId)

const DECISION_TONE: Record<string, string> = {
  selected: 'text-muted-foreground',
  trimmed: 'text-warning',
  dropped: 'text-destructive',
}

// Decision ids read as the selector named them (system.prompt.body,
// message.004); the source id adds the tool or file the fragment came from.
function decisionLabel(decision: ContextfragSelectionDecision): string {
  const named = [decision.id, decision.source_id].filter(Boolean).join(' · ')
  if (named) return named
  const ref = decision.ref
  return ref?.namespace && ref.id ? `${ref.namespace}/${ref.id}` : decision.source ?? ''
}

const scopedDecisions = computed(() => {
  const scope = decisionScope.value
  if (!scope) return []
  return decisions.value.filter((decision) => {
    switch (scope) {
      case 'system':
        return decision.slot === 'system'
      case 'history':
        return decision.slot === 'history'
      case 'cut':
        return decision.decision != null && decision.decision !== 'selected'
    }
  })
})
const decisionRows = computed(() => scopedDecisions.value.slice(0, DECISION_ROW_LIMIT).map((decision, index) => ({
  key: decision.id || `${index}`,
  decision: t(`chat.trajectory.decision.${decision.decision ?? 'selected'}`),
  tone: DECISION_TONE[decision.decision ?? 'selected'] ?? 'text-muted-foreground',
  label: decisionLabel(decision),
  reason: decision.reason ?? '',
  tokens: decision.token_estimate ? formatTokenCount(decision.token_estimate) : '',
})))
const hiddenDecisions = computed(() => Math.max(scopedDecisions.value.length - DECISION_ROW_LIMIT, 0))

const timingRows = computed(() => {
  const detail = props.row.detail
  if (detail.kind !== 'block') return []
  const entries: { key: string, label: string, value: string }[] = []
  if (detail.block.type === 'tool') {
    const timing = detail.block.execution_timing
    if (timing) {
      entries.push({ key: 'started', label: t('chat.trajectory.inspectorStarted'), value: clock(timing.started_at_ms) })
      entries.push({ key: 'ended', label: t('chat.trajectory.inspectorEnded'), value: clock(timing.ended_at_ms) })
      entries.push({ key: 'duration', label: t('chat.trajectory.inspectorDuration'), value: formatDurationMs(timing.ended_at_ms - timing.started_at_ms) })
    }
    return entries
  }
  const trace = detail.trace
  if (!trace) return entries
  entries.push({ key: 'started', label: t('chat.trajectory.inspectorStarted'), value: clock(trace.started_at_ms) })
  if (trace.first_token_at_ms) {
    entries.push({ key: 'firstToken', label: t('chat.trajectory.inspectorFirstToken'), value: clock(trace.first_token_at_ms) })
    entries.push({ key: 'ttft', label: t('chat.trajectory.inspectorTtft'), value: formatDurationMs(trace.first_token_at_ms - trace.started_at_ms) })
  }
  entries.push({ key: 'ended', label: t('chat.trajectory.inspectorEnded'), value: clock(trace.ended_at_ms) })
  entries.push({ key: 'duration', label: t('chat.trajectory.inspectorDuration'), value: formatDurationMs(trace.ended_at_ms - trace.started_at_ms) })
  if (trace.finish_reason) entries.push({ key: 'finish', label: t('chat.trajectory.inspectorFinishReason'), value: trace.finish_reason })
  const usage = trace.usage
  if (usage) {
    const tokenRows: [string, string, number | undefined][] = [
      ['inputTokens', 'chat.trajectory.inspectorInputTokens', usage.input_tokens],
      ['cachedTokens', 'chat.trajectory.inspectorCachedTokens', usage.cached_input_tokens],
      ['cacheWrite', 'chat.trajectory.inspectorCacheWrite', usage.cache_write_tokens],
      ['outputTokens', 'chat.trajectory.inspectorOutputTokens', usage.output_tokens],
      ['reasoningTokens', 'chat.trajectory.inspectorReasoningTokens', usage.reasoning_tokens],
    ]
    for (const [key, labelKey, value] of tokenRows) {
      if (value != null && value > 0) entries.push({ key, label: t(labelKey), value: formatTokenCount(value) })
    }
  } else {
    entries.push({ key: 'usage', label: t('chat.trajectory.inspectorUsage'), value: t('chat.trajectory.usageNotReported') })
  }
  return entries
})
</script>
