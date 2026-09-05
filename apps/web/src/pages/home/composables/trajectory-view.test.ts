import { describe, expect, it } from 'vitest'
import type { ContextfragKind } from '@memohai/sdk'
import en from '@/i18n/locales/en.json'
import ja from '@/i18n/locales/ja.json'
import zh from '@/i18n/locales/zh.json'
import type { RowMapSegment, TrajectoryStats } from './trajectory-model'
import { contextPreview, formatDurationMs, fragmentRowPreview, MAX_STRIP_BARS, rowMapGeometry, statsSegments } from './trajectory-view'

function segment(overrides: Partial<RowMapSegment>): RowMapSegment {
  return {
    key: 'k', rowKey: 'k', lane: 'input', kind: 'user', turnId: 'turn-1', turnStart: false,
    durationMs: 0, splitMs: null, label: '', running: false, stepIndex: null,
    ...overrides,
  }
}

const segments: RowMapSegment[] = [
  segment({ key: 'system', rowKey: 'system', kind: 'system', turnStart: true }),
  segment({ key: 'user', rowKey: 'user', kind: 'user' }),
  segment({ key: 'model:0', rowKey: 'r0', lane: 'model', kind: 'reasoning', durationMs: 1_000, splitMs: 250, stepIndex: 0, label: 'tool-calls' }),
  segment({ key: 'tool:1', rowKey: 't1', lane: 'tools', kind: 'tool', durationMs: 800, stepIndex: 0, label: 'exec' }),
  segment({ key: 'model:1', rowKey: 'a1', lane: 'model', kind: 'assistant', durationMs: 2_000, stepIndex: 1, label: 'stop' }),
  segment({ key: 'user2', rowKey: 'user2', kind: 'user', turnId: 'turn-2', turnStart: true }),
  segment({ key: 'model:2', rowKey: 'a2', lane: 'model', kind: 'assistant', durationMs: 1_000, stepIndex: 0, turnId: 'turn-2' }),
]

describe('rowMapGeometry', () => {
  it('scales bars by duration with a floor for timeless rows and keeps them in ledger order', () => {
    const bars = rowMapGeometry(segments, 'duration')
    expect(bars.map(bar => bar.key)).toEqual(segments.map(s => s.key))
    let cursor = 0
    for (const bar of bars) {
      expect(bar.leftPct).toBeGreaterThanOrEqual(cursor - 1e-9)
      cursor = bar.leftPct + bar.widthPct
    }
    expect(cursor).toBeCloseTo(100, 6)
    const model0 = bars.find(bar => bar.key === 'model:0')!
    const model1 = bars.find(bar => bar.key === 'model:1')!
    expect(model1.widthPct / model0.widthPct).toBeCloseTo(2, 6)
    expect(model0.splitPct).toBe(25)
    const system = bars.find(bar => bar.key === 'system')!
    expect(system.widthPct).toBeGreaterThan(0)
    expect(system.widthPct).toBeLessThan(model0.widthPct)
    const turn2 = bars.find(bar => bar.key === 'user2')!
    const turn1End = model1.leftPct + model1.widthPct
    expect(turn2.leftPct).toBeGreaterThan(turn1End)
  })

  it('gives every segment the same width in sequence mode', () => {
    const bars = rowMapGeometry(segments, 'sequence')
    const widths = new Set(bars.map(bar => bar.widthPct.toFixed(6)))
    expect(widths.size).toBe(1)
    expect(bars[bars.length - 1]!.leftPct + bars[bars.length - 1]!.widthPct).toBeCloseTo(100, 6)
    expect(bars.find(bar => bar.key === 'model:0')!.splitPct).toBe(25)
  })

  it('keeps every bar visible across hundreds of turns', () => {
    const many: RowMapSegment[] = []
    for (let turn = 0; turn < 170; turn += 1) {
      many.push(segment({ key: `u${turn}`, rowKey: `u${turn}`, turnId: `t${turn}`, turnStart: true }))
    }
    for (const mode of ['duration', 'sequence'] as const) {
      const bars = rowMapGeometry(many, mode)
      expect(bars.length).toBe(170)
      let cursor = 0
      for (const bar of bars) {
        expect(bar.widthPct).toBeGreaterThan(0)
        expect(bar.leftPct).toBeGreaterThanOrEqual(cursor - 1e-9)
        cursor = bar.leftPct + bar.widthPct
      }
      expect(cursor).toBeLessThanOrEqual(100 + 1e-6)
      expect(cursor).toBeGreaterThan(80)
    }
  })

  it('folds a long window into a bounded number of proportional bars that focus their first row', () => {
    const many: RowMapSegment[] = []
    for (let turn = 0; turn < 600; turn += 1) {
      many.push(segment({ key: `u${turn}`, rowKey: `u${turn}`, turnId: `t${turn}`, turnStart: true }))
      many.push(segment({ key: `m${turn}`, rowKey: `m${turn}`, lane: 'model', kind: 'assistant', durationMs: (turn + 1) * 10, turnId: `t${turn}` }))
      many.push(segment({ key: `x${turn}`, rowKey: `x${turn}`, lane: 'tools', kind: 'tool', durationMs: 5, turnId: `t${turn}` }))
    }
    const bars = rowMapGeometry(many, 'duration')
    expect(bars.length).toBeLessThanOrEqual(MAX_STRIP_BARS)
    expect(bars.length).toBeGreaterThan(MAX_STRIP_BARS / 2)
    const model = bars.filter(bar => bar.lane === 'model')
    expect(model[0]!.rowKey).toBe('m0')
    expect(model[0]!.rows).toBeGreaterThan(1)
    expect(model.reduce((sum, bar) => sum + bar.rows, 0)).toBe(600)
    // Bars above the visibility floor keep their proportions: the last group
    // covers about twice the wall time of the middle one.
    const ratio = model[model.length - 1]!.widthPct / model[Math.floor(model.length / 2)]!.widthPct
    expect(ratio).toBeGreaterThan(1.8)
    expect(ratio).toBeLessThan(2.2)
    expect(model[0]!.widthPct).toBeGreaterThan(0)
    let cursor = 0
    for (const bar of bars) {
      expect(bar.widthPct).toBeGreaterThan(0)
      expect(bar.leftPct).toBeGreaterThanOrEqual(cursor - 1e-9)
      cursor = bar.leftPct + bar.widthPct
    }
    expect(cursor).toBeLessThanOrEqual(100 + 1e-6)
    expect(rowMapGeometry(segments, 'duration').every(bar => bar.rows === 1)).toBe(true)
  })

  it('handles an empty map and a map without any timing', () => {
    expect(rowMapGeometry([], 'duration')).toEqual([])
    const bars = rowMapGeometry(segments.slice(0, 2), 'duration')
    expect(bars).toHaveLength(2)
    expect(bars[0]!.widthPct + bars[1]!.widthPct).toBeCloseTo(100, 6)
  })
})

describe('contextPreview', () => {
  const t = (key: string, params?: Record<string, unknown>) => `${key}${params ? JSON.stringify(params) : ''}`

  it('describes each entry kind from its own numbers', () => {
    expect(contextPreview({ kind: 'fragments', fragmentKind: 'workspace_instruction', fragments: 1, tokens: 500, textBytes: 0, images: 0, refs: [] }, t))
      .toBe('chat.trajectory.contextFragments{"fragments":1,"tokens":"500"}')
    expect(contextPreview({ kind: 'fragments', fragmentKind: 'conversation_event', fragments: 22, tokens: 84, textBytes: 0, images: 0, refs: [], selection: { selected: 22, dropped: 3 } }, t))
      .toBe('chat.trajectory.contextHistoryCut{"messages":22,"tokens":"84","dropped":3}')
    expect(contextPreview({ kind: 'fragments', fragmentKind: 'conversation_event', fragments: 6, tokens: 84, textBytes: 0, images: 0, refs: [], selection: { selected: 28, dropped: 0 } }, t))
      .toBe('chat.trajectory.contextHistory{"messages":6,"tokens":"84"}')
    expect(contextPreview({ kind: 'tool_defs', tools: 3, tokens: 450, providers: ['memory', 'workspace'], refs: [] }, t))
      .toBe('chat.trajectory.contextToolDefs{"n":3,"tokens":"450"}')
    expect(contextPreview({ kind: 'memory_recall', memory: { cache_state: 'miss', result: { count: 0 } } }, t))
      .toBe('chat.trajectory.contextMemory{"count":0,"state":"miss"}')
    expect(contextPreview({ kind: 'selection', selection: { selected: 4, dropped: 2, trimmed: 1 } }, t))
      .toBe('chat.trajectory.contextSelection{"selected":4,"dropped":2,"trimmed":1}')
    expect(contextPreview({ kind: 'mutation', mutation: { kind: 'mid_task_prune', detail: 'pruned=2' } }, t)).toBe('pruned=2')
    expect(contextPreview({ kind: 'step', step: { step_index: 1, dropped: 2, truncated: 0, reselection_outcome: 'applied' } }, t))
      .toBe('chat.trajectory.contextStep{"dropped":2,"truncated":0,"outcome":"applied"}')
  })
})

describe('fragmentRowPreview', () => {
  const refs = [
    { id: 'system.prompt.intro', kind: 'system_prompt', textHash: 'h1', tokens: 50, bytes: 200 },
    { id: 'system.prompt.body', kind: 'system_prompt', textHash: 'h2', tokens: 100, bytes: 400 },
  ]

  it('reads like the injected text and counts the fragments after it', () => {
    expect(fragmentRowPreview(refs, { h1: { preview: 'You are Memoh,\nan agent.' }, h2: { preview: 'Rules follow.' } })).toBe('You are Memoh, an agent. (+1)')
    expect(fragmentRowPreview(refs, { h2: { preview: 'Rules follow.' } })).toBe('Rules follow.')
  })

  it('yields nothing when no text was stored so the caller keeps its numbers', () => {
    expect(fragmentRowPreview(refs, {})).toBeNull()
    expect(fragmentRowPreview(refs, null)).toBeNull()
    expect(fragmentRowPreview([], { h1: { preview: 'x' } })).toBeNull()
  })
})

describe('formatDurationMs', () => {
  it('prefers the coarsest readable unit', () => {
    expect(formatDurationMs(850)).toBe('850ms')
    expect(formatDurationMs(1_250)).toBe('1.3s')
    expect(formatDurationMs(33_500)).toBe('33.5s')
    expect(formatDurationMs(95_000)).toBe('1m 35s')
    expect(formatDurationMs(0)).toBe('0ms')
    expect(formatDurationMs(59_960)).toBe('1m 0s')
    expect(formatDurationMs(119_600)).toBe('2m 0s')
    expect(formatDurationMs(3_599_600)).toBe('1h 0m')
    expect(formatDurationMs(5_400_000)).toBe('1h 30m')
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

const EVERY_KIND: ContextfragKind[] = [
  'system_prompt', 'system_policy', 'bot_identity', 'workspace_instruction', 'platform_identity', 'tool_usage',
  'conversation_event', 'current_user_message', 'attachment_ref', 'native_image', 'skills_catalog', 'hook_context',
  'injected_message', 'background_summary', 'runtime_context', 'memory_recall', 'conversation_summary', 'tool_definition',
]

describe('context kind labels', () => {
  it('names every fragment kind in every locale', () => {
    for (const locale of [en, zh, ja]) {
      const labels = (locale as { chat: { trajectory: { contextKind: Record<string, string> } } }).chat.trajectory.contextKind
      for (const kind of EVERY_KIND) expect(labels[kind], kind).toBeTruthy()
    }
  })
})
