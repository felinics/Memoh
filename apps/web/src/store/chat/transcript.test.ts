import { describe, expect, it, vi } from 'vitest'
import { nextTick, ref, toRaw, watch } from 'vue'
import type { UIMessage, UITurn } from '@/composables/api/useChat.types'
import { createBackgroundTaskTracker } from './background-tasks'
import { createTranscriptController } from './transcript'
import type { ChatAssistantTurn, ChatUserTurn, ToolCallBlock } from './types'

vi.mock('@/store/user', () => ({
  useUserStore: () => ({ userInfo: { id: 'user-1' } }),
}))

function rawUser(id: string, text = 'hello', timestamp = '2026-01-01T00:00:00.000Z'): UITurn {
  return { id, turn_id: `turn-${id}`, role: 'user', text, timestamp, platform: 'local' }
}

function rawAssistant(id: string, messages: UIMessage[] = [], timestamp = '2026-01-01T00:00:01.000Z'): UITurn {
  return { id, turn_id: `turn-${id}`, role: 'assistant', messages, timestamp }
}

function assistant(id: string, messages: ChatAssistantTurn['messages'] = []): ChatAssistantTurn {
  return {
    id,
    role: 'assistant',
    messages,
    timestamp: '2026-01-01T00:00:01.000Z',
    streaming: true,
    __optimistic: true,
  }
}

function makeTranscript() {
  const currentBotId = ref<string | null>('bot-1')
  const sessionId = ref<string | null>('session-1')
  const backgroundTasks = createBackgroundTaskTracker()
  const bumpFsChangedAtIfFsMutation = vi.fn()
  const fetchMessages = vi.fn().mockResolvedValue([])
  const locateMessage = vi.fn().mockResolvedValue({
    items: [],
    target_id: '',
    target_external_message_id: '',
  })
  const transcript = createTranscriptController({
    currentBotId,
    sessionId,
    rememberBackgroundTask: backgroundTasks.rememberBackgroundTask,
    applyPendingBackgroundEventsToTool: backgroundTasks.applyPendingBackgroundEventsToTool,
    bumpFsChangedAtIfFsMutation,
    fetchMessages,
    locateMessage,
  })
  return { transcript, currentBotId, sessionId, bumpFsChangedAtIfFsMutation, fetchMessages, locateMessage }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

function approvalMessage(status = 'pending'): UIMessage {
  return {
    id: 1,
    type: 'tool',
    name: 'exec',
    input: { command: 'pwd' },
    tool_call_id: 'call-1',
    running: false,
    approval: { approval_id: 'approval-1', status, can_approve: status === 'pending' },
  }
}

describe('chat transcript controller', () => {
  it('is the single context gate for appending active-session turns', () => {
    const { transcript } = makeTranscript()
    const turn = assistant('assistant-1')

    transcript.appendTurnToSession('bot-1', 'other-session', turn)
    expect(transcript.messages).toHaveLength(0)

    transcript.appendTurnToSession('bot-1', 'session-1', turn)
    expect(toRaw(transcript.messages[0])).toBe(turn)
  })

  it('owns server-id lookup and latest visible turn queries', () => {
    const { transcript } = makeTranscript()
    transcript.replaceMessages([
      rawUser('user-1'),
      rawAssistant('assistant-1'),
      rawUser('user-2'),
      rawAssistant('assistant-2'),
    ], 'session-1')
    const latestUser = transcript.findTurnByServerId('user-2')!
    const latestAssistant = transcript.findTurnByServerId('assistant-2')!
    const optimisticUser: ChatUserTurn = {
      id: 'optimistic-user',
      role: 'user',
      text: 'pending',
      attachments: [],
      timestamp: '2026-01-01T00:00:03.000Z',
      streaming: false,
      isSelf: true,
      __optimistic: true,
    }
    transcript.appendToView(optimisticUser)

    expect(transcript.hasTurn(optimisticUser)).toBe(true)
    expect(transcript.findTurnByServerId('missing')).toBeNull()
    expect(transcript.isLatestVisibleUserTurn(latestUser)).toBe(true)
    expect(transcript.isLatestVisibleAssistantTurn(latestAssistant)).toBe(true)
    expect(transcript.isLatestVisibleUserTurn(optimisticUser)).toBe(false)
    expect(transcript.isLatestVisibleUserTurn(transcript.findTurnByServerId('user-1')!)).toBe(false)
  })

  it('does not guess optimistic identity from matching text and timestamps', () => {
    const { transcript } = makeTranscript()
    const optimistic: ChatUserTurn = {
      id: 'local-user',
      role: 'user',
      text: 'hello',
      attachments: [],
      timestamp: '2026-01-01T00:00:00.000Z',
      streaming: false,
      isSelf: true,
      __optimistic: true,
    }
    transcript.appendToView(optimistic)

    transcript.replaceMessages([rawUser('server-user')], 'session-1')

    expect(transcript.messages[0]).toMatchObject({ id: 'server-user' })
  })

  it('rolls an optimistic tail back only while its original context is active', () => {
    const { transcript, sessionId } = makeTranscript()
    transcript.replaceMessages([
      rawUser('user-1'),
      rawAssistant('assistant-1', [{ id: 1, type: 'text', content: 'old' }]),
    ], 'session-1')
    const target = transcript.messages[1]!
    const optimistic = assistant('assistant-local')
    const replaced = transcript.replaceTailFromTurn(target, [optimistic])

    sessionId.value = 'session-2'
    transcript.restoreTailFromOptimistic('bot-1', 'session-1', null, optimistic, replaced)
    expect(toRaw(transcript.messages[1])).toBe(optimistic)

    sessionId.value = 'session-1'
    transcript.restoreTailFromOptimistic('bot-1', 'session-1', null, optimistic, replaced)
    expect(transcript.messages[1]?.id).toBe('assistant-1')
  })

  it('restores approval state after runtime projection replaces its block', () => {
    const { transcript } = makeTranscript()
    transcript.replaceMessages([rawAssistant('assistant-1', [approvalMessage()])], 'session-1')
    const turn = transcript.messages[0] as ChatAssistantTurn
    const block = turn.messages[0] as ToolCallBlock
    const snapshots = transcript.snapshotToolApprovalStates('approval-1')

    transcript.markToolApprovalDecision('approval-1', 'approved')
    const pendingProjection = rawAssistant('runtime-assistant', [approvalMessage('pending')])
    pendingProjection.turn_id = 'turn-assistant-1'
    transcript.applyRuntimeTranscript({
      runId: 'run-1',
      turnId: 'turn-assistant-1',
      status: 'waiting_decision',
      operation: null,
      turns: [pendingProjection],
      streaming: true,
    })
    const currentBlock = (transcript.messages[0] as ChatAssistantTurn).messages[0] as ToolCallBlock
    expect(currentBlock).not.toBe(block)
    transcript.markToolApprovalDecision('approval-1', 'approved')
    expect(currentBlock.approval?.status).toBe('approved')

    transcript.restoreToolApprovalStates(snapshots)
    expect(currentBlock.approval?.status).toBe('pending')
  })

  it('restores optimistic user-input state after runtime projection replaces its block', () => {
    const { transcript } = makeTranscript()
    const userInput = {
      user_input_id: 'input-1',
      status: 'pending',
      can_respond: true,
      questions: [{
        id: 'q1',
        text: 'Pick',
        kind: 'single_select' as const,
        options: [{ id: 'a', label: 'A' }],
      }],
    }
    const message: UIMessage = {
      id: 1,
      type: 'tool',
      name: 'ask_user',
      input: {},
      tool_call_id: 'call-input',
      running: false,
      user_input: userInput,
    }
    transcript.replaceMessages([rawAssistant('assistant-1', [message])], 'session-1')
    const block = (transcript.messages[0] as ChatAssistantTurn).messages[0] as ToolCallBlock
    const snapshots = transcript.snapshotUserInputStates('input-1')
    transcript.markUserInputDecision('input-1', 'submitted', [{ question_id: 'q1', option_ids: ['a'] }])
    expect(block.userInput).toMatchObject({
      status: 'submitted',
      can_respond: false,
      answers: [{ question_id: 'q1', question: 'Pick', selected: [{ id: 'a', label: 'A' }] }],
    })

    const pendingProjection = rawAssistant('runtime-assistant', [message])
    pendingProjection.turn_id = 'turn-assistant-1'
    transcript.applyRuntimeTranscript({
      runId: 'run-1',
      turnId: 'turn-assistant-1',
      status: 'waiting_decision',
      operation: null,
      turns: [pendingProjection],
      streaming: true,
    })
    const currentBlock = (transcript.messages[0] as ChatAssistantTurn).messages[0] as ToolCallBlock
    expect(currentBlock).not.toBe(block)
    transcript.markUserInputDecision('input-1', 'submitted', [{ question_id: 'q1', option_ids: ['a'] }])

    transcript.restoreUserInputStates(snapshots)

    expect(currentBlock.userInput).toMatchObject({ status: 'pending', can_respond: true })
    expect(currentBlock.userInput?.answers).toBeUndefined()
  })

  it('does not inject browser-memory stream errors into authoritative history', () => {
    const { transcript } = makeTranscript()
    transcript.replaceMessages([rawUser('user-1')], 'session-1')
    const failed = assistant('assistant-local', [{ id: 1, type: 'text', content: 'partial' }])
    failed.turnId = 'turn-user-1'
    transcript.appendToView(failed)
    transcript.finalizeStreamFailure(failed, 'bot-1', 'session-1', new Error('stream failed'))

    transcript.replaceMessages([rawUser('user-1')], 'session-1')
    expect(transcript.messages).toHaveLength(1)
  })

  it('routes completed tool messages through the fs mutation beacon', () => {
    const { transcript, bumpFsChangedAtIfFsMutation } = makeTranscript()
    const turn = assistant('assistant-1')
    const tool = approvalMessage()

    transcript.upsertAssistantUIMessage(turn, tool)

    expect(bumpFsChangedAtIfFsMutation).toHaveBeenCalledWith(tool)
  })

  it('normalizes and reconciles background-task turns without leaking tracker state', () => {
    const { transcript } = makeTranscript()
    const turns = transcript.normalizeTurns([
      rawAssistant('assistant-1', [{
        id: 1,
        type: 'tool',
        name: 'background',
        input: {},
        tool_call_id: 'call-bg',
        running: true,
        background_task: { task_id: 'task-1', status: 'running' },
      }]),
      {
        id: 'system-1',
        turn_id: 'turn-system-1',
        role: 'system',
        kind: 'background_task',
        timestamp: '2026-01-01T00:00:02.000Z',
        background_task: { task_id: 'task-1', status: 'completed' },
      },
    ] as UITurn[])

    const tool = (turns[0] as ChatAssistantTurn).messages[0] as ToolCallBlock
    expect(tool.backgroundTask?.status).toBe('completed')
    expect(tool.done).toBe(true)
  })

  it('rekeys an optimistic invocation from run acceptance before terminal refresh', async () => {
    const { transcript, fetchMessages } = makeTranscript()
    const assistantTurn = transcript.createOptimisticAssistantTurn('invocation-1')
    const userTurn = transcript.createOptimisticUserTurn('hello first', undefined, 'invocation-1')
    transcript.appendToView(userTurn, assistantTurn)

    transcript.bindRuntimeTurn('invocation-1', 'turn-1', 'run-1')
    transcript.applyRuntimeTranscript({
      runId: 'run-1',
      turnId: 'turn-1',
      status: 'running',
      operation: null,
      streaming: true,
      turns: [
        rawAssistant('runtime-assistant', []),
      ],
    })

    expect(transcript.messages.map(turn => turn.role)).toEqual(['user', 'assistant'])
    expect(transcript.messages[0]).toMatchObject({ text: 'hello first' })

    transcript.applyRuntimeTranscript({
      runId: 'run-1',
      turnId: 'turn-1',
      status: 'completed',
      operation: null,
      streaming: false,
      turns: [
        rawUser('runtime-user', 'hello first'),
        rawAssistant('runtime-assistant', []),
      ],
    })

    // The terminal REST snapshot is authoritative and replaces the runtime
    // projection without content or timestamp matching.
    const now = new Date().toISOString()
    fetchMessages.mockResolvedValueOnce([
      rawUser('server-user', 'hello first', now),
      rawAssistant('server-assistant', [], now),
    ])
    await transcript.refreshCurrentSession('bot-1', 'session-1')

    expect(transcript.messages.map(turn => turn.role)).toEqual(['user', 'assistant'])
    expect(transcript.messages.filter(turn => turn.role === 'user')).toHaveLength(1)
  })

  it('merges initial history and buffered runtime by authoritative turn id', async () => {
    const { transcript, fetchMessages } = makeTranscript()
    const historyUser = { ...rawUser('server-user'), turn_id: 'turn-1' }
    const historyAssistant = {
      ...rawAssistant('server-assistant', [{ id: 0, type: 'text', content: 'old' }]),
      turn_id: 'turn-1',
    }
    fetchMessages.mockResolvedValueOnce([historyUser, historyAssistant])
    const phases: string[] = []

    await transcript.loadInitialMessages('bot-1', 'session-1', async (applyHistory) => {
      expect(transcript.messages).toHaveLength(0)
      applyHistory()
      phases.push('history')
      phases.push('runtime')
      transcript.applyRuntimeTranscript({
        runId: 'run-1',
        turnId: 'turn-1',
        status: 'running',
        operation: null,
        streaming: true,
        turns: [
          { ...rawUser('runtime-user'), turn_id: 'turn-1' },
          {
            ...rawAssistant('runtime-assistant', [{ id: 0, type: 'text', content: 'streaming' }]),
            turn_id: 'turn-1',
          },
        ],
      })
    })
    phases.push('loaded')

    expect(phases).toEqual(['history', 'runtime', 'loaded'])
    expect(transcript.messages).toHaveLength(2)
    expect(transcript.messages.map(turn => turn.id)).toEqual(['server-user', 'server-assistant'])
    expect(transcript.messages[1]).toMatchObject({
      role: 'assistant',
      streaming: true,
      messages: [{ type: 'text', content: 'streaming' }],
    })
  })

  it('masks a cached transcript until rehydration commits', async () => {
    const { transcript, fetchMessages } = makeTranscript()
    transcript.replaceMessages([rawUser('cached-user', 'stale')], 'session-1')
    const pending = deferred<UITurn[]>()
    fetchMessages.mockReturnValueOnce(pending.promise)

    const hydration = transcript.loadInitialMessages(
      'bot-1',
      'session-1',
      async applyHistory => applyHistory(),
    )

    expect(transcript.messages.map(turn => turn.id)).toEqual(['cached-user'])
    expect(transcript.visibleMessages.value).toEqual([])

    pending.resolve([rawUser('fresh-user', 'fresh')])
    await hydration

    expect(transcript.visibleMessages.value.map(turn => turn.id)).toEqual(['fresh-user'])
  })

  it('replaces staged database history with an active edit projection', async () => {
    const { transcript, fetchMessages } = makeTranscript()
    fetchMessages.mockResolvedValueOnce([
      rawUser('server-user', 'original'),
      rawAssistant('server-assistant', [{ id: 0, type: 'text', content: 'old answer' }]),
    ])

    await transcript.loadInitialMessages('bot-1', 'session-1', async (applyHistory) => {
      expect(transcript.messages).toHaveLength(0)
      applyHistory()
      transcript.applyRuntimeTranscript({
        runId: 'run-edit',
        turnId: 'turn-edit',
        status: 'running',
        operation: {
          kind: 'edit',
          replace_from_message_id: 'server-user',
        },
        streaming: true,
        turns: [
          rawUser('runtime-user', 'edited'),
          rawAssistant('runtime-assistant'),
        ],
      })
    })

    expect(transcript.messages.map(turn => turn.id)).toEqual([
      'runtime-user',
      'runtime-assistant',
    ])
    expect(transcript.messages[0]).toMatchObject({ role: 'user', text: 'edited' })
  })

  it('applies a replacement operation that arrives after the admitting projection', () => {
    const { transcript } = makeTranscript()
    transcript.replaceMessages([
      rawUser('user-old'),
      rawAssistant('assistant-old', [{ id: 0, type: 'text', content: 'old answer' }]),
    ], 'session-1')

    transcript.applyRuntimeTranscript({
      runId: 'run-1',
      turnId: 'turn-1',
      status: 'admitting',
      operation: null,
      streaming: true,
      turns: [rawAssistant('runtime-assistant')],
    })
    transcript.applyRuntimeTranscript({
      runId: 'run-1',
      turnId: 'turn-1',
      status: 'running',
      operation: {
        kind: 'retry',
        replace_from_message_id: 'assistant-old',
      },
      streaming: true,
      turns: [rawAssistant('runtime-assistant', [
        { id: 0, type: 'text', content: 'new answer' },
      ])],
    })

    expect(transcript.messages.map(turn => turn.id)).toEqual(['user-old', 'runtime-assistant'])
    expect(transcript.messages[1]).toMatchObject({
      role: 'assistant',
      messages: [{ type: 'text', content: 'new answer' }],
    })
  })

  it('requires a history resync when a replacement anchor is missing', () => {
    const { transcript } = makeTranscript()
    transcript.replaceMessages([rawUser('user-old')], 'session-1')

    const applied = transcript.applyRuntimeTranscript({
      runId: 'run-1',
      turnId: 'turn-1',
      status: 'running',
      operation: {
        kind: 'edit',
        replace_from_message_id: 'missing-user',
      },
      streaming: true,
      turns: [
        rawUser('runtime-user', 'edited'),
        rawAssistant('runtime-assistant'),
      ],
    })

    expect(applied).toBe(false)
    expect(transcript.messages.map(turn => turn.id)).toEqual(['user-old'])
  })

  it('commits resynced history and its replacement projection in one render', async () => {
    const { transcript, fetchMessages } = makeTranscript()
    transcript.replaceMessages([rawUser('cached-user', 'stale')], 'session-1')
    fetchMessages.mockResolvedValueOnce([
      rawUser('server-user', 'original'),
      rawAssistant('server-assistant', [{ id: 0, type: 'text', content: 'old' }]),
    ])
    const renders: string[] = []
    const stop = watch(
      () => transcript.messages.map(turn => turn.id).join(','),
      value => renders.push(value),
    )

    await transcript.refreshCurrentSession('bot-1', 'session-1', () => {
      transcript.applyRuntimeTranscript({
        runId: 'run-edit',
        turnId: 'turn-edit',
        status: 'running',
        operation: {
          kind: 'edit',
          replace_from_message_id: 'server-user',
        },
        streaming: true,
        turns: [
          rawUser('runtime-user', 'edited'),
          rawAssistant('runtime-assistant'),
        ],
      })
    })
    await nextTick()
    stop()

    expect(renders).toEqual(['runtime-user,runtime-assistant'])
  })

  it('owns refresh state and reports the latest applied timestamp', async () => {
    const { transcript, fetchMessages } = makeTranscript()
    const onRefreshApplied = vi.fn()
    transcript.setRefreshAppliedHook(onRefreshApplied)
    fetchMessages.mockResolvedValueOnce([
      rawUser('user-1'),
      rawAssistant('assistant-1', [], '2026-01-01T00:00:02.000Z'),
    ])

    await transcript.loadInitialMessages('bot-1', 'session-1', async (applyHistory) => {
      applyHistory()
    })

    expect(transcript.loadingMessages.value).toBe(false)
    expect(transcript.hasMoreOlder.value).toBe(true)
    expect(onRefreshApplied).toHaveBeenCalledWith('session-1', '2026-01-01T00:00:02.000Z')
  })

  it('drops an older-page response that resolves after the active session changes', async () => {
    const { transcript, sessionId, fetchMessages } = makeTranscript()
    transcript.replaceHistoryView([rawUser('session-1-user')], 'session-1')
    const pending = deferred<UITurn[]>()
    fetchMessages.mockReturnValueOnce(pending.promise)

    const loading = transcript.loadOlderMessages()
    sessionId.value = 'session-2'
    transcript.clearHistoryView()
    transcript.replaceHistoryView([rawUser('session-2-user')], 'session-2')
    pending.resolve([rawUser('session-1-older', 'old', '2025-01-01T00:00:00.000Z')])

    expect(await loading).toBe(0)
    expect(transcript.messages.map(turn => turn.id)).toEqual(['session-2-user'])
    expect(transcript.loadingOlder.value).toBe(false)
  })

  it('drops a locate response that resolves after the active session changes', async () => {
    const { transcript, sessionId, locateMessage } = makeTranscript()
    const pending = deferred<{
      items: UITurn[]
      target_id: string
      target_external_message_id: string
    }>()
    locateMessage.mockReturnValueOnce(pending.promise)

    const locating = transcript.locateMessageByExternalId('external-1')
    sessionId.value = 'session-2'
    transcript.clearHistoryView()
    transcript.replaceHistoryView([rawUser('session-2-user')], 'session-2')
    pending.resolve({
      items: [{ ...rawUser('session-1-target'), external_message_id: 'external-1' } as UITurn],
      target_id: 'session-1-target',
      target_external_message_id: 'external-1',
    })

    expect(await locating).toBeNull()
    expect(transcript.messages.map(turn => turn.id)).toEqual(['session-2-user'])
    expect(transcript.hasLoadedOlder.value).toBe(false)
  })
})
