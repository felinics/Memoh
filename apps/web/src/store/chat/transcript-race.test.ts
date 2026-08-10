import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
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

function sliceFor(turnId: string, overrides: Partial<RuntimeTranscriptSlice> = {}): RuntimeTranscriptSlice {
  return {
    runId: 'run-1',
    turnId,
    status: 'running',
    operation: null,
    streaming: true,
    turns: [rawUser('runtime-user', 'hello first'), rawAssistant('runtime-assistant')],
    ...overrides,
  }
}

// Adversarial ordering at the race seam: run_accepted (bindRuntimeTurn) and
// projection frames (applyRuntimeTranscript) carry no ordering guarantee, so
// every interleaving is exercised deterministically with fake timers instead
// of being left to production probability.
describe('chat transcript race seam', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('buffers a projection frame that outruns its acceptance, then merges on bind', () => {
    const { transcript } = makeTranscript()
    const { userTurn, assistantTurn } = appendUnboundOptimisticPair(transcript)

    transcript.applyRuntimeTranscript(sliceFor('turn-1'))
    // Buffered: no third turn may appear before the acceptance names the turn.
    expect(transcript.messages).toHaveLength(2)

    transcript.bindRuntimeTurn('invocation-1', 'turn-1', 'run-1')

    expect(transcript.messages.map(turn => turn.role)).toEqual(['user', 'assistant'])
    expect(transcript.messages.every(turn => turn.turnId === 'turn-1')).toBe(true)
    // The optimistic pair keeps its render identity through the merge.
    expect(transcript.messages[0]?.id).toBe(userTurn.id)
    expect(transcript.messages[1]?.id).toBe(assistantTurn.id)
  })

  it('flushes standalone after the grace window when no local send claims the frame', () => {
    const { transcript } = makeTranscript()
    appendUnboundOptimisticPair(transcript)

    // A run from another device (turn-9) races our unbound local send.
    transcript.applyRuntimeTranscript(sliceFor('turn-9'))
    expect(transcript.messages).toHaveLength(2)

    vi.advanceTimersByTime(800)

    expect(transcript.messages).toHaveLength(4)
    expect(transcript.messages.slice(2).every(turn => turn.turnId === 'turn-9')).toBe(true)
    // The unbound local pair is untouched and still first.
    expect(transcript.messages[0]?.turnId ?? '').toBe('')
    expect(transcript.messages[1]?.turnId ?? '').toBe('')
  })

  it('adopts the standalone twin when the acceptance arrives after the flush', () => {
    const { transcript } = makeTranscript()
    appendUnboundOptimisticPair(transcript)

    transcript.applyRuntimeTranscript(sliceFor('turn-1'))
    vi.advanceTimersByTime(800)
    // Grace elapsed before the acceptance: the projection twin is on screen
    // under turn-1's own render identity.
    expect(transcript.messages).toHaveLength(4)

    transcript.bindRuntimeTurn('invocation-1', 'turn-1', 'run-1')

    // The late acceptance must not create a second pair for the same turn.
    expect(transcript.messages).toHaveLength(2)
    expect(transcript.messages.map(turn => turn.role)).toEqual(['user', 'assistant'])
    expect(transcript.messages.every(turn => turn.turnId === 'turn-1')).toBe(true)
  })

  it('retires the pre-stamped assistant turn when adopting the standalone twin', () => {
    const { transcript } = makeTranscript()
    const { assistantTurn } = appendUnboundOptimisticPair(transcript)

    transcript.applyRuntimeTranscript(sliceFor('turn-1'))
    vi.advanceTimersByTime(800)
    expect(transcript.messages).toHaveLength(4)

    // In the real run_accepted flow bindRunId stamps the turnId onto the
    // invocation's assistant turn before bindRuntimeTurn runs. Adoption must
    // ignore that own turn when spotting the twin, and must retire it too —
    // otherwise it survives as a third message next to the twin pair.
    assistantTurn.turnId = 'turn-1'
    transcript.bindRuntimeTurn('invocation-1', 'turn-1', 'run-1')

    expect(transcript.messages).toHaveLength(2)
    expect(transcript.messages.map(turn => turn.role)).toEqual(['user', 'assistant'])
    expect(transcript.messages.every(turn => turn.role === 'system' || turn.invocationId !== 'invocation-1')).toBe(true)
  })

  it('resets the grace timer when a fresher frame arrives for the same turn', () => {
    const { transcript } = makeTranscript()
    appendUnboundOptimisticPair(transcript)

    transcript.applyRuntimeTranscript(sliceFor('turn-1', {
      turns: [rawAssistant('runtime-assistant', [{ id: 0, type: 'text', content: 'first' }])],
    }))
    vi.advanceTimersByTime(500)
    transcript.applyRuntimeTranscript(sliceFor('turn-1', {
      turns: [rawAssistant('runtime-assistant', [{ id: 0, type: 'text', content: 'fresher' }])],
    }))
    // 1000ms since the first frame, but only 500ms since the freshest.
    vi.advanceTimersByTime(500)
    expect(transcript.messages).toHaveLength(2)

    transcript.bindRuntimeTurn('invocation-1', 'turn-1', 'run-1')
    expect(transcript.messages).toHaveLength(2)
    const assistant = transcript.messages[1]
    expect(assistant?.role === 'assistant' && assistant.messages[0]).toMatchObject({ content: 'fresher' })
  })

  it('never buffers retry/edit operation frames', () => {
    const { transcript } = makeTranscript()
    appendUnboundOptimisticPair(transcript)

    const applied = transcript.applyRuntimeTranscript(sliceFor('turn-9', {
      operation: { kind: 'retry', replace_from_message_id: 'runtime-user' },
    }))

    // An operation frame whose turn the screen does not know can neither apply
    // nor buffer: the controller signals a history resync instead.
    expect(applied).toBe(false)
    expect(transcript.messages).toHaveLength(2)
    vi.advanceTimersByTime(800)
    expect(transcript.messages).toHaveLength(2)
  })

  it('self-heals to the settled page after an out-of-grace acceptance', async () => {
    const { transcript } = makeTranscript()
    appendUnboundOptimisticPair(transcript)

    transcript.applyRuntimeTranscript(sliceFor('turn-1'))
    vi.advanceTimersByTime(800)
    transcript.bindRuntimeTurn('invocation-1', 'turn-1', 'run-1')
    expect(transcript.messages).toHaveLength(2)

    const settledUser = { ...rawUser('server-user', 'hello first'), turn_id: 'turn-1' }
    const settledAssistant = { ...rawAssistant('server-assistant'), turn_id: 'turn-1' }
    transcript.replaceMessages([settledUser, settledAssistant], 'session-1')

    expect(transcript.messages.map(turn => turn.role)).toEqual(['user', 'assistant'])
    expect(transcript.messages.every(turn => turn.turnId === 'turn-1')).toBe(true)
  })
})
