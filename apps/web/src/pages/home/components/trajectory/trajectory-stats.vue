<template>
  <div
    class="flex flex-wrap items-center gap-x-2 gap-y-0.5 border-t border-border px-3 py-1.5 text-caption tabular-nums text-muted-foreground"
    data-testid="trajectory-stats"
  >
    <template
      v-for="(group, index) in groups"
      :key="index"
    >
      <span
        v-if="index > 0"
        aria-hidden="true"
      >|</span>
      <span class="inline-flex items-center gap-1.5">
        <template
          v-for="(segment, segmentIndex) in group"
          :key="segment.key"
        >
          <span
            v-if="segmentIndex > 0"
            aria-hidden="true"
          >·</span>
          <span>{{ $t(`chat.trajectory.${segment.key}`, segment.params) }}</span>
        </template>
      </span>
    </template>
    <span class="ml-auto">{{ $t('chat.trajectory.statsScope') }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { TrajectoryStats } from '../../composables/trajectory-model'
import { statsSegments } from '../../composables/trajectory-view'

const props = defineProps<{ stats: TrajectoryStats }>()
const groups = computed(() => statsSegments(props.stats))
</script>
