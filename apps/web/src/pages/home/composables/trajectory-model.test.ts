import { describe, expect, it } from 'vitest'
import type { CompactionLog, HandlersContextLifecycleTurn } from '@memohai/sdk'
import type { ChatAssistantTurn, ChatMessage, ChatUserTurn, ToolCallBlock } from '@/store/chat/types'
import {
  buildRowMap,
  buildTrajectoryRows,
  previousLifecycleByRun,
  contextEntries,
  createTrajectoryRowBuilder,
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
      { first_message_id: 0, last_message_id: 1, step_index: 0, started_at_ms: 1_000, first_token_at_ms: 1_200, ended_at_ms: 1_500, finish_reason: 'tool-calls', usage: { input_tokens: 100, cached_input_tokens: 80, output_tokens: 10 } },
      { first_message_id: 2, last_message_id: 2, step_index: 1, started_at_ms: 2_300, first_token_at_ms: 2_400, ended_at_ms: 3_400, finish_reason: 'stop', usage: { input_tokens: 130, cached_input_tokens: 100, output_tokens: 50 } },
    ],
  }
}

const lifecycleTurn: HandlersContextLifecycleTurn = {
  run_id: 'run-1',
  turn_id: 'turn-1',
  created_at: '2026-09-03T00:00:01.000Z',
  snapshot: {
    version: 2,
    counts: { fragments: 9, token_estimate: 4_200 },
    breakdown: [
      { kind: 'system_prompt', fragments: 1, token_estimate: 1_300 },
      { kind: 'bot_identity', fragments: 1, token_estimate: 200 },
      { kind: 'workspace_instruction', fragments: 1, token_estimate: 500 },
      { kind: 'tool_usage', fragments: 2, token_estimate: 180 },
      { kind: 'skills_catalog', fragments: 1, token_estimate: 220 },
      { kind: 'memory_recall', fragments: 1, token_estimate: 90 },
      { kind: 'conversation_event', fragments: 22, token_estimate: 84 },
      { kind: 'current_user_message', fragments: 1, token_estimate: 20 },
    ],
    fragments: [
      { kind: 'system_prompt', slot: 'system', content_hash: 'c-sys', text_hash: 'h-sys', token_estimate: 1_300, text_bytes: 5_200 },
      { kind: 'bot_identity', slot: 'system', content_hash: 'c-bot', text_hash: 'h-bot', token_estimate: 200, text_bytes: 800 },
      { kind: 'workspace_instruction', slot: 'system', content_hash: 'c-rules', text_hash: 'h-rules', token_estimate: 500, text_bytes: 2_000 },
    ],
    tool_defs: [
      { provider: 'workspace', name: 'exec', bytes: 800, token_estimate: 200, content_hash: 'h-exec' },
      { provider: 'workspace', name: 'write', bytes: 600, token_estimate: 150 },
      { provider: 'memory', name: 'search_memory', bytes: 400, token_estimate: 100 },
    ],
    memory_recall: { provider_id: 'mem-1', cache_state: 'miss', query: { source: 'recent', recent_messages: 4 }, result: { count: 3, refs: ['m1', 'm2', 'm3'], context_bytes: 360 } },
    selection: { selected: 22, dropped: 3, drop_reasons: { budget: 3 }, drop_reason_tokens: { budget: 1_200 } },
    mutations: [{ kind: 'mid_task_prune', detail: 'pruned=2' }],
    steps: [
      { step_index: 0 },
      { step_index: 1, dropped: 2, reselection_applied: true, reselection_outcome: 'applied', drop_reasons: { budget: 2 } },
    ],
    run_trace: { steps: 2, tool_calls: 1, llm_ms: 1_600, tool_ms: 600 },
  },
}

describe('context entries', () => {
  it('splits the manifest into the system prompt and one entry per injected kind', () => {
    const entries = contextEntries(lifecycleTurn.snapshot!)
    expect(entries.system).toEqual({ fragments: 2, tokens: 1_500, refs: [
      { id: '', kind: 'system_prompt', textHash: 'h-sys', tokens: 1_300, bytes: 5_200 },
      { id: '', kind: 'bot_identity', textHash: 'h-bot', tokens: 200, bytes: 800 },
    ] })
    const rules = entries.before[0]!
    expect(rules.kind === 'fragments' && rules.refs.map(ref => ref.textHash)).toEqual(['h-rules'])
    expect(entries.before.map(entry => entry.kind === 'fragments' ? entry.fragmentKind : entry.kind)).toEqual([
      'workspace_instruction', 'tool_usage', 'skills_catalog', 'memory_recall', 'conversation_event', 'tool_defs', 'selection', 'mutation',
    ])
    const memory = entries.before[3]!
    expect(memory.kind === 'fragments' && memory.memory?.result?.count).toBe(3)
    const history = entries.before[4]!
    expect(history.kind === 'fragments' && history.selection?.dropped).toBe(3)
    const tools = entries.before[5]!
    expect(tools.kind === 'tool_defs' && tools.tools).toBe(3)
    expect(tools.kind === 'tool_defs' && tools.tokens).toBe(450)
    expect(tools.kind === 'tool_defs' && tools.providers).toEqual(['memory', 'workspace'])
    expect(tools.kind === 'tool_defs' && tools.refs.map(ref => `${ref.id}:${ref.textHash}`)).toEqual(['workspace/exec:h-exec', 'workspace/write:', 'memory/search_memory:'])
    expect([...entries.perStep.keys()]).toEqual([1])
    const step = entries.perStep.get(1)![0]!
    expect(step.kind === 'step' && step.step.dropped).toBe(2)
  })

  it('reports a recall that injected nothing and skips a clean selection', () => {
    const entries = contextEntries({
      breakdown: [{ kind: 'system_prompt', fragments: 1, token_estimate: 100 }, { kind: 'current_user_message', fragments: 1, token_estimate: 5 }],
      memory_recall: { provider_id: 'mem-1', cache_state: 'hit', result: { count: 0 } },
      selection: { selected: 4, dropped: 0 },
      steps: [{ step_index: 0 }],
    })
    expect(entries.before.map(entry => entry.kind)).toEqual(['memory_recall'])
    expect(entries.perStep.size).toBe(0)
    expect(contextEntries(undefined).system).toBeNull()
  })
})

describe('trajectory rows', () => {
  it('lays out system, user, context, step context, reasoning, tool and assistant rows in transcript order', () => {
    const messages: ChatMessage[] = [user('user-1', 'list the temp dir'), assistantTurn(), user('user-2', '<message>hurry</message>', { contextInjection: { kind: 'steering' }, turnPosition: undefined })]
    const rows = buildTrajectoryRows(messages, lifecycleByTurnId([lifecycleTurn]))

    expect(rows.map(row => row.kind)).toEqual([
      'system', 'user',
      'context', 'context', 'context', 'context', 'context', 'context', 'context', 'context',
      'reasoning', 'tool',
      'context',
      'assistant',
      'context',
    ])
    expect(rows.map(row => row.turnStart)).toEqual([true, ...Array.from({ length: rows.length - 1 }, () => false)])
    expect(rows[0]!.turnLabel).toBe('7')
    expect(rows[0]!.detail.kind).toBe('system')
    expect(rows[0]!.detail.kind === 'system' && rows[0]!.detail.entry.tokens).toBe(1_500)
    expect(rows[2]!.label).toBe('workspace_instruction')
    expect(rows[2]!.detail.kind === 'context' && rows[2]!.detail.entry.kind).toBe('fragments')
    expect(rows[10]!.stepIndex).toBe(0)
    expect(rows[11]!.stepIndex).toBe(0)
    expect(rows[11]!.label).toBe('exec')
    expect(rows[11]!.preview).toBe('{"command":"ls -la /tmp"}')
    expect(rows[11]!.output).toBe('total 0')
    expect(rows[11]!.startedAtMs).toBe(1_600)
    expect(rows[11]!.endedAtMs).toBe(2_200)
    expect(rows[12]!.stepIndex).toBe(1)
    expect(rows[12]!.label).toBe('step')
    expect(rows[13]!.stepIndex).toBe(1)
    expect(rows[13]!.preview).toBe('All done. Second line.')
    expect(rows[13]!.startedAtMs).toBe(2_300)
    expect(rows[14]!.label).toBe('steering')
    expect(new Set(rows.map(row => row.key)).size).toBe(rows.length)
  })

  it('emits the system and context rows once when only the assistant turn is loaded', () => {
    const rows = buildTrajectoryRows([assistantTurn()], lifecycleByTurnId([lifecycleTurn]))
    expect(rows[0]!.kind).toBe('system')
    expect(rows[0]!.turnStart).toBe(true)
    expect(rows.filter(row => row.kind === 'system')).toHaveLength(1)
    expect(rows.filter(row => row.kind === 'context')).toHaveLength(9)
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

  it('maps blocks to the step whose anchor range contains them', () => {
    const traces = assistantTurn().stepTraces!
    expect(stepIndexForBlock(traces, 0)).toBe(0)
    expect(stepIndexForBlock(traces, 1)).toBe(0)
    expect(stepIndexForBlock(traces, 2)).toBe(1)
    expect(stepIndexForBlock(traces, 9)).toBeNull()
    expect(stepIndexForBlock(undefined, 0)).toBeNull()
  })

  it('leaves blocks streamed after the last finished request untimed', () => {
    const turn = assistantTurn()
    turn.messages = [...turn.messages, { id: 3, type: 'text', content: 'still streaming' }]
    const rows = buildTrajectoryRows([turn], new Map())
    const streaming = rows[rows.length - 1]!
    expect(streaming.preview).toBe('still streaming')
    expect(streaming.stepIndex).toBeNull()
    expect(streaming.startedAtMs).toBeNull()
  })

  it('continues turn numbering for live turns that carry no position yet', () => {
    const live = assistantTurn()
    live.id = 'runtime:turn-2:assistant'
    live.turnId = 'turn-2'
    live.turnPosition = undefined
    const rows = buildTrajectoryRows([user('user-1', 'hi'), assistantTurn(), user('user-2', 'next', { turnId: 'turn-2', turnPosition: undefined }), live], new Map())
    expect(rows.filter(row => row.turnStart).map(row => row.turnLabel)).toEqual(['7', '8'])
  })

  it('reuses rows of unchanged turns across rebuilds', () => {
    const build = createTrajectoryRowBuilder()
    const settled = assistantTurn()
    const first = build([user('user-1', 'hi'), settled], new Map())
    const second = build([user('user-1', 'hi'), settled], new Map())
    expect(second[1]).toBe(first[1])
    settled.messages = [...settled.messages, { id: 3, type: 'text', content: 'appended' }]
    const third = build([user('user-1', 'hi'), settled], new Map())
    expect(third[1]).not.toBe(first[1])
    expect(third).toHaveLength(first.length + 1)
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

describe('row map', () => {
  it('projects every ledger row onto its lane and merges a step into one model segment', () => {
    const rows = buildTrajectoryRows([user('user-1', 'hi'), assistantTurn()], lifecycleByTurnId([lifecycleTurn]))
    const segments = buildRowMap(rows)
    expect(segments.map(segment => segment.lane)).toEqual([
      'input', 'input',
      'input', 'input', 'input', 'input', 'input', 'input', 'input', 'input',
      'model', 'tools',
      'input',
      'model',
    ])
    expect(segments[0]!.kind).toBe('system')
    expect(segments[0]!.turnStart).toBe(true)
    expect(segments[0]!.durationMs).toBe(0)
    const step0 = segments[10]!
    expect(step0.kind).toBe('reasoning')
    expect(step0.rowKey).toBe(rows[10]!.key)
    expect(step0.durationMs).toBe(500)
    expect(step0.splitMs).toBe(200)
    expect(step0.stepIndex).toBe(0)
    expect(segments[11]!.durationMs).toBe(600)
    expect(segments[11]!.label).toBe('exec')
    expect(segments[13]!.durationMs).toBe(1_100)
    expect(segments[13]!.label).toBe('stop')
  })

  it('merges reasoning and text of the same step and keeps untimed tail blocks apart', () => {
    const turn = assistantTurn()
    turn.streaming = true
    turn.messages = [
      { id: 0, type: 'reasoning', content: 'think' },
      { id: 1, type: 'text', content: 'partial' },
      { id: 2, type: 'text', content: 'tail' },
    ]
    turn.stepTraces = [{ first_message_id: 0, last_message_id: 1, step_index: 0, started_at_ms: 1_000, first_token_at_ms: 1_100, ended_at_ms: 1_500, finish_reason: 'stop' }]
    const segments = buildRowMap(buildTrajectoryRows([turn], new Map()))
    expect(segments).toHaveLength(2)
    expect(segments[0]!.durationMs).toBe(500)
    expect(segments[0]!.running).toBe(false)
    expect(segments[1]!.durationMs).toBe(0)
    expect(segments[1]!.running).toBe(true)
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

  it('takes TTFT from the lowest-indexed request even when traces arrive unordered', () => {
    const turn = assistantTurn()
    turn.stepTraces = [turn.stepTraces![1]!, turn.stepTraces![0]!]
    const stats = foldTrajectoryStats([turn], new Map())
    expect(stats.ttftAvgMs).toBe(200)
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

describe('previousLifecycleByRun', () => {
  it('names the run before each run and marks the oldest as first or unknown', () => {
    const turn = (runId: string): HandlersContextLifecycleTurn => ({ run_id: runId, created_at: '2026-09-03T00:00:00.000Z', snapshot: {} })
    const turns = [turn('r3'), turn('r2'), turn('r1')]
    const complete = previousLifecycleByRun(turns, false)
    expect(complete.get('r3')?.run_id).toBe('r2')
    expect(complete.get('r2')?.run_id).toBe('r1')
    expect(complete.get('r1')).toBeNull()
    expect(previousLifecycleByRun(turns, true).get('r1')).toBeUndefined()
  })

  it('reaches the system row so the inspector can compare prompts', () => {
    const older: HandlersContextLifecycleTurn = { ...lifecycleTurn, run_id: 'run-0', turn_id: 'turn-0' }
    const previous = previousLifecycleByRun([lifecycleTurn, older], false)
    const rows = buildTrajectoryRows([user('user-1', 'hi'), assistantTurn()], lifecycleByTurnId([lifecycleTurn, older]), previous)
    const system = rows[0]!
    expect(system.detail.kind === 'system' && system.detail.previous?.run_id).toBe('run-0')
  })
})

describe('compaction rows', () => {
  const compaction = (id: string, startedAt: string, extra: Partial<CompactionLog> = {}): CompactionLog => ({
    id, status: 'ok', summary: 'Earlier the user set up the workspace and listed files.', message_count: 12,
    started_at: startedAt, completed_at: new Date(Date.parse(startedAt) + 9_000).toISOString(), ...extra,
  })

  it('places each compaction after the turn it followed and skips ones older than the window', () => {
    const messages: ChatMessage[] = [
      user('user-1', 'first', { timestamp: '2026-09-03T00:00:00.000Z', turnId: 'turn-1', turnPosition: 1 }),
      assistantTurn(),
      user('user-2', 'second', { timestamp: '2026-09-03T00:10:00.000Z', turnId: 'turn-2', turnPosition: 2 }),
    ]
    const rows = buildTrajectoryRows(messages, new Map(), undefined, [
      compaction('c-old', '2026-09-02T23:00:00.000Z'),
      compaction('c-between', '2026-09-03T00:05:00.000Z'),
      compaction('c-tail', '2026-09-03T00:20:00.000Z', { status: 'pending', completed_at: undefined }),
    ])
    const kinds = rows.map(row => `${row.kind}:${row.kind === 'compaction' ? row.key : row.turnId}`)
    expect(kinds.filter(kind => kind.startsWith('compaction'))).toEqual(['compaction:compaction:c-between', 'compaction:compaction:c-tail'])
    const between = rows.findIndex(row => row.key === 'compaction:c-between')
    expect(rows[between - 1]!.turnId).toBe('turn-1')
    expect(rows[between + 1]!.turnId).toBe('turn-2')
    expect(rows[between]!.turnId).toBe('turn-1')
    expect(rows[between]!.turnStart).toBe(false)
    expect(rows[between]!.endedAtMs! - rows[between]!.startedAtMs!).toBe(9_000)
    expect(rows[between]!.preview).toContain('Earlier the user')
    const tail = rows[rows.length - 1]!
    expect(tail.key).toBe('compaction:c-tail')
    expect(tail.running).toBe(true)
    expect(tail.endedAtMs).toBeNull()
  })

  it('shows on the model lane of the row map with its own wall time', () => {
    const rows = buildTrajectoryRows([user('user-1', 'first', { timestamp: '2026-09-03T00:00:00.000Z' })], new Map(), undefined, [compaction('c-1', '2026-09-03T00:05:00.000Z')])
    const segments = buildRowMap(rows)
    const segment = segments.find(item => item.rowKey === 'compaction:c-1')!
    expect(segment.lane).toBe('model')
    expect(segment.durationMs).toBe(9_000)
    expect(segment.kind).toBe('compaction')
  })
})

describe('continued turns', () => {
  it('keeps rows apart and lists a step\'s context once when a later run restarts step indexes', () => {
    const turn = assistantTurn()
    turn.messages = [
      { id: 0, type: 'text', content: 'first run, step zero' },
      tool(1, 'exec', { started_at_ms: 1_600, ended_at_ms: 2_200 }),
      { id: 2, type: 'text', content: 'continuation, step zero again' },
      { id: 3, type: 'text', content: 'continuation, step one' },
    ]
    turn.stepTraces = [
      { first_message_id: 0, last_message_id: 1, step_index: 0, started_at_ms: 1_000, ended_at_ms: 1_500 },
      { first_message_id: 2, last_message_id: 2, step_index: 0, started_at_ms: 5_000, ended_at_ms: 5_500 },
      { first_message_id: 3, last_message_id: 3, step_index: 1, started_at_ms: 6_000, ended_at_ms: 6_500 },
    ]
    const rows = buildTrajectoryRows([turn], lifecycleByTurnId([lifecycleTurn]))
    const keys = rows.map(row => row.key)
    expect(new Set(keys).size).toBe(keys.length)
    expect(rows.filter(row => row.detail.kind === 'context' && row.detail.entry.kind === 'step').map(row => row.stepIndex)).toEqual([1])
    expect(rows.filter(row => row.kind === 'assistant').map(row => row.stepIndex)).toEqual([0, 0, 1])
  })
})

describe('parallel tools', () => {
  it('rebuilds a turn when an earlier tool finishes while the last one still runs', () => {
    const running = (id: number): ToolCallBlock => ({ ...tool(id, 'exec'), running: true, result: undefined, output: undefined, execution_timing: undefined })
    const turn = assistantTurn()
    turn.streaming = true
    turn.stepTraces = []
    turn.messages = [running(0), running(1)]
    const build = createTrajectoryRowBuilder()
    expect(build([turn], new Map()).every(row => row.running)).toBe(true)
    turn.messages = [{ ...running(0), running: false, result: 'done', execution_timing: { started_at_ms: 1_000, ended_at_ms: 2_000 } }, running(1)]
    const rows = build([turn], new Map())
    expect(rows[0]!.running).toBe(false)
    expect(rows[0]!.output).toBe('done')
    expect(rows[0]!.endedAtMs).toBe(2_000)
    expect(rows[1]!.running).toBe(true)
  })
})
