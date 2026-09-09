import { describe, expect, it } from 'vitest'
import { mergeContextCapturePages, mergeTrajectoryCaptures, prependContextCapturePage } from './context-trajectory-view'
import type { ContextCapturePage } from './context-trajectory.types'
import { buildRowMap, buildTrajectoryRows, lifecycleByTurnId } from './trajectory-model'

describe('captured context trajectory', () => {
  it('keeps the original head coverage when global IDs are sparse', () => {
    const pages = prependContextCapturePage([{ data: { events: [{ id: '100' }, { id: '50' }], has_more: true, next_cursor: '50' } }], {
      data: { events: [{ id: '110' }, { id: '100' }], has_more: true, next_cursor: '100' },
    })
    expect(mergeContextCapturePages(pages).gapCursor).toBeNull()
    expect(mergeContextCapturePages(pages).events.map(event => event.id)).toEqual(['50', '100', '110'])
  })

  it('retains coverage of empty intervals after event copies are replaced', () => {
    let pages: ContextCapturePage[] = [{ before: '200', data: { events: [{ id: '190' }, { id: '100' }], has_more: true, next_cursor: '100' } }]
    pages = prependContextCapturePage(pages, { before: '200', data: { events: [{ id: '190' }], has_more: true, next_cursor: '190' } })
    pages = prependContextCapturePage(pages, { before: '150', data: { events: [{ id: '100' }], has_more: true, next_cursor: '100' } })
    expect(mergeContextCapturePages(pages).gapCursor).toBeNull()
    expect(mergeContextCapturePages(pages).events.map(event => event.id)).toEqual(['100', '190'])
  })

  it('retains older captures without storing repeated polling copies', () => {
    let pages: ContextCapturePage[] = []
    for (let head = 100; head <= 120; head++) {
      pages = prependContextCapturePage(pages, { data: { events: [head, head - 1, head - 2].map(id => ({ id: String(id) })), has_more: true, next_cursor: String(head - 2) } })
    }
    const all = pages.flatMap(page => page.data.events ?? [])
    expect(all).toHaveLength(23)
    expect(new Set(all.map(event => event.id)).size).toBe(all.length)
    expect(mergeContextCapturePages(pages).events.map(event => event.id)).toEqual(Array.from({ length: 23 }, (_, index) => String(98 + index)))
    expect(mergeContextCapturePages(pages).gapCursor).toBeNull()
  })

  it('keeps readable lifecycle context when captured stages are partial', () => {
    const lifecycles = [{ run_id: 'run', turn_id: 'turn', snapshot: { breakdown: [
      { kind: 'system_prompt' as const, fragments: 1, token_estimate: 10 },
      { kind: 'workspace_instruction' as const, fragments: 1, token_estimate: 5 },
    ] } }]
    const base = buildTrajectoryRows([{
      role: 'user', id: 'user', turnId: 'turn', text: 'task', timestamp: new Date(0).toISOString(), streaming: false, attachments: [], isSelf: true,
    }], lifecycleByTurnId(lifecycles))
    const context = base.filter(row => row.kind === 'context' || row.kind === 'system')
    expect(context.length).toBeGreaterThan(0)
    const merged = mergeTrajectoryCaptures(base, [{ id: '1', run_id: 'run', capture_id: 'a', sequence: 1, stage: 'trigger' }], lifecycles)
    expect(merged.map(row => row.key)).toEqual(expect.arrayContaining(context.map(row => row.key)))
  })

  it('uses transcript timestamps when request timing was never captured', () => {
    const base = buildTrajectoryRows([
      { role: 'user', id: 'u1', turnId: 'old', text: 'old', timestamp: new Date(0).toISOString(), streaming: false, attachments: [], isSelf: true },
      { role: 'assistant', id: 'a1', turnId: 'old', timestamp: new Date(10).toISOString(), streaming: false, messages: [{ id: 1, type: 'text', content: 'old answer' }] },
      { role: 'user', id: 'u2', turnId: 'new', text: 'new', timestamp: new Date(1000).toISOString(), streaming: false, attachments: [], isSelf: true },
      { role: 'assistant', id: 'a2', turnId: 'new', timestamp: new Date(2000).toISOString(), streaming: false, messages: [{ id: 1, type: 'text', content: 'new answer' }] },
    ], new Map())
    const merged = mergeTrajectoryCaptures(base, [{ id: '1', run_id: 'new-run', capture_id: 'a', sequence: 1, stage: 'external_handoff', recorded_at: new Date(1100).toISOString() }], [])
    expect(merged.map(row => row.key)).toEqual([base[0]!.key, base[1]!.key, base[2]!.key, 'capture:1', base[3]!.key])
  })
  it('places the final HTTP body before the first model output', () => {
    const base = buildTrajectoryRows([{
      role: 'assistant', id: 'answer', turnId: 'turn', timestamp: new Date(2000).toISOString(), streaming: false,
      messages: [{ id: 1, type: 'text', content: 'reply' }],
      stepTraces: [{ step_index: 0, first_message_id: 1, last_message_id: 1, started_at_ms: 1000, first_token_at_ms: 1500, ended_at_ms: 2000 }],
    }], new Map())
    const merged = mergeTrajectoryCaptures(base, [
      { id: '1', run_id: 'run', capture_id: 'a', sequence: 1, stage: 'provider_request', recorded_at: new Date(900).toISOString() },
      { id: '2', run_id: 'run', capture_id: 'a', sequence: 2, stage: 'wire_request', recorded_at: new Date(1100).toISOString() },
    ], [{ run_id: 'run', turn_id: 'turn' }])
    expect(merged.map(row => row.key)).toEqual(['capture:1', 'capture:2', base[0]!.key])
  })
  it('shows every run and tool-only request without requiring transcript messages', () => {
    const rows = mergeTrajectoryCaptures([], [
      { id: '1', run_id: 'initial', capture_id: 'a', sequence: 1, stage: 'context_collected', recorded_at: '2026-09-08T00:00:00Z' },
      { id: '2', run_id: 'continued', capture_id: 'b', sequence: 1, stage: 'provider_request', step_index: 0, recorded_at: '2026-09-08T00:00:01Z' },
    ], [{ run_id: 'initial', turn_id: 'turn' }, { run_id: 'continued', turn_id: 'turn' }])
    expect(rows.map(row => row.key)).toEqual(['capture:1', 'capture:2'])
    expect(rows.map(row => row.kind)).toEqual(['context', 'request'])
    expect(buildRowMap(rows).map(row => row.lane)).toEqual(['input', 'model'])
  })

  it('keeps predecessor relationships inside each capture segment', () => {
    const rows = mergeTrajectoryCaptures([], [
      { id: '10', run_id: 'run', capture_id: 'a', sequence: 2, stage: 'before_selection' },
      { id: '11', run_id: 'run', capture_id: 'b', sequence: 1, stage: 'trigger' },
      { id: '12', run_id: 'run', capture_id: 'a', sequence: 3, stage: 'provider_request' },
    ], [])
    expect(rows.map(row => row.detail.kind === 'capture' ? row.detail.previousEventId : null)).toEqual([undefined, null, '10'])
    expect(new Set(rows.map(row => row.turnId)).size).toBe(1)
    expect(rows.every(row => row.turnLabel.length > 0)).toBe(true)
  })

  it('preserves loaded pages and exposes gaps between refreshes', () => {
    const old = { data: { events: [{ id: '7' }, { id: '6' }], next_cursor: '6', has_more: true } }
    const latest = { data: { events: [{ id: '12' }, { id: '11' }], next_cursor: '11', has_more: true } }
    const gap = mergeContextCapturePages([latest, old])
    expect(gap.events.map(event => event.id)).toEqual(['6', '7', '11', '12'])
    expect(gap.gapCursor).toBe('11')
    const joined = mergeContextCapturePages([latest, old, {
      before: '11', data: { events: [{ id: '10' }, { id: '9' }, { id: '8' }, { id: '7' }], next_cursor: '7', has_more: true },
    }])
    expect(joined.gapCursor).toBeNull()
    expect(joined.nextCursor).toBe('6')
    expect(joined.events.map(event => event.id)).toEqual(['6', '7', '8', '9', '10', '11', '12'])
  })

  it('keeps cursor ordering exact above the JavaScript integer range', () => {
    const merged = mergeContextCapturePages([{ data: { events: [{ id: '9007199254740993' }, { id: '9007199254740992' }], has_more: false } }])
    expect(merged.events.map(event => event.id)).toEqual(['9007199254740992', '9007199254740993'])
    expect(merged.nextCursor).toBeNull()
  })
})
