import { describe, expect, it } from 'vitest'
import type { TrajectoryStats, TurnTimeline } from './trajectory-model'
import { formatDurationMs, laneGeometry, statsSegments } from './trajectory-view'

const timeline: TurnTimeline = {
  start: 1_000,
  end: 5_000,
  steps: 2,
  spans: [
    { lane: 'model', key: 'model:0', start: 1_000, end: 2_000, ttftEnd: 1_250, label: 'tool-calls', tokens: 10, cachedTokens: 0, stepIndex: 0 },
    { lane: 'input', key: 'input:0', start: 1_000, end: 2_000, ttftEnd: null, label: '', tokens: 100, cachedTokens: 50, stepIndex: 0 },
    { lane: 'tools', key: 'tool:1', start: 2_100, end: 2_900, ttftEnd: null, label: 'exec', tokens: 0, cachedTokens: 0, stepIndex: 0 },
    { lane: 'model', key: 'model:1', start: 3_000, end: 5_000, ttftEnd: null, label: 'stop', tokens: 40, cachedTokens: 0, stepIndex: 1 },
    { lane: 'input', key: 'input:1', start: 3_000, end: 5_000, ttftEnd: null, label: '', tokens: 200, cachedTokens: 150, stepIndex: 1 },
  ],
}

describe('laneGeometry', () => {
  it('scales bars by wall clock in duration mode', () => {
    const bars = laneGeometry(timeline, 'duration')
    const first = bars.find(bar => bar.key === 'model:0')!
    expect(first.leftPct).toBe(0)
    expect(first.widthPct).toBe(25)
    expect(first.splitPct).toBe(25)
    const tool = bars.find(bar => bar.key === 'tool:1')!
    expect(tool.leftPct).toBeCloseTo(27.5)
    expect(tool.widthPct).toBeCloseTo(20)
    const secondInput = bars.find(bar => bar.key === 'input:1')!
    expect(secondInput.intensity).toBe(1)
    expect(bars.find(bar => bar.key === 'input:0')!.intensity).toBe(0.5)
  })

  it('gives every step an equal slot in sequence mode and parks tools in their step', () => {
    const bars = laneGeometry(timeline, 'sequence')
    const first = bars.find(bar => bar.key === 'model:0')!
    const second = bars.find(bar => bar.key === 'model:1')!
    expect(first.leftPct).toBe(0)
    expect(first.widthPct).toBe(50)
    expect(second.leftPct).toBe(50)
    expect(second.widthPct).toBe(50)
    const tool = bars.find(bar => bar.key === 'tool:1')!
    expect(tool.leftPct).toBeGreaterThanOrEqual(0)
    expect(tool.leftPct + tool.widthPct).toBeLessThanOrEqual(50)
  })

  it('never divides by a zero domain', () => {
    const bars = laneGeometry({ ...timeline, start: 1_000, end: 1_000, spans: [{ ...timeline.spans[0]!, end: 1_000, ttftEnd: null }] }, 'duration')
    expect(bars[0]!.widthPct).toBeGreaterThan(0)
    expect(Number.isFinite(bars[0]!.leftPct)).toBe(true)
  })
})

describe('formatDurationMs', () => {
  it('prefers the coarsest readable unit', () => {
    expect(formatDurationMs(850)).toBe('850ms')
    expect(formatDurationMs(1_250)).toBe('1.3s')
    expect(formatDurationMs(33_500)).toBe('33.5s')
    expect(formatDurationMs(95_000)).toBe('1m 35s')
    expect(formatDurationMs(0)).toBe('0ms')
  })
})

describe('statsSegments', () => {
  const stats: TrajectoryStats = {
    turns: 1, steps: 3, toolCalls: 2, llmMs: 33_500, toolMs: 8_400, ttftAvgMs: 2_400, decodeMs: 10_000, decodeTokens: 970,
    inputTokens: 95_200, cachedInputTokens: 57_120, outputTokens: 1_200,
  }

  it('renders every sampled group with derived throughput and hit rate', () => {
    const groups = statsSegments(stats)
    expect(groups).toEqual([
      [{ key: 'statsTurns', params: { n: '1' } }, { key: 'statsSteps', params: { n: '3' } }],
      [{ key: 'statsLlm', params: { s: '33.5s' } }, { key: 'statsTools', params: { s: '8.4s' } }],
      [{ key: 'statsTtft', params: { s: '2.4s' } }, { key: 'statsTokPerSec', params: { n: '97' } }],
      [{ key: 'statsCacheHit', params: { p: '60' } }],
      [{ key: 'statsInput', params: { n: '95.2K' } }, { key: 'statsOutput', params: { n: '1.2K' } }],
    ])
  })

  it('drops readings that were never sampled instead of showing zeros', () => {
    const groups = statsSegments({ ...stats, ttftAvgMs: null, decodeMs: 0, decodeTokens: 0, toolMs: 0, cachedInputTokens: 0, inputTokens: 0, outputTokens: 0 })
    expect(groups).toEqual([
      [{ key: 'statsTurns', params: { n: '1' } }, { key: 'statsSteps', params: { n: '3' } }],
      [{ key: 'statsLlm', params: { s: '33.5s' } }],
    ])
  })
})
