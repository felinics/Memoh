<template>
  <div
    class="border-b border-border px-3 py-2"
    data-testid="trajectory-overview"
  >
    <div
      v-if="!timeline"
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
          <div
            v-for="bar in barsByLane[lane]"
            :key="bar.key"
            class="absolute inset-y-0 overflow-hidden rounded-sm"
            :class="LANE_BAR_CLASS[lane]"
            :style="barStyle(bar)"
            :title="barTitle(bar)"
            :data-testid="`trajectory-bar-${bar.key}`"
          >
            <div
              v-if="bar.splitPct != null"
              class="absolute inset-y-0 left-0"
              :class="LANE_TTFT_CLASS"
              :style="{ width: `${bar.splitPct}%` }"
            />
          </div>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { TimelineLane, TurnTimeline } from '../../composables/trajectory-model'
import { formatDurationMs, laneGeometry, LANE_BAR_CLASS, LANE_LABEL_KEY, LANE_TTFT_CLASS, type LaneBar, type TimelineMode } from '../../composables/trajectory-view'
import { formatTokenCount } from '../../composables/context-categories'

const props = defineProps<{
  timeline: TurnTimeline | null
  mode: TimelineMode
}>()

const { t } = useI18n()
const lanes: TimelineLane[] = ['input', 'model', 'tools']

const bars = computed(() => (props.timeline ? laneGeometry(props.timeline, props.mode) : []))
const barsByLane = computed(() => {
  const grouped: Record<TimelineLane, LaneBar[]> = { input: [], model: [], tools: [] }
  for (const bar of bars.value) grouped[bar.lane].push(bar)
  return grouped
})

function barStyle(bar: LaneBar) {
  return {
    left: `${bar.leftPct}%`,
    width: `${bar.widthPct}%`,
    opacity: bar.lane === 'input' ? 0.35 + bar.intensity * 0.65 : 1,
  }
}

function barTitle(bar: LaneBar): string {
  const duration = formatDurationMs(bar.end - bar.start)
  if (bar.lane === 'input') {
    return `${t('chat.trajectory.step', { n: bar.stepIndex ?? 0 })} · ${formatTokenCount(bar.tokens)} · ${duration}`
  }
  const label = bar.label ? `${bar.label} · ` : ''
  return `${label}${duration}`
}
</script>
