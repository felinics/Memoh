import { ref, type Ref } from 'vue'
import type { EChartsType } from 'echarts/core'
import {
  LAYOUT_CAPTURE_MAX_ATTEMPTS,
  LAYOUT_MIN_READY_RATIO,
} from './constants'
import { isLayoutCaptureReady } from './graph-data'
import { fitGraphToView } from './graph-layout'
import { readSeriesCenter, readSeriesZoom } from './build-chart-option'
import type { GraphData, PinnedNodePosition } from './types'

export function useMemoryGraphLayout(graphData: Ref<GraphData | null>) {
  const layoutReady = ref(false)
  const nodePositions = ref(new Map<string, PinnedNodePosition>())
  const viewZoom = ref(1)
  const viewCenter = ref<[number, number] | null>(null)
  const graphZoom = ref(1)

  function resetLayoutState() {
    layoutReady.value = false
    nodePositions.value = new Map()
    viewZoom.value = 1
    viewCenter.value = null
    graphZoom.value = 1
  }

  function syncViewFromChart(chart: EChartsType) {
    viewZoom.value = readSeriesZoom(chart)
    viewCenter.value = readSeriesCenter(chart) ?? viewCenter.value
    graphZoom.value = viewZoom.value
  }

  function captureLayoutPositions(chart: EChartsType): boolean {
    if (layoutReady.value) return true
    const nodes = graphData.value?.nodes.filter((node): node is typeof node & { id: string } => !!node.id) ?? []
    if (nodes.length === 0) return false

    try {
      const seriesModel = chart.getModel().getSeriesByIndex(0)
      if (!seriesModel) return false

      const data = seriesModel.getData()
      const positions = new Map<string, PinnedNodePosition>()
      let ready = 0
      data.each((idx: number) => {
        const layout = data.getItemLayout(idx) as [number, number] | undefined
        const raw = data.getRawDataItem(idx) as { id?: string, name?: string }
        const id = String(raw?.id ?? raw?.name ?? '')
        if (!id || !layout || !Number.isFinite(layout[0]) || !Number.isFinite(layout[1])) return
        ready += 1
        positions.set(id, { x: layout[0], y: layout[1] })
      })
      if (!isLayoutCaptureReady(ready, nodes.length, LAYOUT_MIN_READY_RATIO)) return false

      const fit = fitGraphToView(positions, {
        width: chart.getWidth(),
        height: chart.getHeight(),
      })
      nodePositions.value = positions
      if (fit) {
        viewZoom.value = fit.zoom
        viewCenter.value = fit.center
        graphZoom.value = fit.zoom
      }
      layoutReady.value = true
      return true
    }
    catch {
      return false
    }
  }

  function scheduleLayoutCapture(chart: EChartsType, attempt = 0) {
    if (captureLayoutPositions(chart) || attempt >= LAYOUT_CAPTURE_MAX_ATTEMPTS) return
    setTimeout(
      () => scheduleLayoutCapture(chart, attempt + 1),
      attempt < 4 ? 120 : 250,
    )
  }

  return {
    layoutReady,
    nodePositions,
    viewZoom,
    viewCenter,
    graphZoom,
    resetLayoutState,
    syncViewFromChart,
    scheduleLayoutCapture,
  }
}
