<template>
  <SettingsSection :title="$t('memory.graphTitle')">
    <template #actions>
      <div
        v-if="graphData"
        class="flex shrink-0 gap-4 text-body text-muted-foreground"
      >
        <span>{{ graphData.nodes.length }} {{ $t('memory.graphNodes') }}</span>
        <span>{{ visibleGraphEdges.length }} {{ $t('memory.graphEdges') }}</span>
      </div>
    </template>

    <div
      class="relative h-[30rem]"
      @wheel.capture.prevent="handleGraphWheel"
      @pointerdown="handleGraphPointerDown"
    >
      <PanePlaceholder
        v-if="loading"
        loading
      >
        {{ $t('common.loading') }}
      </PanePlaceholder>
      <template v-else-if="graphData && graphData.nodes.length > 0">
        <VChart
          ref="chartRef"
          :option="chartOption"
          :autoresize="chartAutoresize"
          :update-options="chartUpdateOptions"
          class="size-full"
          :class="{ invisible: !layoutReady }"
        />
        <PanePlaceholder
          v-if="!layoutReady"
          loading
          class="absolute inset-0 z-(--z-raised)"
        >
          {{ $t('common.loading') }}
        </PanePlaceholder>
      </template>
      <PanePlaceholder v-else>
        {{ $t('memory.graphEmpty') }}
      </PanePlaceholder>
    </div>
  </SettingsSection>
</template>

<script setup lang="ts">
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { GraphChart } from 'echarts/charts'
import { TooltipComponent } from 'echarts/components'
import VChart from 'vue-echarts'
import { PanePlaceholder, SettingsSection } from '@felinic/ui'
import { useMemoryGraphFromProps } from './memory-graph/useMemoryGraph'

use([CanvasRenderer, GraphChart, TooltipComponent])

const props = defineProps<{ botId: string }>()

const {
  loading,
  graphData,
  visibleGraphEdges,
  layoutReady,
  chartRef,
  chartOption,
  chartAutoresize,
  chartUpdateOptions,
  handleGraphPointerDown,
  handleGraphWheel,
  fetchGraph,
} = useMemoryGraphFromProps(props)

defineExpose({ refresh: fetchGraph })
</script>
