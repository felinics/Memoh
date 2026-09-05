import { describe, expect, it } from 'vitest'
import type { ContextfragKind, ContextfragLifecycleSnapshot, HandlersContextFragmentPreview } from '@memohai/sdk'
import en from '@/i18n/locales/en.json'
import ja from '@/i18n/locales/ja.json'
import zh from '@/i18n/locales/zh.json'
import type { RowMapSegment, TrajectoryStats } from './trajectory-model'
import { compactionDetailRows, contextPreview, formatDurationMs, fragmentRowPreview, lineDiff, MAX_STRIP_BARS, promptFragmentChanges, rowMapGeometry, statsSegments } from './trajectory-view'

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

describe('promptFragmentChanges', () => {
  it('lists what a run added, rewrote, or dropped against the run before it', () => {
    const previews: Record<string, HandlersContextFragmentPreview> = {
      'h-sys-1': { kind: 'system_prompt', label: 'system.prompt.body', preview: '', text_bytes: 1 },
      'h-sys-2': { kind: 'system_prompt', label: 'system.prompt.body', preview: '', text_bytes: 1 },
      'h-rules': { kind: 'workspace_instruction', label: 'system.workspace_file.AGENTS.md', preview: '', text_bytes: 1 },
      'h-skill': { kind: 'skills_catalog', label: 'system.skill.hooks-setup', preview: '', text_bytes: 1 },
    }
    const previous: ContextfragLifecycleSnapshot = {
      fragments: [{ kind: 'system_prompt', text_hash: 'h-sys-1' }, { kind: 'workspace_instruction', text_hash: 'h-rules' }],
      tool_defs: [{ provider: 'workspace', name: 'exec', content_hash: 't-exec' }, { provider: 'workspace', name: 'write', content_hash: 't-write' }],
    }
    const current: ContextfragLifecycleSnapshot = {
      fragments: [{ kind: 'system_prompt', text_hash: 'h-sys-2' }, { kind: 'workspace_instruction', text_hash: 'h-rules' }, { kind: 'skills_catalog', text_hash: 'h-skill' }],
      tool_defs: [{ provider: 'workspace', name: 'exec', content_hash: 't-exec-2' }],
    }
    const changes = promptFragmentChanges(current, previous, previews)
    expect(changes.map(change => `${change.change}:${change.label}`)).toEqual([
      'changed:system.prompt.body',
      'added:system.skill.hooks-setup',
      'changed:workspace/exec',
      'removed:workspace/write',
    ])
    expect(changes[0]).toMatchObject({ kind: 'system_prompt', currentHash: 'h-sys-2', previousHash: 'h-sys-1' })
    expect(promptFragmentChanges(current, current, previews)).toEqual([])
    expect(promptFragmentChanges({ fragments: [{ kind: 'bot_identity', text_hash: 'h-x' }] }, undefined, null)[0]!.label).toBe('bot_identity#0')
  })
})

describe('lineDiff', () => {
  it('yields an edit script with unchanged runs folded around the changes', () => {
    const before = ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h'].join('\n')
    const after = ['a', 'b', 'c', 'd', 'X', 'f', 'g', 'h', 'i'].join('\n')
    expect(lineDiff(before, after)).toEqual([
      { type: 'skip', count: 2 },
      { type: 'same', text: 'c' },
      { type: 'same', text: 'd' },
      { type: 'remove', text: 'e' },
      { type: 'add', text: 'X' },
      { type: 'same', text: 'f' },
      { type: 'same', text: 'g' },
      { type: 'same', text: 'h' },
      { type: 'add', text: 'i' },
    ])
    expect(lineDiff('same', 'same')).toEqual([{ type: 'same', text: 'same' }])
    expect(lineDiff(Array.from({ length: 401 }, () => 'x').join('\n'), 'y')).toBeNull()
  })
})

describe('compactionDetailRows', () => {
  it('lays out what the compaction replaced, when it ran, and what it cost', () => {
    const t = (key: string) => key.replace('chat.trajectory.', '')
    const clock = (ms: number) => new Date(ms).toISOString()
    const rows = compactionDetailRows({
      status: 'ok', message_count: 12, level: 1,
      anchor_start_ms: Date.parse('2026-09-03T00:00:00.000Z'), anchor_end_ms: Date.parse('2026-09-03T00:04:00.000Z'),
      started_at: '2026-09-03T00:05:00.000Z', completed_at: '2026-09-03T00:05:09.000Z',
      usage: { inputTokens: 4100, outputTokens: 320 },
    }, t, clock)
    expect(rows.map(row => `${row.key}=${row.value}`)).toEqual([
      'status=compactionStatus.ok',
      'messages=12',
      'span=2026-09-03T00:00:00.000Z → 2026-09-03T00:04:00.000Z',
      'level=1',
      'started=2026-09-03T00:05:00.000Z',
      'ended=2026-09-03T00:05:09.000Z',
      'duration=9.0s',
      'input=4.1K',
      'output=320',
    ])
    const failed = compactionDetailRows({ status: 'error', error_message: 'summarizer timed out', started_at: '2026-09-03T00:05:00.000Z' }, t, clock)
    expect(failed[failed.length - 1]).toMatchObject({ key: 'error', value: 'summarizer timed out' })
    expect(failed.some(row => row.key === 'ended')).toBe(false)
  })
})
