<template>
  <div
    class="flex h-full w-full flex-col"
    data-testid="trajectory-pane"
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
      <div class="flex min-h-0 flex-1">
        <div class="flex min-h-0 min-w-0 flex-1 flex-col">
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
            class="min-h-0 flex-1"
            :rows="rows"
            :selected-key="selectedKey"
            :previews="fragmentPreviews"
            @select="select"
          />
        </div>
        <div
          v-if="selectedRow"
          class="w-80 shrink-0 border-l border-border"
        >
          <TrajectoryInspector
            :row="selectedRow"
            :previews="fragmentPreviews"
            @close="select(null)"
          />
        </div>
      </div>
      <TrajectoryStats :stats="stats" />
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
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

const modeItems = computed<SegmentedItem<TimelineMode>[]>(() => [
  { value: 'duration', label: t('chat.trajectory.modeDuration') },
  { value: 'sequence', label: t('chat.trajectory.modeSequence') },
])
</script>
