<template>
  <div
    ref="viewport"
    class="h-full overflow-x-hidden overflow-y-auto"
    role="listbox"
    :aria-label="$t('chat.trajectory.title')"
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
          role="option"
          tabindex="0"
          class="grid cursor-pointer grid-cols-[3.5rem_5.25rem_minmax(0,1fr)_4.5rem] items-center gap-2 px-3 text-body outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
          :class="row.turnStart ? 'border-t border-border' : 'border-t border-transparent'"
          :style="{ height: `${rowHeight}px` }"
          :aria-selected="row.key === selectedKey"
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
              v-if="rowLabel(row)"
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
import { computed, useTemplateRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowRight } from 'lucide-vue-next'
import { Spinner } from '@felinic/ui'
import { entryRefs, type TrajectoryRow } from '../../composables/trajectory-model'
import { contextLabelKey, contextPreview, formatDurationMs, fragmentRowPreview, KIND_LABEL_KEY, KIND_TONE_CLASS, type FragmentPreviews } from '../../composables/trajectory-view'
import { formatTokenCount } from '../../composables/context-categories'
import { useVirtualRows } from '../../composables/useVirtualRows'

const props = defineProps<{
  rows: TrajectoryRow[]
  selectedKey: string | null
  previews?: FragmentPreviews | null
}>()

const emit = defineEmits<{ select: [key: string] }>()

const { t } = useI18n()
const viewport = useTemplateRef<HTMLElement>('viewport')
const count = computed(() => props.rows.length)
const { range, rowHeight, keepAnchored } = useVirtualRows(viewport, count)
const mounted = computed(() => props.rows.slice(range.value.start, range.value.end))

// Older history loads in above the current rows; the first key that was on
// screen tells how many rows arrived in front of it.
watch(() => props.rows, (rows, previous) => {
  const firstKey = previous?.[0]?.key
  if (!firstKey || rows === previous) return
  const index = rows.findIndex(row => row.key === firstKey)
  if (index > 0) keepAnchored(index)
}, { flush: 'pre' })

// A selection made on the strip may point at a row outside the window;
// bring it into view without disturbing selections made in the list itself.
watch(() => props.selectedKey, (key) => {
  const element = viewport.value
  if (!key || !element) return
  const index = props.rows.findIndex(row => row.key === key)
  if (index < 0 || (index >= range.value.start && index < range.value.end)) return
  element.scrollTop = Math.max(index * rowHeight.value - element.clientHeight / 2, 0)
})

function rowLabel(row: TrajectoryRow): string {
  if (row.detail.kind === 'context') {
    const key = contextLabelKey(row.detail.entry)
    return key ? t(key) : row.label
  }
  if (row.kind === 'context') {
    const key = row.label === 'steering' || row.label === 'prepared' ? `chat.trajectory.${row.label}` : ''
    return key ? t(key) : row.label
  }
  if (row.kind === 'compaction') {
    return row.label ? t(`chat.trajectory.compactionStatus.${row.label}`) : ''
  }
  return row.kind === 'tool' || row.kind === 'error' ? row.label : ''
}

function rowPreview(row: TrajectoryRow): string {
  switch (row.detail.kind) {
    case 'system':
      return fragmentRowPreview(row.detail.entry.refs, props.previews)
        ?? t('chat.trajectory.systemPreview', { fragments: row.detail.entry.fragments, tokens: formatTokenCount(row.detail.entry.tokens) })
    case 'context':
      if (row.detail.entry.kind === 'tool_defs') return contextPreview(row.detail.entry, t)
      return fragmentRowPreview(entryRefs(row.detail.entry), props.previews) ?? contextPreview(row.detail.entry, t)
    default:
      return row.preview
  }
}
</script>
