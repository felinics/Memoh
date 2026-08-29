import { describe, expect, it } from 'vitest'
import type {
  RuntimeCurrentRunView,
  UIRuntimeDeltaEvent,
  UIRuntimeSnapshotEvent,
} from '@/composables/api/useChat'
import {
  createEmptyRuntimeProjection,
  isRuntimeRunActive,
  reduceRuntimeProjection,
} from './runtime-projection'

function runView(overrides: Partial<RuntimeCurrentRunView> = {}): RuntimeCurrentRunView {
  return {
    run_id: 'run-1',
    turn_id: 'turn-1',
    generation: 'generation-1',
    status: 'running',
    started_at: '2026-07-27T08:00:00.000Z',
    updated_at: '2026-07-27T08:00:00.000Z',
    messages: [],
    request_user_turn: {
      turn_id: 'turn-1',
      role: 'user',
      text: 'hello',
      timestamp: '2026-07-27T08:00:00.000Z',
    },
    ...overrides,
  }
}

function snapshot(
  currentRunView: RuntimeCurrentRunView | null = runView(),
): UIRuntimeSnapshotEvent {
  return {
    type: 'runtime_snapshot',
    session_id: 'session-1',
    epoch: 'epoch-1',
    seq: 4,
    snapshot: {
      bot_id: 'bot-1',
      session_id: 'session-1',
      epoch: 'epoch-1',
      seq: 4,
      current_run_view: currentRunView ?? undefined,
      updated_at: '2026-07-27T08:00:00.000Z',
    },
  }
}

function delta(
  seq: number,
  value: UIRuntimeDeltaEvent['delta'],
): UIRuntimeDeltaEvent {
  return {
    type: 'runtime_delta',
    session_id: 'session-1',
    epoch: 'epoch-1',
    seq,
    delta: value,
  }
}

describe('runtime projection', () => {
  it('keeps a run active while it waits for a decision response', () => {
    expect(isRuntimeRunActive('waiting_decision')).toBe(true)
  })

  it('projects an authoritative snapshot into stable turn identities', () => {
    const state = reduceRuntimeProjection(createEmptyRuntimeProjection(), snapshot(runView({
      messages: [{ id: 0, type: 'text', content: 'hi' }],
    })))

    expect(state).toMatchObject({
      botId: 'bot-1',
      sessionId: 'session-1',
      epoch: 'epoch-1',
      seq: 4,
      transcript: {
        runId: 'run-1',
        turnId: 'turn-1',
        status: 'running',
        streaming: true,
      },
    })
    expect(state.transcript.turns.map(turn => turn.id)).toEqual([
      'runtime:turn-1:user',
      'runtime:turn-1:assistant',
    ])
  })

  it('treats a null message list from an idle runtime snapshot as empty', () => {
    const state = reduceRuntimeProjection(createEmptyRuntimeProjection(), snapshot(runView({
      status: 'completed',
      messages: null as unknown as RuntimeCurrentRunView['messages'],
      request_user_turn: undefined,
    })))

    expect(state.currentRunView?.messages).toEqual([])
    expect(state.transcript.streaming).toBe(false)
  })

  it('projects an edit replacement as the authoritative user and assistant pair', () => {
    const state = reduceRuntimeProjection(createEmptyRuntimeProjection(), snapshot(runView({
      request_user_turn: undefined,
      operation: {
        kind: 'edit',
        replace_from_message_id: 'user-old',
        replacement_user_turn: {
          turn_id: 'turn-1',
          role: 'user',
          text: 'edited prompt',
          timestamp: '2026-07-27T08:00:01.000Z',
        },
      },
    })))

    expect(state.transcript.operation).toMatchObject({
      kind: 'edit',
      replace_from_message_id: 'user-old',
    })
    expect(state.transcript.turns).toEqual([
      expect.objectContaining({ role: 'user', text: 'edited prompt' }),
      expect.objectContaining({ role: 'assistant' }),
    ])
  })

  it('applies ordered text, progress and upsert deltas without mutating the prior state', () => {
    const initial = reduceRuntimeProjection(createEmptyRuntimeProjection(), snapshot(runView({
      messages: [
        { id: 0, type: 'text', content: 'hel' },
        {
          id: 1,
          type: 'tool',
          name: 'exec',
          input: { command: 'pwd' },
          tool_call_id: 'call-1',
          running: true,
        },
      ],
    })))
    const next = reduceRuntimeProjection(initial, delta(5, {
      message_appends: [{ id: 0, type: 'text', content: 'lo' }],
      progress_appends: [{ id: 1, progress: 'queued' }],
      message_upserts: [{
        id: 2,
        type: 'reasoning',
        content: 'checking',
      }],
    }))

    expect(initial.currentRunView?.messages[0]).toMatchObject({ content: 'hel' })
    expect(next.currentRunView?.messages).toEqual([
      { id: 0, type: 'text', content: 'hello' },
      expect.objectContaining({ id: 1, type: 'tool', progress: ['queued'] }),
      { id: 2, type: 'reasoning', content: 'checking' },
    ])
  })

  it('resets messages and projects stable terminal error codes from run patches', () => {
    const initial = reduceRuntimeProjection(createEmptyRuntimeProjection(), snapshot(runView({
      messages: [{ id: 0, type: 'text', content: 'partial' }],
    })))
    const reset = reduceRuntimeProjection(initial, delta(5, { reset_messages: true }))
    const terminal = reduceRuntimeProjection(reset, delta(6, {
      run: {
        run_id: 'run-1',
        status: 'lost',
        error_code: 'agent.response_timeout',
        error: 'The model did not respond in time. Please try again.',
      },
    }))

    expect(reset.currentRunView?.messages).toEqual([])
    expect(terminal.transcript.streaming).toBe(false)
    expect(terminal.transcript.turns[1]).toMatchObject({
      role: 'assistant',
      messages: [{
        id: 0,
        type: 'error',
        code: 'agent.response_timeout',
        content: 'The model did not respond in time. Please try again.',
      }],
    })
  })

  it('projects a durable code-only failure after backend recovery', () => {
    const state = reduceRuntimeProjection(createEmptyRuntimeProjection(), snapshot(runView({
      status: 'errored',
      request_user_turn: undefined,
      error_code: 'agent.response_interrupted',
      error: undefined,
      messages: [],
    })))

    expect(state.transcript.turns).toEqual([
      expect.objectContaining({
        role: 'assistant',
        messages: [{
          id: 0,
          type: 'error',
          code: 'agent.response_interrupted',
          content: '',
        }],
      }),
    ])
  })

  it('keeps one stable tool block when a later upsert changes its local id', () => {
    const initial = reduceRuntimeProjection(createEmptyRuntimeProjection(), snapshot(runView({
      messages: [{
        id: 1,
        type: 'tool',
        name: 'exec',
        input: null,
        tool_call_id: 'call-1',
        running: true,
      }],
    })))
    const next = reduceRuntimeProjection(initial, delta(5, {
      message_upserts: [{
        id: 10,
        type: 'tool',
        name: 'exec',
        input: null,
        tool_call_id: 'call-1',
        running: false,
        approval: {
          approval_id: 'approval-1',
          short_id: 1,
          status: 'pending',
          can_approve: true,
        },
      }],
    }))

    expect(next.currentRunView?.messages).toHaveLength(1)
    expect(next.currentRunView?.messages[0]).toMatchObject({
      id: 1,
      tool_call_id: 'call-1',
      running: false,
      approval: { approval_id: 'approval-1' },
    })
  })

  it('carries background task updates on runtime deltas', () => {
    const initial = reduceRuntimeProjection(createEmptyRuntimeProjection(), snapshot(runView({
      messages: [{
        id: 1,
        type: 'tool',
        name: 'spawn_agent',
        input: {},
        tool_call_id: 'call-1',
        running: true,
      }],
    })))
    const next = reduceRuntimeProjection(initial, delta(5, {
      message_upserts: [{
        id: 1,
        type: 'tool',
        name: 'spawn_agent',
        input: {},
        tool_call_id: 'call-1',
        running: true,
        background_task: {
          task_id: 'task-1',
          status: 'running',
        },
      }],
    }))

    expect(next.transcript.turns[1]).toMatchObject({
      role: 'assistant',
      messages: [{
        type: 'tool',
        background_task: { task_id: 'task-1', status: 'running' },
      }],
    })
  })

  it('clears the live transcript when a snapshot has no current run', () => {
    const active = reduceRuntimeProjection(createEmptyRuntimeProjection(), snapshot())
    const empty = reduceRuntimeProjection(active, snapshot(null))

    expect(empty.currentRunView).toBeNull()
    expect(empty.transcript.turns).toEqual([])
    expect(empty.transcript.streaming).toBe(false)
  })

  it('treats only admitting, running and aborting as active', () => {
    expect(isRuntimeRunActive('admitting')).toBe(true)
    expect(isRuntimeRunActive('running')).toBe(true)
    expect(isRuntimeRunActive('aborting')).toBe(true)
    expect(isRuntimeRunActive('completed')).toBe(false)
    expect(isRuntimeRunActive('lost')).toBe(false)
  })
})

describe('idle settled run projection', () => {
  it('emits no assistant turn for a settled run without streamed content', () => {
    const state = reduceRuntimeProjection(createEmptyRuntimeProjection(), snapshot(runView({
      status: 'completed',
      messages: null as unknown as RuntimeCurrentRunView['messages'],
    })))

    expect(state.transcript.turns.map(turn => turn.role)).toEqual(['user'])
  })

  it('emits an empty slice when the settled run also lacks a user turn', () => {
    const state = reduceRuntimeProjection(createEmptyRuntimeProjection(), snapshot(runView({
      status: 'completed',
      messages: null as unknown as RuntimeCurrentRunView['messages'],
      request_user_turn: undefined,
    })))

    expect(state.transcript.turns).toEqual([])
  })

  it('still projects the assistant turn for an active run without content yet', () => {
    const state = reduceRuntimeProjection(createEmptyRuntimeProjection(), snapshot(runView({
      status: 'running',
      messages: [],
    })))

    expect(state.transcript.turns.map(turn => turn.role)).toEqual(['user', 'assistant'])
  })
})
