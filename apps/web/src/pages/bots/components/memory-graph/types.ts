import type { HandlersGraphEdge, HandlersGraphNode } from '@memohai/sdk'

export type GraphNode = HandlersGraphNode
export type GraphEdge = HandlersGraphEdge

export interface GraphData {
  nodes: GraphNode[]
  edges: GraphEdge[]
}

export interface AccentPair {
  core: string
  disk: string
}

export interface ChartNodeData extends GraphNode {
  id: string
  name: string
  caption: string
  symbol: 'circle'
  symbolSize: number
  itemStyle: {
    color: {
      type: 'radial'
      x: number
      y: number
      r: number
      colorStops: Array<{ offset: number, color: string }>
    }
    borderWidth: number
    opacity: number
  }
  label: {
    show: boolean
    opacity: number
    position: 'bottom'
    distance: number
    fontSize: number
    fontFamily: string
    color: string
    formatter: () => string
  }
  tooltip: { formatter: () => string }
  x?: number
  y?: number
  fixed?: boolean
}

export interface GraphEdgeCandidate extends GraphEdge {
  source: string
  target: string
  strength: number
}

export interface ChartTheme {
  line: string
  fallback: AccentPair
  fontFamily: string
  palette: AccentPair[]
  tooltip: string
  tooltipForeground: string
  label: string
}

export interface GraphViewport {
  width: number
  height: number
}

export interface PinnedNodePosition {
  x: number
  y: number
}
