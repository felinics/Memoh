<template>
  <div
    class="@container flex h-full w-full flex-col"
    data-testid="trajectory-pane"
    @keydown.esc="closeInspector"
  >
    <div class="flex items-center justify-between gap-2 border-b border-border px-3 py-1.5">
      <span class="text-label font-semibold text-foreground">{{ $t('chat.trajectory.title') }}</span>
      <SegmentedControl
        v-model="mode"
        :items="modeItems"
        :aria-label="$t('chat.trajectory.modeAria')"
      />
    </div>

    <Empty
      v-if="!hasTarget"
      class="min-h-40 flex-1"
    >
      <EmptyDescription>{{ $t('chat.trajectory.noSession') }}</EmptyDescription>
    </Empty>

    <template v-else>
      <TrajectoryOverview
        :bars="bars"
        :selected-key="selectedKey"
        @select="focus"
      />
      <div
        ref="split"
        class="relative flex min-h-0 flex-1 [--trajectory-inspector:cover] @3xl:[--trajectory-inspector:beside]"
        data-testid="trajectory-split"
      >
        <div
          ref="column"
          class="flex min-h-0 min-w-0 flex-1 flex-col"
          :inert="listCovered || undefined"
          data-testid="trajectory-column"
        >
          <div
            v-if="hasOlder && !loadingMessages"
            class="flex items-center justify-center border-b border-border py-1"
          >
            <Button
              variant="ghost"
              size="sm"
              :loading="loadingOlder"
              loading-mode="icon"
              @click="loadOlder"
            >
              {{ $t('chat.trajectory.loadOlder') }}
            </Button>
          </div>
          <div
            v-if="loadingMessages && rows.length === 0"
            class="space-y-1.5 px-3 py-2"
          >
            <Skeleton
              v-for="row in 6"
              :key="row"
              class="h-6 w-full"
            />
          </div>
          <Empty
            v-else-if="rows.length === 0"
            class="min-h-40 flex-1"
          >
            <EmptyDescription>{{ $t('chat.trajectory.empty') }}</EmptyDescription>
          </Empty>
          <TrajectoryLedger
            v-else
            ref="ledger"
            class="min-h-0 flex-1"
            :rows="rows"
            :selected-key="selectedKey"
            :previews="fragmentPreviews"
            @select="select"
            @navigate="navigate"
          />
        </div>
        <div
          v-if="selectedRow"
          class="absolute inset-0 bg-background @3xl:static @3xl:w-80 @3xl:shrink-0 @3xl:border-l @3xl:border-border"
          data-testid="trajectory-inspector-host"
        >
          <TrajectoryInspector
            ref="inspector"
            :row="selectedRow"
            :previews="fragmentPreviews"
            @close="closeInspector"
          />
        </div>
      </div>
      <TrajectoryStats :stats="stats" />
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useTemplateRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, Empty, EmptyDescription, SegmentedControl, Skeleton } from '@felinic/ui'
import type { SegmentedItem } from '@felinic/ui'
import { useTrajectory } from '../../composables/useTrajectory'
import type { TimelineMode } from '../../composables/trajectory-view'
import TrajectoryOverview from './trajectory-overview.vue'
import TrajectoryLedger from './trajectory-ledger.vue'
import TrajectoryInspector from './trajectory-inspector.vue'
import TrajectoryStats from './trajectory-stats.vue'

const { t } = useI18n()
const { hasTarget, rows, stats, fragmentPreviews, loadingMessages, selectedKey, selectedRow, bars, mode, hasOlder, loadingOlder, loadOlder, select, focus } = useTrajectory()

const split = useTemplateRef<HTMLElement>('split')
const column = useTemplateRef<HTMLElement>('column')
const ledger = useTemplateRef<InstanceType<typeof TrajectoryLedger>>('ledger')
const inspector = useTemplateRef<InstanceType<typeof TrajectoryInspector>>('inspector')

// The container query decides whether the inspector sits beside the list
// or covers it, and leaves the answer in a custom property so the focus
// rules below follow the same breakpoint as the layout; the pane re-reads
// it whenever its width changes.
const covers = ref(false)
let observer: ResizeObserver | null = null

function readCovers() {
  const element = split.value
  covers.value = !!element && getComputedStyle(element).getPropertyValue('--trajectory-inspector').trim() === 'cover'
}

watch(split, (element) => {
  observer?.disconnect()
  observer = null
  if (!element) return
  readCovers()
  if (typeof ResizeObserver !== 'undefined') {
    observer = new ResizeObserver(readCovers)
    observer.observe(element)
  }
}, { immediate: true, flush: 'post' })

onBeforeUnmount(() => observer?.disconnect())

// Beside the list, the inspector follows the caret. Covering it, every
// arrow press would hide the list, so the caret moves alone and Enter or a
// click opens the row.
function navigate(key: string) {
  if (!covers.value) focus(key)
}

function closeInspector() {
  const key = selectedKey.value
  if (!key) return
  select(null)
  void nextTick(() => ledger.value?.focusRow(key))
}

// While the inspector covers the list, the list is inert so Tab cannot
// reach what is hidden; whatever focus the cover takes away moves into the
// inspector, whether the cover came from opening a row or from the pane
// narrowing with a row open.
const listCovered = computed(() => covers.value && !!selectedRow.value)

watch(listCovered, async (covered) => {
  if (!covered) return
  const active = document.activeElement
  const displaced = !active || active === document.body || !!column.value?.contains(active)
  await nextTick()
  if (displaced) inspector.value?.focus()
})

const modeItems = computed<SegmentedItem<TimelineMode>[]>(() => [
  { value: 'duration', label: t('chat.trajectory.modeDuration') },
  { value: 'sequence', label: t('chat.trajectory.modeSequence') },
])
</script>
