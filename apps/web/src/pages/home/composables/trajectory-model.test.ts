import { describe, expect, it } from 'vitest'
import type { HandlersContextLifecycleTurn } from '@memohai/sdk'
import type { ChatAssistantTurn, ChatMessage, ChatUserTurn, ToolCallBlock } from '@/store/chat/types'
import {
  buildTrajectoryRows,
  buildTurnTimeline,
  foldTrajectoryStats,
  lifecycleByTurnId,
  previewText,
  stepIndexForBlock,
  visibleRowRange,
} from './trajectory-model'

function tool(id: number, name: string, timing?: { started_at_ms: number, ended_at_ms: number }, running = false): ToolCallBlock {
  return {
    id,
    type: 'tool',
    name,
    toolName: name,
    tool_call_id: `call-${id}`,
    toolCallId: `call-${id}`,
    input: { command: 'ls -la /tmp' },
    output: running ? undefined : 'total 0',
    result: running ? null : 'total 0',
    running,
    done: !running,
    execution_timing: timing,
  }
}

function user(id: string, text: string, extra: Partial<ChatUserTurn> = {}): ChatUserTurn {
  return { id, role: 'user', text, attachments: [], timestamp: '2026-09-03T00:00:00.000Z', streaming: false, isSelf: true, turnId: 'turn-1', turnPosition: 7, ...extra }
}

function assistantTurn(): ChatAssistantTurn {
  return {
    id: 'assistant-1',
    role: 'assistant',
    turnId: 'turn-1',
    turnPosition: 7,
    timestamp: '2026-09-03T00:00:01.000Z',
    streaming: false,
    messages: [
      { id: 0, type: 'reasoning', content: 'think first' },
      tool(1, 'exec', { started_at_ms: 1_600, ended_at_ms: 2_200 }),
      { id: 2, type: 'text', content: 'All done.\nSecond line.' },
    ],
    stepTraces: [
      { first_message_id: 0, step_index: 0, started_at_ms: 1_000, first_token_at_ms: 1_200, ended_at_ms: 1_500, finish_reason: 'tool-calls', usage: { input_tokens: 100, cached_input_tokens: 80, output_tokens: 10 } },
      { first_message_id: 2, step_index: 1, started_at_ms: 2_300, first_token_at_ms: 2_400, ended_at_ms: 3_400, finish_reason: 'stop', usage: { input_tokens: 130, cached_input_tokens: 100, output_tokens: 50 } },
    ],
  }
}

const lifecycleTurn: HandlersContextLifecycleTurn = {
  run_id: 'run-1',
  turn_id: 'turn-1',
  created_at: '2026-09-03T00:00:01.000Z',
  snapshot: { version: 2, counts: { fragments: 3, token_estimate: 4_200 }, run_trace: { steps: 2, tool_calls: 1, llm_ms: 1_600, tool_ms: 600 } },
}

describe('trajectory rows', () => {
  it('lays out user, system, reasoning, tool and assistant rows in transcript order', () => {
    const messages: ChatMessage[] = [user('user-1', 'list the temp dir'), assistantTurn(), user('user-2', '<message>hurry</message>', { contextInjection: { kind: 'steering' }, turnPosition: undefined })]
    const rows = buildTrajectoryRows(messages, lifecycleByTurnId([lifecycleTurn]))

    expect(rows.map(row => row.kind)).toEqual(['user', 'system', 'reasoning', 'tool', 'assistant', 'context'])
    expect(rows.map(row => row.turnStart)).toEqual([true, false, false, false, false, false])
    expect(rows[0]!.turnLabel).toBe('7')
    expect(rows[1]!.detail.kind).toBe('system')
    expect(rows[2]!.stepIndex).toBe(0)
    expect(rows[3]!.stepIndex).toBe(0)
    expect(rows[3]!.label).toBe('exec')
    expect(rows[3]!.preview).toBe('{"command":"ls -la /tmp"}')
    expect(rows[3]!.output).toBe('total 0')
    expect(rows[3]!.startedAtMs).toBe(1_600)
    expect(rows[3]!.endedAtMs).toBe(2_200)
    expect(rows[4]!.stepIndex).toBe(1)
    expect(rows[4]!.preview).toBe('All done. Second line.')
    expect(rows[4]!.startedAtMs).toBe(2_300)
    expect(rows[5]!.label).toBe('steering')
    expect(new Set(rows.map(row => row.key)).size).toBe(rows.length)
  })

  it('keeps running tools without fabricated timing', () => {
    const turn = assistantTurn()
    turn.messages = [tool(1, 'exec', undefined, true)]
    turn.stepTraces = []
    const rows = buildTrajectoryRows([turn], new Map())
    expect(rows).toHaveLength(1)
    expect(rows[0]!.running).toBe(true)
    expect(rows[0]!.startedAtMs).toBeNull()
    expect(rows[0]!.output).toBeNull()
    expect(rows[0]!.stepIndex).toBeNull()
  })

  it('maps blocks to the step whose anchor precedes them', () => {
    const traces = assistantTurn().stepTraces!
    expect(stepIndexForBlock(traces, 0)).toBe(0)
    expect(stepIndexForBlock(traces, 1)).toBe(0)
    expect(stepIndexForBlock(traces, 2)).toBe(1)
    expect(stepIndexForBlock(traces, 9)).toBe(1)
    expect(stepIndexForBlock(undefined, 0)).toBeNull()
  })
})

describe('trajectory previews', () => {
  it('flattens values to one bounded line', () => {
    expect(previewText('a\n\nb', 100)).toBe('a b')
    expect(previewText({ path: '/x', n: 1 }, 100)).toBe('{"path":"/x","n":1}')
    expect(previewText('x'.repeat(20), 8)).toBe('xxxxxxx…')
    expect(previewText(null, 10)).toBe('')
  })
})

describe('turn timeline', () => {
  it('projects steps onto model and input lanes and tools onto their own lane', () => {
    const timeline = buildTurnTimeline(assistantTurn())!
    expect(timeline.start).toBe(1_000)
    expect(timeline.end).toBe(3_400)
    expect(timeline.steps).toBe(2)
    const lanes = timeline.spans.map(span => span.lane)
    expect(lanes.filter(lane => lane === 'model')).toHaveLength(2)
    expect(lanes.filter(lane => lane === 'input')).toHaveLength(2)
    expect(lanes.filter(lane => lane === 'tools')).toHaveLength(1)
    const model = timeline.spans.find(span => span.lane === 'model')!
    expect(model.start).toBe(1_000)
    expect(model.ttftEnd).toBe(1_200)
    expect(model.end).toBe(1_500)
    const input = timeline.spans.find(span => span.lane === 'input')!
    expect(input.tokens).toBe(100)
    expect(input.cachedTokens).toBe(80)
    const tools = timeline.spans.find(span => span.lane === 'tools')!
    expect(tools.label).toBe('exec')
    expect(tools.stepIndex).toBe(0)
  })

  it('returns null when nothing in the turn was timed', () => {
    const turn = assistantTurn()
    turn.stepTraces = undefined
    turn.messages = [tool(1, 'exec')]
    expect(buildTurnTimeline(turn)).toBeNull()
  })
})

describe('trajectory stats', () => {
  it('folds step traces with DSH throughput rules', () => {
    const stats = foldTrajectoryStats([user('user-1', 'hi'), assistantTurn()], new Map())
    expect(stats.turns).toBe(1)
    expect(stats.steps).toBe(2)
    expect(stats.llmMs).toBe(1_600)
    expect(stats.toolMs).toBe(600)
    expect(stats.ttftAvgMs).toBe(200)
    expect(stats.decodeMs).toBe(1_300)
    expect(stats.decodeTokens).toBe(60)
    expect(stats.inputTokens).toBe(230)
    expect(stats.cachedInputTokens).toBe(180)
    expect(stats.outputTokens).toBe(60)
  })

  it('falls back to the lifecycle run trace for turns without step traces', () => {
    const turn = assistantTurn()
    turn.stepTraces = undefined
    turn.messages = [tool(1, 'exec')]
    const stats = foldTrajectoryStats([turn], lifecycleByTurnId([{
      ...lifecycleTurn,
      snapshot: { version: 2, counts: {}, run_trace: { steps: 3, tool_calls: 1, llm_ms: 900, tool_ms: 400, ttft_ms: 150, decode_ms: 500, decode_output_tokens: 25, input_tokens: 700, cached_input_tokens: 300, output_tokens: 40 } },
    }]))
    expect(stats.steps).toBe(3)
    expect(stats.llmMs).toBe(900)
    expect(stats.toolMs).toBe(400)
    expect(stats.ttftAvgMs).toBe(150)
    expect(stats.decodeTokens).toBe(25)
    expect(stats.inputTokens).toBe(700)
    expect(stats.outputTokens).toBe(40)
  })

  it('omits readings that were never sampled', () => {
    const stats = foldTrajectoryStats([user('user-1', 'hi')], new Map())
    expect(stats.turns).toBe(0)
    expect(stats.ttftAvgMs).toBeNull()
    expect(stats.decodeMs).toBe(0)
  })
})

describe('virtual row range', () => {
  it('bounds the mounted rows to the viewport plus overscan', () => {
    expect(visibleRowRange({ scrollTop: 0, viewportHeight: 100, rowHeight: 28, count: 1_000, overscan: 2 })).toEqual({ start: 0, end: 6, offsetTop: 0, totalHeight: 28_000 })
    expect(visibleRowRange({ scrollTop: 2_800, viewportHeight: 100, rowHeight: 28, count: 1_000, overscan: 2 })).toEqual({ start: 98, end: 106, offsetTop: 2_744, totalHeight: 28_000 })
    expect(visibleRowRange({ scrollTop: 27_990, viewportHeight: 100, rowHeight: 28, count: 1_000, overscan: 2 })).toEqual({ start: 997, end: 1_000, offsetTop: 27_916, totalHeight: 28_000 })
    expect(visibleRowRange({ scrollTop: 0, viewportHeight: 100, rowHeight: 28, count: 0, overscan: 2 })).toEqual({ start: 0, end: 0, offsetTop: 0, totalHeight: 0 })
  })
})
