<template>
  <div
    class="border-b border-border px-3 py-2"
    data-testid="trajectory-overview"
  >
    <div
      v-if="bars.length === 0"
      class="text-caption text-muted-foreground"
    >
      {{ $t('chat.trajectory.timelineEmpty') }}
    </div>
    <div
      v-else
      class="grid grid-cols-[3rem_minmax(0,1fr)] gap-x-2 gap-y-1"
    >
      <template
        v-for="lane in lanes"
        :key="lane"
      >
        <span class="text-caption leading-4 text-muted-foreground">{{ $t(LANE_LABEL_KEY[lane]) }}</span>
        <div class="relative h-4 overflow-hidden rounded-sm bg-accent">
          <button
            v-for="bar in barsByLane[lane]"
            :key="bar.key"
            type="button"
            class="absolute inset-y-0 cursor-pointer overflow-hidden rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
            :class="[KIND_BAR_CLASS[bar.kind], bar.rowKey === selectedKey ? 'ring-2 ring-ring' : '', bar.running ? 'animate-pulse' : '']"
            :style="{ left: `${bar.leftPct}%`, width: `${bar.widthPct}%` }"
            :title="barTitle(bar)"
            :aria-label="barTitle(bar)"
            :data-ui-selected="bar.rowKey === selectedKey ? '' : undefined"
            :data-testid="`trajectory-bar-${bar.key}`"
            @click="emit('select', bar.rowKey)"
          >
            <span
              v-if="bar.splitPct != null"
              class="absolute inset-y-0 left-0"
              :class="LANE_TTFT_CLASS"
              :style="{ width: `${bar.splitPct}%` }"
            />
          </button>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { TimelineLane } from '../../composables/trajectory-model'
import { formatDurationMs, KIND_BAR_CLASS, KIND_LABEL_KEY, LANE_LABEL_KEY, LANE_TTFT_CLASS, type RowMapBar } from '../../composables/trajectory-view'

const props = defineProps<{
  bars: RowMapBar[]
  selectedKey: string | null
}>()

const emit = defineEmits<{ select: [key: string] }>()

const { t } = useI18n()
const lanes: TimelineLane[] = ['input', 'model', 'tools']

const barsByLane = computed(() => {
  const grouped: Record<TimelineLane, RowMapBar[]> = { input: [], model: [], tools: [] }
  for (const bar of props.bars) grouped[bar.lane].push(bar)
  return grouped
})

function barTitle(bar: RowMapBar): string {
  const parts = [bar.rows > 1 ? t('chat.trajectory.barRows', { n: bar.rows }) : t(KIND_LABEL_KEY[bar.kind])]
  if (bar.label) parts.push(bar.label)
  if (bar.durationMs > 0) parts.push(formatDurationMs(bar.durationMs))
  return parts.join(' · ')
}
</script>
