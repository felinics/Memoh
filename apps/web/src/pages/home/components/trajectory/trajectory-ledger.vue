<template>
  <div
    ref="viewport"
    class="h-full overflow-y-auto"
    data-testid="trajectory-ledger"
  >
    <div
      class="relative"
      :style="{ height: `${range.totalHeight}px` }"
    >
      <div
        class="absolute inset-x-0"
        :style="{ top: `${range.offsetTop}px` }"
      >
        <div
          v-for="row in mounted"
          :key="row.key"
          role="button"
          tabindex="0"
          class="grid cursor-pointer grid-cols-[3.5rem_5.25rem_minmax(0,1fr)_4.5rem] items-center gap-2 px-3 text-body outline-none focus-visible:ring-2 focus-visible:ring-ring"
          :class="row.turnStart ? 'border-t border-border' : 'border-t border-transparent'"
          :style="{ height: `${rowHeight}px` }"
          :data-ui-selected="row.key === selectedKey ? '' : undefined"
          :data-testid="`trajectory-row-${row.kind}`"
          @click="emit('select', row.key)"
          @keydown.enter.prevent="emit('select', row.key)"
          @keydown.space.prevent="emit('select', row.key)"
        >
          <span class="truncate text-caption text-muted-foreground">
            <template v-if="row.turnStart">{{ $t('chat.trajectory.turn', { n: row.turnLabel }) }}</template>
          </span>
          <span
            class="truncate text-caption font-medium"
            :class="KIND_TONE_CLASS[row.kind]"
          >{{ $t(KIND_LABEL_KEY[row.kind]) }}</span>
          <span class="flex min-w-0 items-center gap-1.5 truncate">
            <span
              v-if="row.kind === 'tool' || row.kind === 'context' || row.kind === 'error'"
              class="shrink-0 font-mono text-foreground"
            >{{ rowLabel(row) }}</span>
            <span
              class="truncate text-muted-foreground"
              :class="row.kind === 'tool' ? 'font-mono' : ''"
            >{{ rowPreview(row) }}</span>
            <template v-if="row.output">
              <ArrowRight class="size-3 shrink-0 text-muted-foreground" />
              <span class="truncate font-mono text-muted-foreground">{{ row.output }}</span>
            </template>
          </span>
          <span class="truncate text-right text-caption tabular-nums text-muted-foreground">
            <Spinner
              v-if="row.running"
              class="ml-auto size-3"
            />
            <template v-else-if="row.startedAtMs != null && row.endedAtMs != null">{{ formatDurationMs(row.endedAtMs - row.startedAtMs) }}</template>
          </span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, useTemplateRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowRight } from 'lucide-vue-next'
import { Spinner } from '@felinic/ui'
import type { TrajectoryRow } from '../../composables/trajectory-model'
import { formatDurationMs, KIND_LABEL_KEY, KIND_TONE_CLASS } from '../../composables/trajectory-view'
import { formatTokenCount } from '../../composables/context-categories'
import { useVirtualRows } from '../../composables/useVirtualRows'

const props = defineProps<{
  rows: TrajectoryRow[]
  selectedKey: string | null
}>()

const emit = defineEmits<{ select: [key: string] }>()

const { t } = useI18n()
const rowHeight = 28
const viewport = useTemplateRef<HTMLElement>('viewport')
const count = computed(() => props.rows.length)
const { range, scrollToIndex, scrollToBottom } = useVirtualRows(viewport, count, rowHeight)
const mounted = computed(() => props.rows.slice(range.value.start, range.value.end))

function rowLabel(row: TrajectoryRow): string {
  if (row.kind === 'context') {
    const key = row.label === 'steering' || row.label === 'prepared' ? `chat.trajectory.${row.label}` : ''
    return key ? t(key) : row.label
  }
  return row.label
}

function rowPreview(row: TrajectoryRow): string {
  if (row.kind !== 'system' || row.detail.kind !== 'system') return row.preview
  const tokens = row.detail.lifecycle.snapshot?.counts?.token_estimate ?? 0
  return t('chat.trajectory.systemPreview', { tokens: formatTokenCount(tokens) })
}

defineExpose({ scrollToIndex, scrollToBottom })
</script>
