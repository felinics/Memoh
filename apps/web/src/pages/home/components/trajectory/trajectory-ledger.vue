<template>
  <div
    ref="viewport"
    class="h-full overflow-x-hidden overflow-y-auto outline-none [overflow-anchor:none]"
    role="listbox"
    :tabindex="rowFocused ? -1 : 0"
    :aria-label="$t('chat.trajectory.title')"
    data-testid="trajectory-ledger"
    @pointerdown="onPointerDown"
    @pointerup="onPointerUp"
    @pointercancel="onPointerUp"
    @focus="onListFocus"
    @focusin="onFocusIn"
    @focusout="onFocusOut"
    @keydown="onKeydown"
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
          :ref="(el) => bindRow(row.key, el)"
          role="option"
          tabindex="-1"
          class="grid cursor-pointer grid-cols-[3.5rem_5.25rem_minmax(0,1fr)_4.5rem] items-center gap-2 px-3 text-body outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
          :class="row.turnStart ? 'border-t border-border' : 'border-t border-transparent'"
          :style="{ height: `${rowHeight}px` }"
          :aria-selected="row.key === selectedKey"
          :data-ui-selected="row.key === selectedKey ? '' : undefined"
          :data-testid="`trajectory-row-${row.kind}`"
          @focus="activeKey = row.key"
          @click="emit('select', row.key)"
          @keydown.enter.prevent="emit('select', row.key)"
          @keydown.space.prevent="emit('select', row.key)"
        >
          <span class="truncate text-caption text-muted-foreground">
            <template v-if="row.turnStart">{{ row.turnId.startsWith('run:') ? $t('chat.trajectory.captureRun', { id: row.turnLabel }) : $t('chat.trajectory.turn', { n: row.turnLabel }) }}</template>
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
import { computed, nextTick, onDeactivated, ref, useTemplateRef, watch, type ComponentPublicInstance } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowRight } from 'lucide-vue-next'
import { Spinner } from '@felinic/ui'
import { entryRefs, type TrajectoryRow } from '../../composables/trajectory-model'
import { contextLabelKey, contextPreview, formatDurationMs, fragmentRowPreview, KIND_LABEL_KEY, KIND_TONE_CLASS, type FragmentPreviews } from '../../composables/trajectory-view'
import { formatTokenCount } from '../../composables/context-categories'
import { useVirtualRows } from '../../composables/useVirtualRows'
import { captureStageLabel } from '../../composables/context-trajectory-labels'

const props = defineProps<{
  rows: TrajectoryRow[]
  selectedKey: string | null
  previews?: FragmentPreviews | null
}>()

// select toggles the inspector on a row; navigate only says the caret moved,
// so the pane decides whether the inspector follows it.
const emit = defineEmits<{ select: [key: string], navigate: [key: string] }>()

const { t, te } = useI18n()
const viewport = useTemplateRef<HTMLElement>('viewport')
const count = computed(() => props.rows.length)
const { range, rowHeight, keepAnchored, scrollRowIntoView, pageRows } = useVirtualRows(viewport, count)
const mounted = computed(() => props.rows.slice(range.value.start, range.value.end))

// The list is one tab stop: rows are reached from the list itself, and the
// row that last had focus (or the selected one) takes the caret again. Rows
// outside the window are not in the DOM, so the list scrolls first and
// focuses once the row is mounted.
const rowElements = new Map<string, HTMLElement>()
const activeKey = ref<string | null>(null)
const rowFocused = ref(false)

// A focused row that scrolls out of the window is removed without every
// browser sending focusout, and a deactivated tab keeps its focus state
// with it; both hand the tab stop back to the list.
function bindRow(key: string, el: Element | ComponentPublicInstance | null) {
  if (el) {
    rowElements.set(key, el as HTMLElement)
    return
  }
  if (rowElements.get(key) === document.activeElement) rowFocused.value = false
  rowElements.delete(key)
}

onDeactivated(() => {
  rowFocused.value = false
})

function activeIndex(): number {
  const key = activeKey.value ?? props.selectedKey
  const index = key ? props.rows.findIndex(row => row.key === key) : -1
  return index >= 0 ? index : props.rows.length > 0 ? 0 : -1
}

async function focusRow(target: number | string): Promise<boolean> {
  const index = typeof target === 'string' ? props.rows.findIndex(row => row.key === target) : target
  const row = props.rows[index]
  if (!row) return false
  scrollRowIntoView(index)
  await nextTick()
  const element = rowElements.get(row.key)
  if (!element) return false
  activeKey.value = row.key
  element.focus({ preventScroll: true })
  return true
}

// Keyboard entry hands the caret to a row. A pointer pressed on the list
// itself (its scrollbar, the space under the rows) must not: the flag is
// raised for that press only, never for a press on a row, and drops with
// the release so a stale press cannot swallow a later keyboard entry.
let pointerOnList = false

function onPointerDown(event: PointerEvent) {
  pointerOnList = !(event.target as Element | null)?.closest('[role="option"]')
}

function onPointerUp() {
  pointerOnList = false
}

function onListFocus() {
  if (pointerOnList) {
    pointerOnList = false
    return
  }
  void focusRow(activeIndex())
}

function onFocusIn(event: FocusEvent) {
  rowFocused.value = event.target !== viewport.value
}

function onFocusOut(event: FocusEvent) {
  if (viewport.value?.contains(event.relatedTarget as Node | null)) return
  rowFocused.value = false
  pointerOnList = false
}

function onKeydown(event: KeyboardEvent) {
  const last = props.rows.length - 1
  if (last < 0) return
  const current = activeIndex()
  let next: number
  switch (event.key) {
    case 'ArrowDown':
      next = Math.min(current + 1, last)
      break
    case 'ArrowUp':
      next = Math.max(current - 1, 0)
      break
    case 'PageDown':
      next = Math.min(current + pageRows.value, last)
      break
    case 'PageUp':
      next = Math.max(current - pageRows.value, 0)
      break
    case 'Home':
      next = 0
      break
    case 'End':
      next = last
      break
    default:
      return
  }
  event.preventDefault()
  void focusRow(next)
  if (next !== current) emit('navigate', props.rows[next]!.key)
}

defineExpose({ focusRow })

// Older history loads in above the current rows; the first key that was on
// screen tells how many rows arrived in front of it.
watch(() => props.rows, (rows, previous) => {
  const firstKey = previous?.[0]?.key
  if (!firstKey || rows === previous) return
  const index = rows.findIndex(row => row.key === firstKey)
  if (index > 0) keepAnchored(index)
}, { flush: 'pre' })

// A selection made on the strip may point at a row off screen, mounted in
// the overscan or not; center it when it is not fully visible, and let it
// take the caret next.
watch(() => props.selectedKey, (key) => {
  if (!key) return
  activeKey.value = key
  const index = props.rows.findIndex(row => row.key === key)
  if (index >= 0) scrollRowIntoView(index, 'center')
})

function rowLabel(row: TrajectoryRow): string {
  if (row.detail.kind === 'capture') return captureStageLabel(row.detail.event.stage, t, te)
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
    case 'capture':
      return t('chat.trajectory.captureBlocks', { n: row.detail.event.block_count ?? 0 })
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
