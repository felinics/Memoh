import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import type { UIMessage, UITurn } from '@/composables/api/useChat.types'
import { createBackgroundTaskTracker } from './background-tasks'
import { createTranscriptController } from './transcript'
import type { RuntimeTranscriptSlice } from './runtime-projection'

vi.mock('@/store/user', () => ({
  useUserStore: () => ({ userInfo: { id: 'user-1' } }),
}))

function rawUser(id: string, text = 'hello', timestamp = '2026-01-01T00:00:00.000Z'): UITurn {
  return { id, turn_id: `turn-${id}`, role: 'user', text, timestamp, platform: 'local' }
}

function rawAssistant(id: string, messages: UIMessage[] = [], timestamp = '2026-01-01T00:00:01.000Z'): UITurn {
  return { id, turn_id: `turn-${id}`, role: 'assistant', messages, timestamp }
}

function makeTranscript() {
  const currentBotId = ref<string | null>('bot-1')
  const sessionId = ref<string | null>('session-1')
  const backgroundTasks = createBackgroundTaskTracker()
  const transcript = createTranscriptController({
    currentBotId,
    sessionId,
    rememberBackgroundTask: backgroundTasks.rememberBackgroundTask,
    applyPendingBackgroundEventsToTool: backgroundTasks.applyPendingBackgroundEventsToTool,
    bumpFsChangedAtIfFsMutation: vi.fn(),
    fetchMessages: vi.fn().mockResolvedValue([]),
    locateMessage: vi.fn().mockResolvedValue({ items: [], target_id: '', target_external_message_id: '' }),
  })
  return { transcript }
}

function appendUnboundOptimisticPair(transcript: ReturnType<typeof makeTranscript>['transcript']) {
  const assistantTurn = transcript.createOptimisticAssistantTurn('invocation-1')
  const userTurn = transcript.createOptimisticUserTurn('hello first', undefined, 'invocation-1')
  transcript.appendToView(userTurn, assistantTurn)
  return { userTurn, assistantTurn }
}

function sliceFor(turnId: string, invocationId = '', overrides: Partial<RuntimeTranscriptSlice> = {}): RuntimeTranscriptSlice {
  return {
    runId: 'run-1',
    turnId,
    invocationId,
    status: 'running',
    operation: null,
    streaming: true,
    turns: [rawUser('runtime-user', 'hello first'), rawAssistant('runtime-assistant')],
    ...overrides,
  }
}

// Identity matching at the projection seam. Projection frames echo the
// originating send's invocation_id, so every run_accepted × frame
// interleaving resolves by reading the frame — there is no timing window
// and nothing to guess. These tests enumerate the interleavings.
describe('chat transcript projection identity', () => {
  it('binds the optimistic pair by invocation when the frame outruns the acceptance', () => {
    const { transcript } = makeTranscript()
    const { userTurn, assistantTurn } = appendUnboundOptimisticPair(transcript)

    transcript.applyRuntimeTranscript(sliceFor('turn-1', 'invocation-1'))

    // The pair is bound and merged in place: no twin, render identity kept.
    expect(transcript.messages).toHaveLength(2)
    expect(transcript.messages.every(turn => turn.turnId === 'turn-1')).toBe(true)
    expect(transcript.messages[0]?.id).toBe(userTurn.id)
    expect(transcript.messages[1]?.id).toBe(assistantTurn.id)
  })

  it('merges into the pair bound by an earlier acceptance', () => {
    const { transcript } = makeTranscript()
    const { userTurn, assistantTurn } = appendUnboundOptimisticPair(transcript)

    transcript.bindRuntimeTurn('invocation-1', 'turn-1', 'run-1')
    transcript.applyRuntimeTranscript(sliceFor('turn-1', 'invocation-1'))

    expect(transcript.messages).toHaveLength(2)
    expect(transcript.messages[0]?.id).toBe(userTurn.id)
    expect(transcript.messages[1]?.id).toBe(assistantTurn.id)
  })

  it('keeps multiple applied steer inputs as distinct live user turns', () => {
    const { transcript } = makeTranscript()
    const { userTurn, assistantTurn } = appendUnboundOptimisticPair(transcript)

    transcript.applyRuntimeTranscript(sliceFor('turn-1', 'invocation-1', {
      turns: [
        rawUser('runtime-user', 'hello first'),
        { ...rawUser('runtime-steer-1', 'first steer'), turn_id: 'turn-steer-1' },
        { ...rawUser('runtime-steer-2', 'second steer'), turn_id: 'turn-steer-2' },
        rawAssistant('runtime-assistant'),
      ],
    }))

    expect(transcript.messages.map(turn => [turn.role, turn.turnId])).toEqual([
      ['user', 'turn-1'],
      ['user', 'turn-steer-1'],
      ['user', 'turn-steer-2'],
      ['assistant', 'turn-1'],
    ])
    expect(transcript.messages[0]?.id).toBe(userTurn.id)
    expect(transcript.messages[3]?.id).toBe(assistantTurn.id)
  })

  it('merges when the acceptance pre-stamped the assistant before the frame arrived', () => {
    const { transcript } = makeTranscript()
    const { userTurn, assistantTurn } = appendUnboundOptimisticPair(transcript)
    // bindRunId stamps the turnId onto the invocation's assistant turn inside
    // the run_accepted handler, before any frame processing; the later frame
    // must still merge into the pair, not treat it as a foreign owner.
    assistantTurn.turnId = 'turn-1'

    transcript.applyRuntimeTranscript(sliceFor('turn-1', 'invocation-1'))

    expect(transcript.messages).toHaveLength(2)
    expect(transcript.messages[0]?.id).toBe(userTurn.id)
    expect(transcript.messages[1]?.id).toBe(assistantTurn.id)
  })

  it('lands a foreign run standalone without waiting for any acceptance', () => {
    const { transcript } = makeTranscript()
    appendUnboundOptimisticPair(transcript)

    // A frame naming an invocation this screen does not know is a run from
    // another device: it renders immediately, no grace delay.
    transcript.applyRuntimeTranscript(sliceFor('turn-9', 'someone-elses-invocation'))

    expect(transcript.messages).toHaveLength(4)
    expect(transcript.messages.slice(2).every(turn => turn.turnId === 'turn-9')).toBe(true)
    // The unbound local pair is untouched and still first.
    expect(transcript.messages[0]?.turnId ?? '').toBe('')
    expect(transcript.messages[1]?.turnId ?? '').toBe('')
  })

  it('marks a server-owned continuation user turn for immediate layout handoff', () => {
    const { transcript } = makeTranscript()
    transcript.applyRuntimeTranscript(sliceFor('turn-continuation', 'continuation:item-1', {
      continuation: true,
      turns: [rawUser('runtime-user', 'follow-up input'), rawAssistant('runtime-assistant')],
    }))

    expect(transcript.messages[0]).toMatchObject({
      role: 'user',
      text: 'follow-up input',
      runtimeContinuation: true,
    })
  })

  it('treats the acceptance as a no-op once the frame already bound the pair', () => {
    const { transcript } = makeTranscript()
    const { userTurn, assistantTurn } = appendUnboundOptimisticPair(transcript)

    transcript.applyRuntimeTranscript(sliceFor('turn-1', 'invocation-1'))
    transcript.bindRuntimeTurn('invocation-1', 'turn-1', 'run-1')

    expect(transcript.messages).toHaveLength(2)
    expect(transcript.messages[0]?.id).toBe(userTurn.id)
    expect(transcript.messages[1]?.id).toBe(assistantTurn.id)
  })

  it('retires the invocation leftovers when its binding arrives after the turn settled', () => {
    const { transcript } = makeTranscript()
    appendUnboundOptimisticPair(transcript)

    // The acceptance was lost and the turn already settled from history:
    // turn-1's seat is owned by an identity that is not invocation-1. A late
    // (duplicate) acceptance must not put a second pair on screen.
    const settledUser = { ...rawUser('server-user', 'hello first'), turn_id: 'turn-1' }
    const settledAssistant = { ...rawAssistant('server-assistant'), turn_id: 'turn-1' }
    transcript.replaceMessages([settledUser, settledAssistant], 'session-1')

    transcript.bindRuntimeTurn('invocation-1', 'turn-1', 'run-1')

    const nonSystem = transcript.messages.filter(turn => turn.role !== 'system')
    expect(nonSystem).toHaveLength(2)
    expect(nonSystem.every(turn => turn.turnId === 'turn-1')).toBe(true)
    expect(nonSystem.every(turn => turn.invocationId !== 'invocation-1')).toBe(true)
  })

  it('signals a history resync for an operation frame whose turn is unknown', () => {
    const { transcript } = makeTranscript()
    appendUnboundOptimisticPair(transcript)

    const applied = transcript.applyRuntimeTranscript(sliceFor('turn-9', '', {
      operation: { kind: 'retry', replace_from_message_id: 'runtime-user' },
    }))

    // An operation frame whose turn the screen does not know can neither apply
    // nor be claimed: the controller signals a history resync instead.
    expect(applied).toBe(false)
    expect(transcript.messages).toHaveLength(2)
  })

  it('applies a foreign edit in place when its anchor is on screen', () => {
    const { transcript } = makeTranscript()
    // Settled history pair the foreign edit can anchor to.
    transcript.replaceMessages([rawUser('u1', 'original'), rawAssistant('a1')], 'session-1')

    const applied = transcript.applyRuntimeTranscript(sliceFor('turn-9', 'someone-elses-invocation', {
      operation: { kind: 'edit', replace_from_message_id: 'u1' },
      turns: [rawUser('runtime-user', 'edited elsewhere'), rawAssistant('runtime-assistant')],
    }))

    expect(applied).toBe(true)
    expect(transcript.messages).toHaveLength(2)
    expect(transcript.messages.every(turn => turn.turnId === 'turn-9')).toBe(true)
    expect(transcript.messages[0]).toMatchObject({ role: 'user', text: 'edited elsewhere' })
  })

  it('self-heals to the settled page after a frame-first flow', () => {
    const { transcript } = makeTranscript()
    appendUnboundOptimisticPair(transcript)

    transcript.applyRuntimeTranscript(sliceFor('turn-1', 'invocation-1'))
    expect(transcript.messages).toHaveLength(2)

    const settledUser = { ...rawUser('server-user', 'hello first'), turn_id: 'turn-1' }
    const settledAssistant = { ...rawAssistant('server-assistant'), turn_id: 'turn-1' }
    transcript.replaceMessages([settledUser, settledAssistant], 'session-1')

    expect(transcript.messages.map(turn => turn.role)).toEqual(['user', 'assistant'])
    expect(transcript.messages.every(turn => turn.turnId === 'turn-1')).toBe(true)
  })
})
