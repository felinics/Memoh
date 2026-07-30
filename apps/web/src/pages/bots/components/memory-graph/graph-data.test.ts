import { describe, expect, it } from 'vitest'
import {
  compareGraphEdges,
  graphEdgeStrength,
  graphRelRank,
  graphRelWeight,
  isLayoutCaptureReady,
  neighborIdsFor,
  normalizeGraph,
  positionsFromLayouts,
  selectVisibleGraphEdges,
  toGraphEdgeCandidates,
} from './graph-data'
import type { GraphEdge, GraphEdgeCandidate } from './types'

function edge(over: Partial<GraphEdge> & { source: string, target: string }): GraphEdge {
  return { ...over }
}

function candidate(over: Partial<GraphEdgeCandidate> & { source: string, target: string, strength: number }): GraphEdgeCandidate {
  return { ...over }
}

describe('graphRelWeight', () => {
  it('maps known relation types', () => {
    expect(graphRelWeight('refs')).toBe(1.2)
    expect(graphRelWeight('same_day')).toBe(0.5)
    expect(graphRelWeight(undefined)).toBe(0.4)
  })
})

describe('graphEdgeStrength', () => {
  it('prefers weight then count then relation fallback', () => {
    expect(graphEdgeStrength(edge({ source: 'a', target: 'b', weight: 3 }))).toBe(3)
    expect(graphEdgeStrength(edge({ source: 'a', target: 'b', count: 2 }))).toBe(2)
    expect(graphEdgeStrength(edge({ source: 'a', target: 'b', rel: 'refs' }))).toBe(1.2)
  })
})

describe('selectVisibleGraphEdges', () => {
  it('caps edges per node by strength', () => {
    const edges = [
      candidate({ source: 'a', target: 'b', strength: 5 }),
      candidate({ source: 'a', target: 'c', strength: 4 }),
      candidate({ source: 'a', target: 'd', strength: 3 }),
      candidate({ source: 'a', target: 'e', strength: 2 }),
      candidate({ source: 'a', target: 'f', strength: 1 }),
    ]
    const visible = selectVisibleGraphEdges(edges, 2)
    expect(visible).toHaveLength(2)
    expect(visible.map(e => e.target)).toEqual(['b', 'c'])
  })
})

describe('compareGraphEdges', () => {
  it('sorts by strength descending', () => {
    const a = candidate({ source: 'a', target: 'b', strength: 2 })
    const b = candidate({ source: 'c', target: 'd', strength: 5 })
    expect(compareGraphEdges(a, b)).toBeGreaterThan(0)
  })
})

describe('neighborIdsFor', () => {
  it('collects adjacent node ids', () => {
    const edges = [
      candidate({ source: 'a', target: 'b', strength: 1 }),
      candidate({ source: 'a', target: 'c', strength: 1 }),
      candidate({ source: 'b', target: 'd', strength: 1 }),
    ]
    expect(neighborIdsFor(edges, 'a')).toEqual(new Set(['b', 'c']))
  })
})

describe('normalizeGraph', () => {
  it('defaults missing arrays', () => {
    expect(normalizeGraph(undefined)).toEqual({ nodes: [], edges: [] })
  })
})

describe('positionsFromLayouts', () => {
  it('skips invalid entries', () => {
    const map = positionsFromLayouts([
      { id: 'a', x: 1, y: 2 },
      { id: '', x: 3, y: 4 },
      { id: 'b', x: Number.NaN, y: 1 },
    ])
    expect(map.size).toBe(1)
    expect(map.get('a')).toEqual({ x: 1, y: 2 })
  })
})

describe('isLayoutCaptureReady', () => {
  it('requires enough positioned nodes', () => {
    expect(isLayoutCaptureReady(8, 10, 0.8)).toBe(true)
    expect(isLayoutCaptureReady(7, 10, 0.8)).toBe(false)
  })
})

describe('toGraphEdgeCandidates', () => {
  it('drops edges without endpoints', () => {
    const result = toGraphEdgeCandidates([
      edge({ source: 'a', target: 'b' }),
      edge({ source: '', target: 'c' }),
    ])
    expect(result).toHaveLength(1)
    expect(result[0]?.source).toBe('a')
  })
})

describe('graphRelRank', () => {
  it('ranks refs highest', () => {
    expect(graphRelRank('refs')).toBeLessThan(graphRelRank('same_day'))
  })
})
