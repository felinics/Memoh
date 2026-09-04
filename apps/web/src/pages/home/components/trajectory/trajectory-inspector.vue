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
          :turns="[row.detail.lifecycle]"
          :has-older="false"
        />
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
    </div>
  </ScrollArea>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { X } from 'lucide-vue-next'
import { Button, ScrollArea } from '@felinic/ui'
import type { TrajectoryRow } from '../../composables/trajectory-model'
import { formatDurationMs, KIND_LABEL_KEY, KIND_TONE_CLASS } from '../../composables/trajectory-view'
import { formatTokenCount } from '../../composables/context-categories'
import ContextLifecycleTurns from '../context-lifecycle-turns.vue'

const props = defineProps<{ row: TrajectoryRow }>()
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
