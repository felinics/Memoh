import type { HandlersGraphResponse } from '@memohai/sdk'
import { MAX_VISIBLE_EDGES_PER_NODE } from './constants'
import type { GraphData, GraphEdge, GraphEdgeCandidate } from './types'

export function graphRelWeight(rel: string | undefined): number {
  switch (rel) {
    case 'refs': return 1.2
    case 'same_profile': return 1
    case 'same_topic': return 0.8
    case 'same_day': return 0.5
    default: return 0.4
  }
}

export function graphRelRank(rel: string | undefined): number {
  switch (rel) {
    case 'refs': return 0
    case 'same_profile': return 1
    case 'same_topic': return 2
    case 'same_day': return 3
    default: return 100
  }
}

export function graphEdgeStrength(edge: GraphEdge): number {
  const weight = Number(edge.weight)
  if (Number.isFinite(weight) && weight > 0) return weight
  const count = Number(edge.count)
  if (Number.isFinite(count) && count > 0) return count
  return graphRelWeight(edge.rel)
}

export function compareGraphEdges(a: GraphEdgeCandidate, b: GraphEdgeCandidate): number {
  if (a.strength !== b.strength) return b.strength - a.strength
  const relRank = graphRelRank(a.rel) - graphRelRank(b.rel)
  if (relRank !== 0) return relRank
  if (a.source !== b.source) return a.source < b.source ? -1 : 1
  if (a.target !== b.target) return a.target < b.target ? -1 : 1
  return 0
}

export function toGraphEdgeCandidates(edges: GraphEdge[]): GraphEdgeCandidate[] {
  return edges
    .filter((edge): edge is GraphEdge & { source: string, target: string } => !!edge.source && !!edge.target)
    .map(edge => ({ ...edge, strength: graphEdgeStrength(edge) }))
}

export function selectVisibleGraphEdges(
  edges: GraphEdgeCandidate[],
  maxPerNode = MAX_VISIBLE_EDGES_PER_NODE,
): GraphEdgeCandidate[] {
  const degree = new Map<string, number>()
  const selected: GraphEdgeCandidate[] = []

  for (const edge of [...edges].sort(compareGraphEdges)) {
    const sourceDegree = degree.get(edge.source) ?? 0
    const targetDegree = degree.get(edge.target) ?? 0
    if (sourceDegree >= maxPerNode || targetDegree >= maxPerNode) continue
    selected.push(edge)
    degree.set(edge.source, sourceDegree + 1)
    degree.set(edge.target, targetDegree + 1)
  }

  return selected.sort((a, b) => {
    if (a.source !== b.source) return a.source < b.source ? -1 : 1
    if (a.target !== b.target) return a.target < b.target ? -1 : 1
    return graphRelRank(a.rel) - graphRelRank(b.rel)
  })
}

export function neighborIdsFor(edges: GraphEdgeCandidate[], nodeId: string): Set<string> {
  const neighbors = new Set<string>()
  for (const edge of edges) {
    if (edge.source === nodeId) neighbors.add(edge.target)
    else if (edge.target === nodeId) neighbors.add(edge.source)
  }
  return neighbors
}

export function normalizeGraph(data: HandlersGraphResponse | undefined): GraphData {
  return {
    nodes: data?.nodes ?? [],
    edges: data?.edges ?? [],
  }
}

export function positionsFromLayouts(
  layouts: Array<{ id: string, x: number, y: number }>,
): Map<string, { x: number, y: number }> {
  const positions = new Map<string, { x: number, y: number }>()
  for (const { id, x, y } of layouts) {
    if (id && Number.isFinite(x) && Number.isFinite(y)) {
      positions.set(id, { x, y })
    }
  }
  return positions
}

export function isLayoutCaptureReady(readyCount: number, totalNodes: number, minRatio = 0.8): boolean {
  if (totalNodes === 0) return false
  return readyCount >= Math.ceil(totalNodes * minRatio)
}
