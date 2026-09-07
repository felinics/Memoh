import { toRaw } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { createAssistantStreamRegistry } from './assistant-streams'
import { createComposerPairSync } from './composer-pair-sync'
import type { ChatAssistantTurn } from './types'

function assistantTurn(id: string): ChatAssistantTurn {
  return {
    id,
    role: 'assistant',
    messages: [],
    timestamp: '2026-01-01T00:00:00.000Z',
    streaming: true,
    __optimistic: true,
  }
}

function makeRegistry() {
  const finishAssistantTurn = vi.fn((turn: ChatAssistantTurn) => {
    turn.streaming = false
  })
  const registry = createAssistantStreamRegistry({ finishAssistantTurn })
  return { registry, finishAssistantTurn }
}

function track(
  registry: ReturnType<typeof createAssistantStreamRegistry>,
  invocationId: string,
  targetSessionId = 'session-a',
  botId = 'bot-1',
) {
  const turn = assistantTurn(`turn-${invocationId}`)
  const completion = registry.trackAssistantStream({
    invocationId,
    assistantTurn: turn,
    botId,
    sessionId: targetSessionId,
  })
  return { turn, completion }
}

describe('assistant stream registry', () => {
  it('tracks submission promises without deciding session streaming truth', async () => {
    const { registry, finishAssistantTurn } = makeRegistry()
    const { turn, completion } = track(registry, 'invocation-1')

    expect(toRaw(registry.getAssistantStream('invocation-1')!.assistantTurn)).toBe(turn)
    expect(registry.assistantStreamsForSession('bot-1', 'session-a')).toHaveLength(1)

    registry.resolveAssistantStream('invocation-1')
    await completion

    expect(finishAssistantTurn).toHaveBeenCalledOnce()
    expect(turn.streaming).toBe(false)
    expect(registry.getAssistantStream('invocation-1')).toBeUndefined()
  })

  it('rejects blank and concurrently duplicated invocation ids', async () => {
    const { registry } = makeRegistry()
    await expect(track(registry, ' ').completion).rejects.toThrow('invocation_id is required')

    const original = track(registry, 'invocation-1')
    await expect(track(registry, 'invocation-1').completion)
      .rejects.toThrow('invocation_id invocation-1 is already active')

    const failure = new Error('failed')
    registry.rejectAssistantStream('invocation-1', failure)
    await expect(original.completion).rejects.toBe(failure)
  })

  it('allows an invocation id to be retried after its prior submission settles', async () => {
    const { registry } = makeRegistry()
    const first = track(registry, 'invocation-1')
    registry.discardAssistantStream('invocation-1')
    await first.completion

    const retry = track(registry, 'invocation-1')
    registry.resolveAssistantStream('invocation-1')
    await expect(retry.completion).resolves.toBeUndefined()
  })

  it('routes events by accepted run id and keeps deferred abort intent', async () => {
    const { registry } = makeRegistry()
    const entry = track(registry, 'invocation-1')

    expect(registry.requestAbort('invocation-1')).toBe('')
    expect(registry.bindRunId('invocation-1', 'run-1', 'turn-1')).toMatchObject({
      runId: 'run-1',
      abortRequested: true,
    })
    expect(registry.invocationIdForEvent({
      run_id: 'run-1',
      session_id: 'session-a',
    })).toBe('invocation-1')
    expect(registry.requestAbort('invocation-1')).toBe('run-1')

    registry.resolveAssistantStream('invocation-1')
    await entry.completion

    expect(registry.invocationIdForEvent({
      run_id: 'run-1',
      session_id: 'session-a',
    })).toBe('invocation-1')
  })

  it('never guesses that an unknown run belongs to the only local submission', async () => {
    const { registry } = makeRegistry()
    const local = track(registry, 'invocation-local')

    expect(registry.invocationIdForEvent({
      run_id: 'run-from-another-subscriber',
      session_id: 'session-a',
    })).toBe('run-from-another-subscriber')

    registry.resolveAssistantStream('invocation-local')
    await local.completion
  })

  it('binds a deferred draft stream to exactly one created session', async () => {
    const { registry } = makeRegistry()
    const deferred = track(registry, 'invocation-1', '')

    expect(registry.isUnboundComposerStreaming('bot-1')).toBe(true)
    expect(registry.activeUnboundInvocationIds('bot-1')).toEqual(['invocation-1'])
    expect(registry.recordCreatedSession('invocation-1', 'session-created'))
      .toBe('session-created')
    expect(registry.recordCreatedSession('invocation-1', 'conflicting-session'))
      .toBe('session-created')

    registry.resolveAssistantStream('invocation-1')
    await deferred.completion
    expect(registry.createdSessionIdForInvocation('invocation-1')).toBe('session-created')
  })

  it('maps continuation block ids after blocks already in the assistant turn', async () => {
    const { registry } = makeRegistry()
    const turn = assistantTurn('shared-turn')
    turn.messages.push({
      id: 4,
      type: 'tool',
      name: 'ask_user',
      input: {},
      tool_call_id: 'call-ask',
      running: false,
      toolCallId: 'call-ask',
      toolName: 'ask_user',
      result: null,
      done: true,
    })
    const completion = registry.trackAssistantStream({
      invocationId: 'response-invocation',
      assistantTurn: turn,
      botId: 'bot-1',
      sessionId: 'session-a',
    })

    expect(registry.mapAssistantStreamMessage('response-invocation', {
      id: 0,
      type: 'text',
      content: 'Done',
    })).toMatchObject({ id: 5 })

    registry.resolveAssistantStream('response-invocation')
    await completion
  })

  it('rejects every active submission in insertion order', async () => {
    const { registry } = makeRegistry()
    const entries = [
      track(registry, 'invocation-a1'),
      track(registry, 'invocation-b1', 'session-b'),
      track(registry, 'invocation-a2'),
    ]
    const completions = entries.map(entry => entry.completion.catch(error => error))
    const failure = new Error('aborted')
    const beforeReject: string[] = []

    registry.rejectAllStreams(failure, invocationId => beforeReject.push(invocationId))

    expect(beforeReject).toEqual(['invocation-a1', 'invocation-b1', 'invocation-a2'])
    expect(await Promise.all(completions)).toEqual([failure, failure, failure])
  })
})


describe('model preference settlement', () => {
  it('saves a later pick before generation ends, only after the matching write settles', async () => {
    const { registry } = makeRegistry()
    const pair = createComposerPairSync()
    const send = pair.prepareSend()
    let persisted = 'A'
    const write = pair.write(async () => persisted, async () => { persisted = 'B'; return 'B' }, () => {})
    send.begin()
    const notify = vi.fn(() => send.finish(false))
    let completed = false
    const completion = registry.trackAssistantStream({
      invocationId: 'inv', botId: 'bot', sessionId: 'session',
      assistantTurn: assistantTurn('turn'), onModelPreferenceSettled: notify,
    }).then(() => { completed = true })
    registry.bindRunId('inv', 'run', 'turn')
    await Promise.resolve()
    expect(persisted).toBe('A')
    registry.settleModelPreference({ invocation_id: 'inv', run_id: 'other', session_id: 'session' })
    registry.settleModelPreference({ invocation_id: 'inv', run_id: 'run', session_id: 'other' })
    expect(notify).not.toHaveBeenCalled()
    const event = { invocation_id: 'inv', run_id: 'run', session_id: 'session' }
    registry.settleModelPreference(event)
    registry.settleModelPreference(event)
    await write
    expect(notify).toHaveBeenCalledOnce()
    expect(persisted).toBe('B')
    expect(completed).toBe(false)
    registry.resolveAssistantStream('inv')
    await completion
    send.release()
    registry.settleModelPreference(event)
    expect(notify).toHaveBeenCalledOnce()
  })
})
