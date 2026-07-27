import { ref, toRaw } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { createAssistantStreamRegistry } from './assistant-streams'
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

function makeRegistry(activeSessionId: string | null = 'session-a') {
  const currentBotId = ref<string | null>('bot-1')
  const sessionId = ref<string | null>(activeSessionId)
  const finishAssistantTurn = vi.fn((turn: ChatAssistantTurn) => {
    turn.streaming = false
  })
  const registry = createAssistantStreamRegistry({ currentBotId, sessionId, finishAssistantTurn })
  return { registry, currentBotId, sessionId, finishAssistantTurn }
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
  it('registers synchronously and resolves only after removing the active stream', async () => {
    const { registry, finishAssistantTurn } = makeRegistry()
    const { turn, completion } = track(registry, 'invocation-1')

    expect(toRaw(registry.getAssistantStream('invocation-1')!.assistantTurn)).toBe(turn)
    expect(registry.streaming.value).toBe(true)

    const settled = vi.fn()
    const observed = completion.then(() => settled('resolved'))
    registry.resolveAssistantStream('invocation-1')
    registry.resolveAssistantStream('invocation-1')
    await observed

    expect(settled).toHaveBeenCalledOnce()
    expect(finishAssistantTurn).toHaveBeenCalledOnce()
    expect(turn.streaming).toBe(false)
    expect(registry.getAssistantStream('invocation-1')).toBeUndefined()
    expect(registry.streaming.value).toBe(false)
  })

  it('rejects blank and duplicate ids without replacing the original stream', async () => {
    const { registry } = makeRegistry()
    await expect(track(registry, ' ').completion).rejects.toThrow('invocation_id is required')

    const original = track(registry, 'invocation-1')
    const duplicate = track(registry, 'invocation-1')
    await expect(duplicate.completion).rejects.toThrow('invocation_id invocation-1 is already active')
    expect(toRaw(registry.getAssistantStream('invocation-1')!.assistantTurn)).toBe(original.turn)

    const failure = new Error('failed')
    registry.rejectAssistantStream('invocation-1', failure)
    await expect(original.completion).rejects.toBe(failure)
    expect(original.turn.streaming).toBe(false)
  })

  it('discards a pre-dispatch stream as a settled terminal transition', async () => {
    const { registry } = makeRegistry()
    const entry = track(registry, 'invocation-1')

    registry.discardAssistantStream('invocation-1')

    await expect(entry.completion).resolves.toBeUndefined()
    expect(entry.turn.streaming).toBe(false)
    expect(registry.getAssistantStream('invocation-1')).toBeUndefined()
    expect(registry.isTerminalInvocation('invocation-1')).toBe(true)
    await expect(track(registry, 'invocation-1').completion).rejects.toThrow('invocation_id invocation-1 is already terminal')
  })

  it('reactively prioritizes only the selected bot streaming session', async () => {
    const { registry, currentBotId, sessionId } = makeRegistry('session-b')
    const first = track(registry, 'invocation-a', 'session-a')
    const second = track(registry, 'invocation-b', 'session-b')
    const otherBot = track(registry, 'invocation-other', 'session-b', 'bot-2')

    expect(registry.streaming.value).toBe(true)
    expect(registry.streamingSessionId.value).toBe('session-b')
    expect(registry.assistantStreamsForSession('bot-1', 'session-a').map(stream => stream.invocationId)).toEqual(['invocation-a'])
    expect(registry.assistantStreamsForSession('bot-2', 'session-a')).toEqual([])
    expect(registry.isSessionStreaming('bot-1', 'session-b')).toBe(true)
    expect(registry.isSessionStreaming('bot-2', 'session-b')).toBe(true)

    sessionId.value = 'session-c'
    expect(registry.streaming.value).toBe(false)
    expect(registry.streamingSessionId.value).toBe('session-a')

    currentBotId.value = 'bot-2'
    expect(registry.streaming.value).toBe(false)
    expect(registry.streamingSessionId.value).toBe('session-b')
    currentBotId.value = 'bot-1'

    registry.resolveAssistantStream('invocation-a')
    await first.completion
    expect(registry.streamingSessionId.value).toBe('session-b')

    registry.resolveAssistantStream('invocation-b')
    await second.completion
    expect(registry.streamingSessionId.value).toBeNull()

    registry.resolveAssistantStream('invocation-other')
    await otherBot.completion
  })

  it('routes missing event ids only when the session has one unambiguous stream', async () => {
    const { registry } = makeRegistry()
    const first = track(registry, 'invocation-a')

    expect(registry.invocationIdForEvent('bot-1', { session_id: 'session-a' })).toBe('invocation-a')
    expect(registry.invocationIdForEvent('bot-1', { invocation_id: 'explicit', session_id: 'session-a' })).toBe('explicit')

    const second = track(registry, 'invocation-b')
    expect(registry.invocationIdForEvent('bot-1', { session_id: 'session-a' })).toBe('session:bot-1:session-a:agent-run')
    expect(registry.invocationIdForEvent('bot-1', {}, '')).toBe('bot:bot-1:orphan-run')

    registry.resolveAssistantStream('invocation-a')
    registry.resolveAssistantStream('invocation-b')
    await Promise.all([first.completion, second.completion])
  })

  it('routes later events by run id and prefers it over an ambiguous session', async () => {
    const { registry } = makeRegistry()
    const first = track(registry, 'invocation-a')
    const second = track(registry, 'invocation-b')

    expect(registry.bindRunId('invocation-a', 'run-a')).toMatchObject({ runId: 'run-a', botId: 'bot-1' })
    expect(registry.invocationIdForEvent('bot-1', { run_id: 'run-a', session_id: 'session-a' })).toBe('invocation-a')
    // A run started elsewhere has no local invocation and cannot borrow one
    // while the session holds more than a single turn.
    expect(registry.invocationIdForEvent('bot-1', { run_id: 'run-foreign', session_id: 'session-a' })).toBe('run-foreign')

    registry.resolveAssistantStream('invocation-a')
    // The mapping outlives the turn, so a late event still resolves to its own
    // terminal invocation rather than being adopted by the surviving turn.
    expect(registry.invocationIdForEvent('bot-1', { run_id: 'run-a', session_id: 'session-a' })).toBe('invocation-a')
    expect(registry.isTerminalInvocation('invocation-a')).toBe(true)

    registry.resolveAssistantStream('invocation-b')
    await Promise.all([first.completion, second.completion])
  })

  it('keeps an accepted run addressable even when no local turn tracks it', async () => {
    const { registry } = makeRegistry()
    const only = track(registry, 'invocation-a')

    // A silent approval response is sent without a UI turn. Its run must not be
    // mistaken for the session's one visible turn.
    registry.bindRunId('silent-invocation', 'run-silent')
    expect(registry.invocationIdForEvent('bot-1', { run_id: 'run-silent', session_id: 'session-a' })).toBe('silent-invocation')
    expect(registry.getAssistantStream('silent-invocation')).toBeUndefined()

    registry.resolveAssistantStream('invocation-a')
    await only.completion
  })

  it('defers a stop pressed before the run is accepted', async () => {
    const { registry } = makeRegistry()
    const entry = track(registry, 'invocation-1')

    expect(registry.requestAbort('invocation-1')).toBe('')
    expect(registry.bindRunId('invocation-1', 'run-1')).toMatchObject({
      runId: 'run-1',
      botId: 'bot-1',
      abortRequested: true,
    })
    // Once bound, the same stop resolves to a run the server can address, and
    // the deferred intent is not replayed a second time.
    expect(registry.requestAbort('invocation-1')).toBe('run-1')
    expect(registry.bindRunId('invocation-1', 'run-1')?.abortRequested).toBe(false)
    expect(registry.bindRunId('invocation-1', undefined)).toBeUndefined()

    registry.resolveAssistantStream('invocation-1')
    await entry.completion
  })

  it('addresses a stop for a silent turn that has no stream of its own', () => {
    const { registry } = makeRegistry()

    expect(registry.requestAbort('silent-invocation')).toBe('')
    expect(registry.bindRunId('silent-invocation', 'run-silent')).toEqual({
      invocationId: 'silent-invocation',
      runId: 'run-silent',
      botId: '',
      abortRequested: true,
    })
    expect(registry.requestAbort('silent-invocation')).toBe('run-silent')
  })

  it('keeps a shared continuation turn streaming until every stream finishes', async () => {
    const { registry } = makeRegistry()
    const turn = assistantTurn('shared-turn')
    const first = registry.trackAssistantStream({
      invocationId: 'main-invocation', assistantTurn: turn, botId: 'bot-1', sessionId: 'session-a',
    })
    const second = registry.trackAssistantStream({
      invocationId: 'response-invocation', assistantTurn: turn, botId: 'bot-1', sessionId: 'session-a',
    })

    registry.resolveAssistantStream('response-invocation')
    await second
    expect(turn.streaming).toBe(true)

    registry.resolveAssistantStream('main-invocation')
    await first
    expect(turn.streaming).toBe(false)
  })

  it('maps resumed stream block ids after the existing assistant turn', async () => {
    const { registry } = makeRegistry()
    const turn = assistantTurn('resumed-turn')
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
      type: 'reasoning',
      content: 'Continuing',
    })).toMatchObject({ id: 5, content: 'Continuing' })
    expect(registry.mapAssistantStreamMessage('response-invocation', {
      id: 0,
      type: 'reasoning',
      content: 'Continuing with more detail',
    })).toMatchObject({ id: 5, content: 'Continuing with more detail' })
    expect(registry.mapAssistantStreamMessage('response-invocation', {
      id: 1,
      type: 'text',
      content: 'Done',
    })).toMatchObject({ id: 6, content: 'Done' })

    registry.resolveAssistantStream('response-invocation')
    await completion
  })

  it('binds a deferred stream once and retains created-session metadata past terminal', async () => {
    const { registry, sessionId } = makeRegistry(null)
    const deferred = track(registry, 'invocation-1', '')

    expect(registry.streaming.value).toBe(true)
    expect(registry.streamingSessionId.value).toBeNull()
    expect(registry.isUnboundComposerStreaming('bot-1')).toBe(true)
    expect(registry.isUnboundComposerStreaming('bot-1', 'chat')).toBe(true)
    expect(registry.isUnboundComposerStreaming('bot-2')).toBe(false)
    expect(registry.activeUnboundInvocationIds('bot-1')).toEqual(['invocation-1'])
    expect(registry.activeUnboundInvocationIds('bot-1', 'other')).toEqual([])
    registry.recordCreatedSession('invocation-1', 'session-created')
    registry.recordCreatedSession('invocation-1', 'conflicting-session')
    expect(registry.getAssistantStream('invocation-1')?.sessionId).toBe('session-created')
    expect(registry.createdSessionIdForInvocation('invocation-1')).toBe('session-created')

    sessionId.value = 'session-created'
    expect(registry.streaming.value).toBe(true)
    registry.resolveAssistantStream('invocation-1')
    await deferred.completion

    expect(registry.createdSessionIdForInvocation('invocation-1')).toBe('session-created')
    registry.forgetCreatedSession('invocation-1')
    expect(registry.createdSessionIdForInvocation('invocation-1')).toBe('')
  })

  it('records created-session metadata even after the pending entry is gone', async () => {
    const { registry } = makeRegistry()
    registry.recordCreatedSession('late-invocation', 'session-created')
    registry.recordCreatedSession('late-invocation', 'conflicting-session')
    expect(registry.createdSessionIdForInvocation('late-invocation')).toBe('session-created')

    const terminal = track(registry, 'terminal-invocation')
    registry.resolveAssistantStream('terminal-invocation')
    await terminal.completion
    expect(registry.isTerminalInvocation('terminal-invocation')).toBe(true)
    registry.clearStreamHistory()
    expect(registry.createdSessionIdForInvocation('late-invocation')).toBe('')
    expect(registry.isTerminalInvocation('terminal-invocation')).toBe(false)
  })

  it('rejects the global stream snapshot in insertion order', async () => {
    const { registry } = makeRegistry()
    const first = track(registry, 'invocation-a1', 'session-a')
    const second = track(registry, 'invocation-b1', 'session-b')
    const third = track(registry, 'invocation-a2', 'session-a')
    const completions = [first, second, third].map(entry => entry.completion.catch(error => error))
    const failure = new Error('aborted')
    const beforeReject: string[] = []

    registry.rejectAllStreams(failure, (invocationId) => {
      expect(registry.getAssistantStream(invocationId)).toBeDefined()
      beforeReject.push(invocationId)
    })
    expect(beforeReject).toEqual(['invocation-a1', 'invocation-b1', 'invocation-a2'])
    expect(await Promise.all(completions)).toEqual([failure, failure, failure])
  })
})
