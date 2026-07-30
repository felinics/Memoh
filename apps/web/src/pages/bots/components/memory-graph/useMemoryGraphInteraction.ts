import { ref, type Ref } from 'vue'
import type { ECElementEvent, EChartsType } from 'echarts/core'
import {
  GRAPH_DRAG_THRESHOLD_PX,
  GRAPH_ROAM_EVENT,
  GRAPH_WHEEL_ZOOM_RATE,
  MEMORY_GRAPH_SERIES_ID,
} from './constants'
import {
  isChartNodeData,
  nodeDataIndex,
  tooltipPositionForNode,
} from './build-chart-option'
import { wheelZoomScale } from './graph-layout'

export function useMemoryGraphInteraction(options: {
  chartRef: Ref<{ chart?: EChartsType } | null>
  hoveredNodeId: Ref<string | null>
  syncViewFromChart: (chart: EChartsType) => void
  scheduleLayoutCapture: (chart: EChartsType) => void
}) {
  const { chartRef, hoveredNodeId, syncViewFromChart, scheduleLayoutCapture } = options

  const isGraphDragging = ref(false)
  let uninstallChartEvents: (() => void) | null = null
  let uninstallCursorGuard: (() => void) | null = null
  let clearHoverTimer: ReturnType<typeof setTimeout> | null = null

  function refreshAnchoredTooltip() {
    const chart = chartRef.value?.chart
    const id = hoveredNodeId.value
    if (!chart || !id) return
    const dataIndex = nodeDataIndex(chart, id)
    if (dataIndex == null) return
    chart.dispatchAction({ type: 'showTip', seriesIndex: 0, dataIndex })
  }

  function handleChartMouseOver(params: ECElementEvent) {
    if (params.dataType !== 'node' || !isChartNodeData(params.data)) return
    if (clearHoverTimer != null) {
      clearTimeout(clearHoverTimer)
      clearHoverTimer = null
    }
    hoveredNodeId.value = params.data.id
  }

  function handleChartMouseOut(params: ECElementEvent) {
    if (params.dataType !== 'node') return
    if (clearHoverTimer != null) clearTimeout(clearHoverTimer)
    clearHoverTimer = setTimeout(() => {
      clearHoverTimer = null
      hoveredNodeId.value = null
    }, 40)
  }

  function handleChartGlobalOut() {
    if (clearHoverTimer != null) {
      clearTimeout(clearHoverTimer)
      clearHoverTimer = null
    }
    hoveredNodeId.value = null
  }

  function handleChartRoam() {
    const chart = chartRef.value?.chart
    if (!chart) return
    syncViewFromChart(chart)
    refreshAnchoredTooltip()
  }

  function handleChartFinished() {
    const chart = chartRef.value?.chart
    if (!chart) return
    scheduleLayoutCapture(chart)
  }

  function installCursorGuard(chart: EChartsType) {
    uninstallCursorGuard?.()
    const zr = chart.getZr()
    const proxy = zr.handler.proxy as { setCursor?: (cursorStyle?: string) => void }
    const rawSetCursor = proxy.setCursor?.bind(proxy)
    if (!rawSetCursor) return

    proxy.setCursor = (requested?: string) => {
      if (isGraphDragging.value) {
        rawSetCursor('move')
        return
      }
      const style = requested || 'default'
      if (style === 'grab' || style === 'move') {
        rawSetCursor('default')
        return
      }
      rawSetCursor(style)
    }
    proxy.setCursor('default')

    uninstallCursorGuard = () => {
      proxy.setCursor = rawSetCursor
      uninstallCursorGuard = null
    }
  }

  function syncDraggingCursor() {
    chartRef.value?.chart?.getZr().setCursorStyle(isGraphDragging.value ? 'move' : 'default')
  }

  function handleGraphPointerDown(event: PointerEvent) {
    if (event.button !== 0) return
    const startX = event.clientX
    const startY = event.clientY
    let active = false

    const onMove = (moveEvent: PointerEvent) => {
      if (active) return
      if (Math.hypot(moveEvent.clientX - startX, moveEvent.clientY - startY) < GRAPH_DRAG_THRESHOLD_PX) return
      active = true
      isGraphDragging.value = true
      syncDraggingCursor()
    }
    const onUp = () => {
      isGraphDragging.value = false
      syncDraggingCursor()
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      window.removeEventListener('pointercancel', onUp)
    }

    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    window.addEventListener('pointercancel', onUp)
  }

  function handleGraphWheel(event: WheelEvent) {
    const chart = chartRef.value?.chart
    if (!chart) return

    const surface = event.currentTarget
    if (!(surface instanceof HTMLElement)) return

    const rect = surface.getBoundingClientRect()
    const scale = wheelZoomScale(event.deltaY, event.deltaMode, rect.height, GRAPH_WHEEL_ZOOM_RATE)
    if (scale === 1) return

    chart.dispatchAction({
      type: 'graphRoam',
      seriesId: MEMORY_GRAPH_SERIES_ID,
      zoom: scale,
      originX: event.clientX - rect.left,
      originY: event.clientY - rect.top,
    })
  }

  function createTooltipPositioner() {
    return (
      point: [number, number],
      params: { dataType?: string, dataIndex?: number, seriesIndex?: number },
      _dom: unknown,
      _rect: unknown,
      size: { contentSize: [number, number], viewSize: [number, number] },
    ) => tooltipPositionForNode(chartRef.value?.chart, params, size) ?? point
  }

  function installChartEvents(chart: EChartsType) {
    uninstallChartEvents?.()
    installCursorGuard(chart)
    chart.on('mouseover', handleChartMouseOver)
    chart.on('mouseout', handleChartMouseOut)
    chart.on('globalout', handleChartGlobalOut)
    chart.on(GRAPH_ROAM_EVENT, handleChartRoam)
    chart.on('finished', handleChartFinished)

    uninstallChartEvents = () => {
      uninstallCursorGuard?.()
      chart.off('mouseover', handleChartMouseOver)
      chart.off('mouseout', handleChartMouseOut)
      chart.off('globalout', handleChartGlobalOut)
      chart.off(GRAPH_ROAM_EVENT, handleChartRoam)
      chart.off('finished', handleChartFinished)
      if (clearHoverTimer != null) {
        clearTimeout(clearHoverTimer)
        clearHoverTimer = null
      }
      hoveredNodeId.value = null
      uninstallChartEvents = null
    }
  }

  function teardownChartEvents() {
    uninstallChartEvents?.()
    uninstallCursorGuard?.()
  }

  return {
    isGraphDragging,
    handleGraphPointerDown,
    handleGraphWheel,
    createTooltipPositioner,
    installChartEvents,
    teardownChartEvents,
  }
}
