import { describe, expect, it } from 'vitest'
import type {
  RuntimeCurrentRunView,
  UIRuntimeDeltaEvent,
  UIRuntimeSnapshotEvent,
  UIStepTrace,
} from '@/composables/api/useChat.types'
import { createEmptyRuntimeProjection, reduceRuntimeProjection } from './runtime-projection'

const trace: UIStepTrace = {
  first_message_id: 0,
  step_index: 0,
  started_at_ms: 1_000,
  first_token_at_ms: 1_200,
  ended_at_ms: 2_000,
  finish_reason: 'tool-calls',
  usage: { input_tokens: 40, cached_input_tokens: 10, output_tokens: 3 },
}

function runView(overrides: Partial<RuntimeCurrentRunView> = {}): RuntimeCurrentRunView {
  return {
    run_id: 'run-1',
    turn_id: 'turn-1',
    generation: 'generation-1',
    status: 'running',
    started_at: '2026-09-03T08:00:00.000Z',
    updated_at: '2026-09-03T08:00:00.000Z',
    messages: [{ id: 0, type: 'text', content: 'hi' }],
    ...overrides,
  }
}

function snapshot(currentRunView: RuntimeCurrentRunView): UIRuntimeSnapshotEvent {
  return {
    type: 'runtime_snapshot',
    session_id: 'session-1',
    epoch: 'epoch-1',
    seq: 1,
    snapshot: {
      bot_id: 'bot-1',
      session_id: 'session-1',
      epoch: 'epoch-1',
      seq: 1,
      current_run_view: currentRunView,
      updated_at: '2026-09-03T08:00:00.000Z',
    },
  }
}

function delta(seq: number, value: UIRuntimeDeltaEvent['delta']): UIRuntimeDeltaEvent {
  return { type: 'runtime_delta', session_id: 'session-1', epoch: 'epoch-1', seq, delta: value }
}

describe('runtime projection step traces', () => {
  it('projects snapshot step traces onto the live assistant turn', () => {
    const state = reduceRuntimeProjection(createEmptyRuntimeProjection('session-1'), snapshot(runView({ step_traces: [trace] })))
    const assistant = state.transcript.turns.find(turn => turn.role === 'assistant')
    expect(assistant && assistant.role === 'assistant' ? assistant.step_traces : undefined).toEqual([trace])
  })

  it('appends step traces from deltas and drops them on reset', () => {
    let state = reduceRuntimeProjection(createEmptyRuntimeProjection('session-1'), snapshot(runView()))
    state = reduceRuntimeProjection(state, delta(2, { step_trace_appends: [trace] }))
    const second: UIStepTrace = { ...trace, first_message_id: 1, step_index: 1 }
    state = reduceRuntimeProjection(state, delta(3, {
      message_upserts: [{ id: 1, type: 'tool', name: 'exec', input: {}, tool_call_id: 'call-1', running: false, execution_timing: { started_at_ms: 2_100, ended_at_ms: 2_600 } }],
      step_trace_appends: [second],
    }))
    expect(state.currentRunView?.step_traces).toEqual([trace, second])
    const assistant = state.transcript.turns.find(turn => turn.role === 'assistant')
    expect(assistant && assistant.role === 'assistant' ? assistant.step_traces : undefined).toEqual([trace, second])
    const tool = state.currentRunView?.messages.find(message => message.type === 'tool')
    expect(tool && tool.type === 'tool' ? tool.execution_timing : undefined).toEqual({ started_at_ms: 2_100, ended_at_ms: 2_600 })

    state = reduceRuntimeProjection(state, delta(4, { reset_messages: true }))
    expect(state.currentRunView?.step_traces ?? []).toEqual([])
  })
})
