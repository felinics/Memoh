import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import type { RuntimeCurrentRunView, UITurn } from '@/composables/api/useChat.types'
import { createBackgroundTaskTracker } from './background-tasks'
import { isRuntimeRunStreaming, projectRuntimeTranscript, runOwnsTurn } from './runtime-projection'
import { createTranscriptController } from './transcript'

vi.mock('@/store/user', () => ({
  useUserStore: () => ({ userInfo: { id: 'user-1' } }),
}))

function runView(overrides: Partial<RuntimeCurrentRunView> = {}): RuntimeCurrentRunView {
  return {
    run_id: 'run-1',
    turn_id: 'turn-5',
    turn_position: 5,
    generation: 'generation-1',
    status: 'running',
    started_at: '2026-07-27T08:00:00.000Z',
    updated_at: '2026-07-27T08:00:05.000Z',
    messages: [
      { id: 0, type: 'text', content: 'before steer' },
      { id: 1, type: 'text', content: 'after steer' },
    ],
    user_turns: [{
      turn_id: 'turn-5',
      turn_position: 5,
      role: 'user',
      text: 'live ask',
      timestamp: '2026-07-27T08:00:00.000Z',
    }],
    steer_turns: [{
      item_id: 'item-1',
      status: 'claimed',
      text: 'steer me',
      after_message_id: 0,
      timestamp: '2026-07-27T08:00:02.000Z',
    }],
    ...overrides,
  }
}

function settledTurn(
  id: string,
  turnId: string,
  position: number,
  role: 'user' | 'assistant',
  timestamp: string,
): UITurn {
  return role === 'user'
    ? { id, turn_id: turnId, turn_position: position, role, text: `u-${id}`, timestamp, platform: 'local' }
    : { id, turn_id: turnId, turn_position: position, role, messages: [], timestamp }
}

// Mirrors the wiring in views.ts so the transcript sees the same predicate the
// app does.
function makeTranscript(run: RuntimeCurrentRunView | null) {
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
    locateMessage: vi.fn(),
    isTurnLive: (_sessionId, turnId) =>
      Boolean(run && isRuntimeRunStreaming(run) && runOwnsTurn(run, turnId)),
  })
  return { transcript }
}

describe('a run owns several turns', () => {
  it('claims its request turn, its persisted inputs and its steer turns', () => {
    const run = runView({
      user_turns: [
        { turn_id: 'turn-5', role: 'user', text: 'ask', timestamp: '2026-07-27T08:00:00.000Z' },
        { turn_id: 'turn-6', role: 'user', text: 'steer', timestamp: '2026-07-27T08:00:02.000Z' },
      ],
      steer_turns: [
        { item_id: 'item-1', status: 'applied', text: 'steer', turn_id: 'turn-6', after_message_id: 0, timestamp: '2026-07-27T08:00:02.000Z' },
        { item_id: 'item-2', status: 'claimed', text: 'and again', after_message_id: 1, timestamp: '2026-07-27T08:00:04.000Z' },
      ],
    })

    expect(runOwnsTurn(run, 'turn-5')).toBe(true)
    expect(runOwnsTurn(run, 'turn-6')).toBe(true)
    // A steer with no durable turn yet is named after its queue item; both its
    // bubble and the segment filed under it carry that same identity.
    expect(runOwnsTurn(run, 'queue-steer:item-2')).toBe(true)
    expect(runOwnsTurn(run, 'turn-9')).toBe(false)
    expect(runOwnsTurn(run, 'queue-steer:item-unknown')).toBe(false)
    expect(runOwnsTurn(null, 'turn-5')).toBe(false)
  })

  // A background-task notification (or any session_touched) refreshes history
  // mid-run. The settled page cannot know a steer whose step has not committed,
  // so retention is the only thing keeping it on screen.
  it('keeps a live steer and its output through a mid-run history refresh', () => {
    const run = runView()
    const { transcript } = makeTranscript(run)
    transcript.applyRuntimeTranscript(projectRuntimeTranscript(run))
    expect(transcript.messages.map(turn => turn.turnId)).toEqual([
      'turn-5', 'turn-5', 'queue-steer:item-1', 'queue-steer:item-1',
    ])

    transcript.replaceMessages([
      settledTurn('m5', 'turn-5', 5, 'user', '2026-07-27T08:00:00.000Z'),
    ], 'session-1')

    // The settled page knows only the request user row so far. Everything the
    // run is still producing — its own assistant segment and both halves of the
    // uncommitted steer — has to survive.
    expect(transcript.messages.map(turn => `${turn.role}:${turn.turnId}`)).toEqual([
      'user:turn-5',
      'assistant:turn-5',
      'user:queue-steer:item-1',
      'assistant:queue-steer:item-1',
    ])
  })

  it('still drops a turn whose run has ended and that history does not know', () => {
    const { transcript } = makeTranscript(runView({ status: 'completed' }))
    transcript.applyRuntimeTranscript(projectRuntimeTranscript(runView()))

    transcript.replaceMessages([
      settledTurn('m5', 'turn-5', 5, 'user', '2026-07-27T08:00:00.000Z'),
    ], 'session-1')

    expect(transcript.messages.map(turn => turn.turnId)).toEqual(['turn-5'])
  })
})

describe('a terminal run view is not a source of new turns', () => {
  const completed = (): RuntimeCurrentRunView => ({
    run_id: 'run-old',
    turn_id: 'turn-1',
    turn_position: 1,
    generation: 'generation-1',
    status: 'completed',
    started_at: '2026-07-20T08:00:00.000Z',
    updated_at: '2026-07-20T08:00:05.000Z',
    messages: [{ id: 0, type: 'text', content: 'old reply' }],
    user_turns: [{
      turn_id: 'turn-1',
      turn_position: 1,
      role: 'user',
      text: 'old ask',
      timestamp: '2026-07-20T08:00:00.000Z',
    }],
  })

  // The snapshot keeps a finished run for the whole state TTL and replays it on
  // every subscribe. Appending it re-added a days-old round below the newest
  // turns once it had aged out of the loaded window.
  it('does not re-append a finished run whose turn aged out of the window', () => {
    const { transcript } = makeTranscript(null)
    transcript.replaceMessages([
      settledTurn('m9', 'turn-9', 9, 'user', '2026-07-27T08:00:00.000Z'),
      settledTurn('m10', 'turn-9', 9, 'assistant', '2026-07-27T08:00:01.000Z'),
    ], 'session-1')

    const applied = transcript.applyRuntimeTranscript(projectRuntimeTranscript(completed()))

    expect(applied).toBe(true)
    expect(transcript.messages.map(turn => turn.turnId)).toEqual(['turn-9', 'turn-9'])
  })

  // A run that fails never persists its error: the reason lives only in the
  // terminal frame. History returns the question alone, so a window test keyed
  // on position alone dropped the assistant carrying the reason — reopening a
  // failed session showed the question and nothing to explain it.
  it('keeps the failure reason of a run whose question is already settled', () => {
    const { transcript } = makeTranscript(null)
    transcript.replaceMessages([
      settledTurn('m1', 'turn-1', 1, 'user', '2026-07-20T08:00:00.000Z'),
    ], 'session-1')

    transcript.applyRuntimeTranscript(projectRuntimeTranscript({
      ...completed(),
      status: 'errored',
      messages: [{ id: 0, type: 'error', content: 'model refused' }],
    }))

    expect(transcript.messages.map(turn => `${turn.role}:${turn.turnId}`)).toEqual([
      'user:turn-1',
      'assistant:turn-1',
    ])
    const assistant = transcript.messages[1]
    expect(assistant?.role === 'assistant' && assistant.messages).toEqual([
      expect.objectContaining({ content: 'model refused' }),
    ])
  })

  it.each(['history first', 'runtime first'])('keeps published media when replaying a completed run (%s)', (order) => {
    const { transcript } = makeTranscript(null)
    const published = '![chart](/bots/bot-1/media/archived-hash)\n![missing](memoh-media-error:media.reference_unavailable)'
    const history: UITurn[] = [
      settledTurn('m1', 'turn-1', 1, 'user', '2026-07-20T08:00:00.000Z'),
      {
        id: 'm2',
        turn_id: 'turn-1',
        turn_position: 1,
        role: 'assistant',
        timestamp: '2026-07-20T08:00:01.000Z',
        messages: [{ id: 0, type: 'text', content: published }],
      },
    ]
    const replay = projectRuntimeTranscript({
      ...completed(),
      messages: [{ id: 0, type: 'text', content: '![chart](/data/chart.png)\n![missing](/data/missing.png)' }],
    })
    if (order === 'runtime first') transcript.applyRuntimeTranscript(replay)
    transcript.replaceMessages(history, 'session-1')
    transcript.applyRuntimeTranscript(replay)
    // Re-entering the cached session can replay the same terminal snapshot again.
    transcript.applyRuntimeTranscript(replay)

    const assistant = transcript.messages.find(turn => turn.role === 'assistant')
    expect(assistant?.role === 'assistant' && assistant.messages).toEqual([
      expect.objectContaining({ content: published }),
    ])

    // An active replacement must still be able to stream and then finish.
    for (const status of ['running', 'completed'] as const) {
      transcript.applyRuntimeTranscript(projectRuntimeTranscript({
        ...completed(),
        status,
        messages: [{ id: 0, type: 'text', content: status }],
      }))
      expect(assistant?.role === 'assistant' && assistant.messages).toEqual([
        expect.objectContaining({ content: status }),
      ])
    }
  })

  it('still reconciles a finished run onto the turn already on screen', () => {
    const { transcript } = makeTranscript(null)
    transcript.replaceMessages([
      settledTurn('m1', 'turn-1', 1, 'user', '2026-07-20T08:00:00.000Z'),
      settledTurn('m2', 'turn-1', 1, 'assistant', '2026-07-20T08:00:01.000Z'),
    ], 'session-1')

    transcript.applyRuntimeTranscript(projectRuntimeTranscript(completed()))

    expect(transcript.messages.map(turn => turn.turnId)).toEqual(['turn-1', 'turn-1'])
    const assistant = transcript.messages[1]
    expect(assistant?.role === 'assistant' && assistant.messages).toEqual([
      expect.objectContaining({ content: 'old reply' }),
    ])
  })

  // The run just ended and its step commit has not reached the message page
  // yet. That round is the newest thing in the session, so it must still show.
  it('appends a just-finished run whose turn is newer than the settled page', () => {
    const { transcript } = makeTranscript(null)
    transcript.replaceMessages([
      settledTurn('m0', 'turn-0', 0, 'user', '2026-07-20T07:00:00.000Z'),
    ], 'session-1')

    transcript.applyRuntimeTranscript(projectRuntimeTranscript(completed()))

    expect(transcript.messages.map(turn => turn.turnId)).toEqual(['turn-0', 'turn-1', 'turn-1'])
  })

  // A server that predates turn_position gives no evidence either way, so the
  // frame keeps the behaviour it had before the position existed.
  it('appends a finished run from a server that sends no position', () => {
    const { transcript } = makeTranscript(null)
    transcript.replaceMessages([
      settledTurn('m9', 'turn-9', 9, 'user', '2026-07-27T08:00:00.000Z'),
    ], 'session-1')

    // A server from before the field sends no position anywhere, so the window
    // has nothing to compare against and the frame keeps its old behaviour.
    transcript.applyRuntimeTranscript(projectRuntimeTranscript({
      ...completed(),
      turn_position: undefined,
      user_turns: [{
        turn_id: 'turn-1',
        role: 'user',
        text: 'old ask',
        timestamp: '2026-07-20T08:00:00.000Z',
      }],
    }))

    expect(transcript.messages.map(turn => turn.turnId)).toEqual(['turn-9', 'turn-1', 'turn-1'])
  })

  // A run owns several turns, so its starting position says nothing about where
  // its later turns landed. Judging the whole frame by that one number threw
  // away a steer's freshly committed answer along with the aged-out first half.
  it('keeps the later turns of a run whose first turn aged out', () => {
    const { transcript } = makeTranscript(null)
    const window: UITurn[] = []
    for (let position = 6; position <= 35; position++) {
      window.push(settledTurn(`m${position}`, `turn-${position}`, position,
        'user', `2026-07-27T08:${String(position).padStart(2, '0')}:00.000Z`))
    }
    transcript.replaceMessages(window, 'session-1')

    transcript.applyRuntimeTranscript(projectRuntimeTranscript({
      ...completed(),
      messages: [
        { id: 0, type: 'text', content: 'first half' },
        { id: 1, type: 'text', content: 'latest answer' },
      ],
      user_turns: [
        { turn_id: 'turn-1', turn_position: 1, role: 'user', text: 'ask', timestamp: '2026-07-20T08:00:00.000Z' },
        { turn_id: 'turn-36', turn_position: 36, role: 'user', text: 'steer', timestamp: '2026-07-27T08:36:00.000Z' },
      ],
      steer_turns: [{
        item_id: 'item-1',
        status: 'applied',
        text: 'steer',
        turn_id: 'turn-36',
        after_message_id: 0,
        timestamp: '2026-07-27T08:36:00.000Z',
      }],
    }))

    const tail = transcript.messages.slice(-2).map(turn => `${turn.role}:${turn.turnId}`)
    expect(tail).toEqual(['user:turn-36', 'assistant:turn-36'])
    // The first half is inside the window the read covered and did not come
    // back, so history does not have it and the frame must not re-add it.
    expect(transcript.messages.some(turn => turn.turnId === 'turn-1')).toBe(false)
  })

  // The window test has to run per turn rather than only when nothing matched:
  // the later half of the same run can still be on screen while the first half
  // has aged out.
  it('drops the aged-out half of a run whose later half is still on screen', () => {
    const { transcript } = makeTranscript(null)
    transcript.replaceMessages([
      settledTurn('m30', 'turn-30', 30, 'user', '2026-07-27T08:30:00.000Z'),
      settledTurn('m36', 'turn-36', 36, 'user', '2026-07-27T08:36:00.000Z'),
      settledTurn('m36a', 'turn-36', 36, 'assistant', '2026-07-27T08:36:01.000Z'),
    ], 'session-1')

    transcript.applyRuntimeTranscript(projectRuntimeTranscript({
      ...completed(),
      messages: [
        { id: 0, type: 'text', content: 'first half' },
        { id: 1, type: 'text', content: 'second half' },
      ],
      user_turns: [
        { turn_id: 'turn-1', turn_position: 1, role: 'user', text: 'ask', timestamp: '2026-07-20T08:00:00.000Z' },
        { turn_id: 'turn-36', turn_position: 36, role: 'user', text: 'steer', timestamp: '2026-07-27T08:36:00.000Z' },
      ],
      steer_turns: [{
        item_id: 'item-1',
        status: 'applied',
        text: 'steer',
        turn_id: 'turn-36',
        after_message_id: 0,
        timestamp: '2026-07-27T08:36:00.000Z',
      }],
    }))

    expect(transcript.messages.map(turn => `${turn.role}:${turn.turnId}`)).toEqual([
      'user:turn-30',
      'user:turn-36',
      'assistant:turn-36',
    ])
  })

  it('lets an active run introduce a turn the transcript has not seen', () => {
    const run = runView({ steer_turns: [] })
    const { transcript } = makeTranscript(run)
    transcript.replaceMessages([
      settledTurn('m9', 'turn-9', 9, 'user', '2026-07-27T07:00:00.000Z'),
    ], 'session-1')

    transcript.applyRuntimeTranscript(projectRuntimeTranscript(run))

    // The run holds position 5, so turn-9 was numbered while it was already
    // streaming: the run's turns belong ahead of it. This asserted the tail
    // append that insertRuntimeTurns replaced with per-turn placement.
    expect(transcript.messages.map(turn => turn.turnId)).toEqual(['turn-5', 'turn-5', 'turn-9'])
  })
})

// Ownership reads the run's inputs through the same wire-shape adapter the
// projection uses, so every shape the server can send is covered by one chain.
describe('ownership covers every shape a run view can carry', () => {
  it('claims the legacy request_user_turn when user_turns is absent', () => {
    expect(runOwnsTurn({
      run_id: 'run-1',
      turn_id: 'turn-5',
      generation: 'g',
      status: 'running',
      started_at: '2026-07-27T08:00:00.000Z',
      updated_at: '2026-07-27T08:00:00.000Z',
      messages: [],
      request_user_turn: {
        turn_id: 'turn-legacy',
        role: 'user',
        text: 'ask',
        timestamp: '2026-07-27T08:00:00.000Z',
      },
    }, 'turn-legacy')).toBe(true)
  })

  it('claims an edit\'s replacement turn', () => {
    expect(runOwnsTurn({
      run_id: 'run-1',
      turn_id: 'turn-5',
      generation: 'g',
      status: 'running',
      started_at: '2026-07-27T08:00:00.000Z',
      updated_at: '2026-07-27T08:00:00.000Z',
      messages: [],
      operation: {
        kind: 'edit',
        replace_from_message_id: 'm1',
        replacement_user_turn: {
          turn_id: 'turn-replacement',
          role: 'user',
          text: 'edited',
          timestamp: '2026-07-27T08:00:00.000Z',
        },
      },
    }, 'turn-replacement')).toBe(true)
  })
})
