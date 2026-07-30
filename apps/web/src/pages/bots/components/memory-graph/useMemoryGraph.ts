import { computed, onBeforeUnmount, ref, toRef, watch, type Ref } from 'vue'
import type { EChartsType } from 'echarts/core'
import { buildMemoryGraphOption } from './build-chart-option'
import { useMemoryGraphData } from './useMemoryGraphData'
import { useMemoryGraphInteraction } from './useMemoryGraphInteraction'
import { useMemoryGraphLayout } from './useMemoryGraphLayout'
import { useMemoryGraphTheme } from './useMemoryGraphTheme'
import type { GraphEdgeCandidate } from './types'

export function useMemoryGraph(botId: Ref<string> | (() => string)) {
  const botIdRef = typeof botId === 'function' ? computed(botId) : botId

  const chartRef = ref<{ chart?: EChartsType } | null>(null)
  const hoveredNodeId = ref<string | null>(null)

  const { chartTheme } = useMemoryGraphTheme()
  const {
    loading,
    graphData,
    visibleGraphEdges,
    fetchGraph,
  } = useMemoryGraphData(() => botIdRef.value)

  const {
    layoutReady,
    nodePositions,
    viewZoom,
    viewCenter,
    graphZoom,
    resetLayoutState,
    syncViewFromChart,
    scheduleLayoutCapture,
  } = useMemoryGraphLayout(graphData)

  const {
    handleGraphPointerDown,
    handleGraphWheel,
    createTooltipPositioner,
    installChartEvents,
    teardownChartEvents,
  } = useMemoryGraphInteraction({
    chartRef,
    hoveredNodeId,
    syncViewFromChart,
    scheduleLayoutCapture,
  })

  const chartAutoresize = { throttle: 120 }
  const chartUpdateOptions = { lazyUpdate: true }
  const tooltipPositioner = createTooltipPositioner()

  const chartOption = computed(() => {
    if (!graphData.value || graphData.value.nodes.length === 0) return {}
    const nodes = graphData.value.nodes.filter((node): node is typeof node & { id: string } => !!node.id)
    return buildMemoryGraphOption({
      theme: chartTheme.value,
      nodes,
      edges: visibleGraphEdges.value as GraphEdgeCandidate[],
      hoveredNodeId: hoveredNodeId.value,
      graphZoom: graphZoom.value,
      layoutReady: layoutReady.value,
      nodePositions: nodePositions.value,
      viewZoom: viewZoom.value,
      viewCenter: viewCenter.value,
      tooltipPosition: tooltipPositioner,
    })
  })

  watch(botIdRef, () => {
    resetLayoutState()
    hoveredNodeId.value = null
    void fetchGraph()
  }, { immediate: true })

  watch(graphData, () => {
    resetLayoutState()
  })

  watch(
    () => chartRef.value?.chart,
    (chart) => {
      if (chart) {
        installChartEvents(chart)
        scheduleLayoutCapture(chart)
      }
      else {
        teardownChartEvents()
      }
    },
  )

  onBeforeUnmount(() => {
    teardownChartEvents()
  })

  return {
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
  }
}

/** Convenience wrapper when botId comes from props. */
export function useMemoryGraphFromProps(props: { botId: string }) {
  return useMemoryGraph(toRef(props, 'botId'))
}
