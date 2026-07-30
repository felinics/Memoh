import type { EChartsType } from 'echarts/core'
import { MEMORY_GRAPH_SERIES_ID } from './constants'
import type { ChartNodeData, ChartTheme, GraphEdgeCandidate, GraphNode, PinnedNodePosition } from './types'
import { neighborIdsFor } from './graph-data'
import { buildGraphLinks } from './graph-links'
import {
  formatNodeTooltipHtml,
  isTechnicalSlug,
  labelOpacityForZoom,
  nodeCaption,
  shouldShowZoomLabel,
  truncateLabel,
} from './graph-labels'
import { graphNodeSize, particleItemStyle, subjectAccent } from './graph-nodes'

export interface MemoryGraphOptionInput {
  theme: ChartTheme
  nodes: Array<GraphNode & { id: string }>
  edges: GraphEdgeCandidate[]
  hoveredNodeId: string | null
  graphZoom: number
  layoutReady: boolean
  nodePositions: Map<string, PinnedNodePosition>
  viewZoom: number
  viewCenter: [number, number] | null
  tooltipPosition?: (
    point: [number, number],
    params: { dataType?: string, dataIndex?: number, seriesIndex?: number },
    dom: unknown,
    rect: unknown,
    size: { contentSize: [number, number], viewSize: [number, number] },
  ) => [number, number]
}

export function buildMemoryGraphOption(input: MemoryGraphOptionInput) {
  const {
    theme,
    nodes,
    edges,
    hoveredNodeId: hovered,
    graphZoom,
    layoutReady,
    nodePositions,
    viewZoom,
    viewCenter,
    tooltipPosition,
  } = input

  const neighbors = hovered ? neighborIdsFor(edges, hovered) : null
  const zoomLabelOpacity = labelOpacityForZoom(graphZoom)

  return {
    animation: false,
    animationDuration: 0,
    animationDurationUpdate: 0,
    tooltip: {
      trigger: 'item' as const,
      backgroundColor: theme.tooltip,
      borderWidth: 0,
      padding: [4, 8],
      transitionDuration: 0,
      confine: true,
      position: tooltipPosition,
      textStyle: {
        color: theme.tooltipForeground,
        fontSize: 12,
        fontFamily: theme.fontFamily,
        fontWeight: 500,
      },
      extraCssText: 'border-radius: var(--radius-sm); box-shadow: none;',
      formatter: (params: { dataType?: string, data?: ChartNodeData }) => {
        if (params.dataType === 'edge' || !params.data) return ''
        const text = String(params.data.caption || params.data.memory || params.data.label || '').trim()
        if (!text || isTechnicalSlug(text)) return ''
        return formatNodeTooltipHtml(text)
      },
    },
    series: [{
      id: MEMORY_GRAPH_SERIES_ID,
      type: 'graph' as const,
      layout: layoutReady ? 'none' as const : 'force' as const,
      roam: true,
      draggable: false,
      nodeScaleRatio: 1,
      zoom: viewZoom,
      ...(viewCenter ? { center: viewCenter } : {}),
      cursor: 'default',
      hoverAnimation: false,
      force: {
        initLayout: 'circular' as const,
        repulsion: 200,
        edgeLength: [60, 160] as [number, number],
        gravity: 0.08,
        layoutAnimation: false,
      },
      lineStyle: {
        color: theme.line,
        width: 1,
        curveness: 0,
      },
      emphasis: { focus: 'none' as const, scale: false },
      data: nodes.map((node) => {
        const accent = subjectAccent(node.subject || node.slug || node.topic, theme)
        const caption = nodeCaption(node)
        const isHovered = hovered === node.id
        const isNeighbor = !!neighbors?.has(node.id)
        const isDimmed = layoutReady && !!hovered && !isHovered && !isNeighbor
        const showZoomLabel = shouldShowZoomLabel(!!hovered, graphZoom)
        const pos = nodePositions.get(node.id)

        return {
          ...node,
          name: node.id,
          caption,
          symbol: 'circle' as const,
          symbolSize: graphNodeSize(node.count),
          itemStyle: {
            ...particleItemStyle(accent),
            opacity: isDimmed ? 0.22 : 1,
          },
          ...(layoutReady && pos
            ? { x: pos.x, y: pos.y, fixed: true as const }
            : {}),
          label: {
            show: showZoomLabel,
            opacity: zoomLabelOpacity,
            position: 'bottom' as const,
            distance: 6,
            fontSize: 11,
            fontFamily: theme.fontFamily,
            color: theme.label,
            formatter: () => {
              if (!caption || isTechnicalSlug(caption)) return ''
              return truncateLabel(caption)
            },
          },
          tooltip: {
            formatter: () => formatNodeTooltipHtml(caption),
          },
        } satisfies ChartNodeData
      }),
      links: buildGraphLinks(edges, theme, hovered, layoutReady),
    }],
  }
}

export function readSeriesZoom(chart: EChartsType): number {
  try {
    const seriesModel = chart.getModel().getSeries().find(series => (
      series.id === MEMORY_GRAPH_SERIES_ID || series.subType === 'graph'
    )) ?? chart.getModel().getSeriesByIndex(0)
    const coord = seriesModel?.coordinateSystem as { getZoom?: () => number } | undefined
    const fromCoord = coord?.getZoom?.()
    if (typeof fromCoord === 'number' && Number.isFinite(fromCoord) && fromCoord > 0) return fromCoord
    const fromOption = seriesModel?.get('zoom')
    if (typeof fromOption === 'number' && Number.isFinite(fromOption) && fromOption > 0) return fromOption
  }
  catch {
    // fall through
  }
  return 1
}

export function readSeriesCenter(chart: EChartsType): [number, number] | null {
  try {
    const seriesModel = chart.getModel().getSeries().find(series => (
      series.id === MEMORY_GRAPH_SERIES_ID || series.subType === 'graph'
    )) ?? chart.getModel().getSeriesByIndex(0)
    const center = seriesModel?.get('center')
    if (Array.isArray(center) && center.length >= 2) {
      const x = Number(center[0])
      const y = Number(center[1])
      if (Number.isFinite(x) && Number.isFinite(y)) return [x, y]
    }
  }
  catch {
    // fall through
  }
  return null
}

export function nodeDataIndex(chart: EChartsType, nodeId: string): number | null {
  try {
    const seriesModel = chart.getModel().getSeries().find(series => (
      series.id === MEMORY_GRAPH_SERIES_ID || series.subType === 'graph'
    )) ?? chart.getModel().getSeriesByIndex(0)
    const data = seriesModel?.getData()
    if (!data) return null
    let found: number | null = null
    data.each((idx: number) => {
      if (found != null) return
      const raw = data.getRawDataItem(idx) as { id?: string, name?: string }
      if (String(raw?.id ?? raw?.name ?? '') === nodeId) found = idx
    })
    return found
  }
  catch {
    return null
  }
}

export function tooltipPositionForNode(
  chart: EChartsType | undefined | null,
  params: { dataType?: string, dataIndex?: number, seriesIndex?: number },
  size: { contentSize: [number, number], viewSize: [number, number] },
): [number, number] | null {
  if (params.dataType !== 'node' || params.dataIndex == null || !chart) return null

  try {
    const seriesModel = chart.getModel().getSeries().find(series => (
      series.id === MEMORY_GRAPH_SERIES_ID || series.subType === 'graph'
    )) ?? chart.getModel().getSeriesByIndex(params.seriesIndex ?? 0)
    const layout = seriesModel?.getData().getItemLayout(params.dataIndex) as [number, number] | undefined
    if (!layout || !Number.isFinite(layout[0]) || !Number.isFinite(layout[1])) return null

    const pixel = chart.convertToPixel(
      { seriesIndex: params.seriesIndex ?? 0 },
      layout,
    ) as [number, number] | undefined
    if (!pixel || !Number.isFinite(pixel[0]) || !Number.isFinite(pixel[1])) return null

    const [tipW, tipH] = size.contentSize
    const [viewW, viewH] = size.viewSize
    const nodeRadius = 20
    let x = pixel[0] - tipW / 2
    let y = pixel[1] + nodeRadius + 8

    x = Math.max(4, Math.min(x, viewW - tipW - 4))
    y = Math.max(4, Math.min(y, viewH - tipH - 4))

    return [x, y]
  }
  catch {
    return null
  }
}

export function isChartNodeData(data: unknown): data is ChartNodeData {
  return typeof data === 'object' && data !== null && 'id' in data && 'name' in data
}
