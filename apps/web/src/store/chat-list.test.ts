import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import type { BotSessionActivityEvent, SessionMessageStreamEvent, UIStreamEvent, UIStreamEventHandler, UIToolApproval, UIUserInput } from '@/composables/api/useChat'
import { REASONING_EFFORT_DISABLE } from '@/pages/bots/components/reasoning-effort'
import { AUTH_SESSION_CLEARED_EVENT } from '@/lib/auth-session'
import { useChatSelectionStore } from './chat-selection'
import { useChatStore } from './chat-list'

const api = vi.hoisted(() => ({
  createSession: vi.fn(),
  deleteSession: vi.fn(),
  forkSessionFromMessage: vi.fn(),
  fetchSession: vi.fn(),
  fetchSessions: vi.fn(),
  fetchBots: vi.fn(),
  fetchMessagesUI: vi.fn(),
  sendLocalChannelMessage: vi.fn(),
  executeQuickAction: vi.fn(),
  fetchSafeSkillCatalog: vi.fn(),
  updateSessionAgent: vi.fn(),
  ensureACPRuntime: vi.fn(),
  createACPRuntime: vi.fn(),
  fetchACPRuntimeByID: vi.fn(),
  setACPRuntimeModel: vi.fn(),
  setACPRuntimeModelByID: vi.fn(),
  setACPRuntimeReasoning: vi.fn(),
  setACPRuntimeReasoningByID: vi.fn(),
  closeACPRuntime: vi.fn(),
  streamSessionMessageEvents: vi.fn(),
  streamBotSessionsActivityEvents: vi.fn(),
  connectWebSocket: vi.fn(),
  locateMessageUI: vi.fn(),
}))

const toast = vi.hoisted(() => ({
  error: vi.fn(),
}))

const sdk = vi.hoisted(() => ({
  getBotsByBotIdSettings: vi.fn(),
}))

vi.hoisted(() => {
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: () => null,
      setItem: () => {},
      removeItem: () => {},
      clear: () => {},
    },
  })
})

vi.mock('@/composables/api/useChat', () => api)
vi.mock('@memohai/sdk', () => ({ getBotsByBotIdSettings: sdk.getBotsByBotIdSettings }))
vi.mock('vue-sonner', () => ({ toast }))
vi.mock('@felinic/ui', async (importOriginal) => {
  const original = await importOriginal<typeof import('@felinic/ui')>()
  return { ...original, toast }
})

function flushPromises() {
  return new Promise(resolve => setTimeout(resolve, 0))
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function applyLatestDraftRequest(store: ReturnType<typeof useChatStore>) {
  const request = store.draftViewRequested
  if (!request) throw new Error('Expected a Draft view request')
  store.applyDraftViewRequest(request, true)
}

async function applyLatestForkRequest(store: ReturnType<typeof useChatStore>) {
  const request = store.forkedSessionRequested
  if (!request) throw new Error('Expected a Forked Session request')
  await store.selectSession(request.sessionId)
}

function singleSelectUserInput(id = 'input-1'): UIUserInput {
  return {
    user_input_id: id,
    short_id: id === 'input-1' ? 4 : 5,
    status: 'pending',
    questions: [{
      id: 'q1',
      text: id === 'input-1' ? 'Which plan?' : 'Second question?',
      kind: 'single_select',
      options: [
        { id: 'q1.o1', label: id === 'input-1' ? 'Plan A' : 'Plan B' },
        { id: 'q1.o2', label: id === 'input-1' ? 'Plan B' : 'Plan C' },
      ],
    }],
    can_respond: true,
  }
}

function askUserTurn(userInput: UIUserInput, toolCallId = 'call-ask', blockId = 1) {
  return {
    id: 'assistant-1',
    role: 'assistant' as const,
    messages: [{
      id: blockId,
      type: 'tool' as const,
      name: 'ask_user',
      input: { questions: [{ text: userInput.questions?.[0]?.text ?? 'Question?', kind: 'single_select' }] },
      tool_call_id: toolCallId,
      toolCallId,
      toolName: 'ask_user',
      running: false,
      done: true,
      result: null,
      userInput,
    }],
    timestamp: new Date().toISOString(),
    streaming: false,
  }
}

function approvalTurn(approval: UIToolApproval, blockId = 1) {
  return {
    id: 'assistant-approval',
    role: 'assistant' as const,
    messages: [{
      id: blockId,
      type: 'tool' as const,
      name: 'exec',
      input: { command: 'pwd' },
      tool_call_id: 'call-pwd',
      toolCallId: 'call-pwd',
      toolName: 'exec',
      running: false,
      done: true,
      result: null,
      approval,
    }],
    timestamp: new Date().toISOString(),
    streaming: false,
  }
}

function richActiveRunStoreScript(sessionId = 'session-1', runId = 'run-rich'): UIStreamEvent[] {
  return [
    { type: 'start', run_id: runId, session_id: sessionId } as UIStreamEvent,
    {
      type: 'message',
      run_id: runId,
      session_id: sessionId,
      data: { id: 0, type: 'reasoning', content: 'I need to inspect the workspace.' },
    } as UIStreamEvent,
    {
      type: 'message',
      run_id: runId,
      session_id: sessionId,
      data: { id: 1, type: 'text', content: 'I will check the current state.' },
    } as UIStreamEvent,
    {
      type: 'message',
      run_id: runId,
      session_id: sessionId,
      data: {
        id: 2,
        type: 'tool',
        name: 'exec',
        tool_call_id: 'call-exec',
        input: { command: 'pwd' },
        running: true,
        progress: ['queued'],
      },
    } as UIStreamEvent,
    {
      type: 'message',
      run_id: runId,
      session_id: sessionId,
      data: {
        id: 2,
        type: 'tool',
        name: 'exec',
        tool_call_id: 'call-exec',
        input: { command: 'pwd' },
        output: { structuredContent: { stdout: '/workspace\n' } },
        running: false,
        progress: ['queued', { stdout: '/workspace\n' }],
      },
    } as UIStreamEvent,
    {
      type: 'message',
      run_id: runId,
      session_id: sessionId,
      data: {
        id: 3,
        type: 'tool',
        name: 'exec',
        tool_call_id: 'call-approval',
        input: { command: 'rm -rf build' },
        running: false,
        approval: {
          approval_id: 'approval-1',
          short_id: 7,
          status: 'pending',
          can_approve: true,
        },
      },
    } as UIStreamEvent,
    {
      type: 'message',
      run_id: runId,
      session_id: sessionId,
      data: {
        id: 4,
        type: 'tool',
        name: 'ask_user',
        tool_call_id: 'call-ask',
        input: { questions: [{ text: 'Continue?', kind: 'single_select' }] },
        running: false,
        user_input: {
          user_input_id: 'input-1',
          short_id: 8,
          status: 'pending',
          can_respond: true,
          questions: [{
            id: 'q1',
            text: 'Continue?',
            kind: 'single_select',
            options: [
              { id: 'yes', label: 'Yes' },
              { id: 'no', label: 'No' },
            ],
          }],
        },
      },
    } as UIStreamEvent,
  ]
}

function interruptedRunStoreScript(sessionId = 'session-1', runId = 'run-interrupted'): UIStreamEvent[] {
  return [
    { type: 'start', run_id: runId, session_id: sessionId } as UIStreamEvent,
    {
      type: 'message',
      run_id: runId,
      session_id: sessionId,
      data: { id: 0, type: 'text', content: 'partial output' },
    } as UIStreamEvent,
    { type: 'error', run_id: runId, session_id: sessionId, message: 'runtime interrupted' } as UIStreamEvent,
  ]
}

describe('chat-list store', () => {
  let streamHandler: UIStreamEventHandler | null
  // Captured but not driven by any test body yet; keep the capture so future
  // tests can simulate per-session SSE events without rewiring the mock.
  let _sessionMessageHandler: ((event: SessionMessageStreamEvent) => void) | null
  let sessionMessageHandlers: Map<string, (event: SessionMessageStreamEvent) => void>
  let sessionsActivityHandler: ((event: BotSessionActivityEvent) => void) | null
  let sendEvents: UIStreamEvent[]
  let sentWSMessages: Array<Record<string, unknown>>
  // wsRunIds[i] is the run the fake server minted for sentWSMessages[i].
  let wsRunIds: string[]
  let abortedWSRuns: string[]
  let lastRunId = ''
  let lastSessionId = ''
  let wsRunSeq = 0

  // The invocation the client minted for an outbound message, and the run the
  // fake server named in reply. Negative indexes count from the newest send.
  function wsInvocationId(index = 0): string {
    const message = index < 0 ? sentWSMessages.at(index) : sentWSMessages[index]
    return (message?.invocation_id as string | undefined) ?? ''
  }

  function wsRunId(index = 0): string {
    return (index < 0 ? wsRunIds.at(index) : wsRunIds[index]) ?? ''
  }

  function wsRunIdForType(type: string): string {
    const index = sentWSMessages.findIndex(message => message.type === type)
    return index < 0 ? '' : wsRunId(index)
  }

  beforeEach(() => {
    setActivePinia(createPinia())
    streamHandler = null
    _sessionMessageHandler = null
    sessionMessageHandlers = new Map()
    sessionsActivityHandler = null
    lastRunId = ''
    lastSessionId = ''
    wsRunSeq = 0
    sentWSMessages = []
    wsRunIds = []
    abortedWSRuns = []
    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      { type: 'error', message: 'model failed' } as UIStreamEvent,
    ]
    vi.clearAllMocks()

    api.fetchBots.mockResolvedValue([
      { id: 'bot-1', status: 'active', name: 'Bot' },
    ])
    api.fetchSessions.mockResolvedValue({ items: [], nextCursor: null })
    api.fetchSession.mockResolvedValue({
      id: 'session-unknown',
      bot_id: 'bot-1',
      title: 'Unknown session',
      type: 'chat',
    })
    api.createSession.mockResolvedValue({
      id: 'session-1',
      bot_id: 'bot-1',
      title: 'New session',
      type: 'chat',
    })
    api.updateSessionAgent.mockResolvedValue({
      id: 'session-1',
      bot_id: 'bot-1',
      title: '',
      type: 'acp_agent',
      metadata: {
        acp_agent_id: 'codex',
        project_path: '/data/app',
      },
    })
    api.ensureACPRuntime.mockResolvedValue({
      session_id: 'session-1',
      agent_id: 'codex',
      models: {
        current_model_id: 'gpt-5.1-codex',
        available_models: [{ id: 'gpt-5.1-codex', name: 'GPT-5.1 Codex' }],
      },
      reasoning: {
        current_effort: 'medium',
        available_efforts: [
          { id: 'medium', name: 'Medium' },
          { id: 'high', name: 'High' },
        ],
      },
    })
    api.createACPRuntime.mockResolvedValue({
      runtime_id: 'rt_warm',
      agent_id: 'codex',
      state: 'idle',
      default_model_id: 'gpt-5.1-codex',
      models: {
        current_model_id: 'gpt-5.1-codex',
        available_models: [
          { id: 'gpt-5.1-codex', name: 'GPT-5.1 Codex' },
          { id: 'gpt-5.1-codex-high', name: 'GPT-5.1 Codex High' },
        ],
      },
      reasoning: {
        current_effort: 'medium',
        available_efforts: [
          { id: 'medium', name: 'Medium' },
          { id: 'high', name: 'High' },
        ],
      },
    })
    api.fetchACPRuntimeByID.mockResolvedValue({
      runtime_id: 'rt_warm',
      agent_id: 'codex',
      state: 'idle',
      default_model_id: 'gpt-5.1-codex',
      models: {
        current_model_id: 'gpt-5.1-codex',
        available_models: [
          { id: 'gpt-5.1-codex', name: 'GPT-5.1 Codex' },
          { id: 'gpt-5.1-codex-high', name: 'GPT-5.1 Codex High' },
        ],
      },
      reasoning: {
        current_effort: 'medium',
        available_efforts: [
          { id: 'medium', name: 'Medium' },
          { id: 'high', name: 'High' },
        ],
      },
    })
    api.setACPRuntimeModel.mockResolvedValue({
      session_id: 'session-1',
      agent_id: 'codex',
      models: {
        current_model_id: 'gpt-5.1-codex-high',
        available_models: [{ id: 'gpt-5.1-codex-high', name: 'GPT-5.1 Codex High' }],
      },
    })
    api.setACPRuntimeModelByID.mockResolvedValue({
      runtime_id: 'rt_warm',
      agent_id: 'codex',
      state: 'idle',
      default_model_id: 'gpt-5.1-codex',
      models: {
        current_model_id: 'gpt-5.1-codex-high',
        available_models: [{ id: 'gpt-5.1-codex-high', name: 'GPT-5.1 Codex High' }],
      },
    })
    api.setACPRuntimeReasoning.mockResolvedValue({
      session_id: 'session-1',
      agent_id: 'codex',
      reasoning: {
        current_effort: 'low',
        available_efforts: [{ id: 'low', name: 'Low' }],
      },
    })
    api.setACPRuntimeReasoningByID.mockResolvedValue({
      runtime_id: 'rt_warm',
      agent_id: 'codex',
      state: 'idle',
      reasoning: {
        current_effort: 'high',
        available_efforts: [
          { id: 'medium', name: 'Medium' },
          { id: 'high', name: 'High' },
        ],
      },
    })
    api.closeACPRuntime.mockResolvedValue(undefined)
    api.fetchMessagesUI.mockResolvedValue([])
    api.executeQuickAction.mockResolvedValue(null)
    api.fetchSafeSkillCatalog.mockResolvedValue([])
    sdk.getBotsByBotIdSettings.mockResolvedValue({ data: { chat_runtime: 'model' } })
    api.streamSessionMessageEvents.mockImplementation((botId: string, targetSessionId: string, signal: AbortSignal, onEvent: (event: SessionMessageStreamEvent) => void) => new Promise<void>((resolve) => {
      _sessionMessageHandler = onEvent
      const key = `${botId}:${targetSessionId}`
      sessionMessageHandlers.set(key, onEvent)
      signal.addEventListener('abort', () => {
        if (sessionMessageHandlers.get(key) === onEvent) sessionMessageHandlers.delete(key)
        resolve()
      }, { once: true })
    }))
    api.streamBotSessionsActivityEvents.mockImplementation((_botId: string, signal: AbortSignal, onEvent: (event: BotSessionActivityEvent) => void) => new Promise<void>((resolve) => {
      sessionsActivityHandler = onEvent
      signal.addEventListener('abort', () => resolve(), { once: true })
    }))
    api.connectWebSocket.mockImplementation((_botId: string, onStreamEvent: UIStreamEventHandler) => {
      streamHandler = onStreamEvent
      return {
        get connected() {
          return true
        },
        send: vi.fn((message: { invocation_id?: string; session_id?: string }) => {
          sentWSMessages.push(message as Record<string, unknown>)
          lastSessionId = message.session_id ?? ''
          // The server names the run and announces it before any turn output,
          // so every later event is addressed by run_id.
          const invocationId = message.invocation_id ?? ''
          lastRunId = invocationId ? `run-${++wsRunSeq}` : ''
          wsRunIds.push(lastRunId)
          if (lastRunId) {
            onStreamEvent({
              type: 'run_accepted',
              run_id: lastRunId,
              invocation_id: invocationId,
              session_id: lastSessionId,
            } as UIStreamEvent)
          }
          for (const event of sendEvents) {
            onStreamEvent({
              ...event,
              run_id: lastRunId,
              session_id: lastSessionId,
            } as UIStreamEvent)
          }
        }),
        abort: vi.fn((runId: string) => {
          abortedWSRuns.push(runId)
        }),
        close: vi.fn(),
        onOpen: null,
        onClose: null,
      }
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('selects the first ready bot during initialization when none is selected', async () => {
    api.fetchBots.mockResolvedValueOnce([
      { id: 'bot-creating', status: 'creating', name: 'Creating' },
      { id: 'bot-ready', status: 'active', name: 'Ready' },
    ])

    const store = useChatStore()

    await store.initialize()

    expect(store.currentBotId).toBe('bot-ready')
    expect(api.fetchSessions).toHaveBeenCalledWith('bot-ready')
  })

  it('requests the Desktop once when each Browser Use or Computer Use call starts', async () => {
    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      {
        type: 'message',
        data: {
          id: 1,
          type: 'tool',
          name: 'browser_action',
          input: { action: 'click' },
          tool_call_id: 'call-browser',
          running: true,
        },
      } as UIStreamEvent,
    ]
    const store = useChatStore()
    await store.selectBot('bot-1')

    const sending = store.sendMessage('use the browser')
    await flushPromises()
    expect(store.guiToolUseRequested).toMatchObject({
      botId: 'bot-1',
      sessionId: 'session-1',
      toolCallId: 'call-browser',
      toolName: 'browser_action',
      seq: 1,
    })

    streamHandler?.({
      type: 'message',
      run_id: lastRunId,
      session_id: 'session-1',
      data: {
        id: 1,
        type: 'tool',
        name: 'browser_action',
        input: { action: 'click', coordinate: [10, 20] },
        tool_call_id: 'call-browser',
        running: true,
      },
    } as UIStreamEvent)
    expect(store.guiToolUseRequested?.seq).toBe(1)

    streamHandler?.({
      type: 'message',
      run_id: lastRunId,
      session_id: 'session-1',
      data: {
        id: 2,
        type: 'tool',
        name: 'computer_observe',
        input: { observe: 'snapshot' },
        tool_call_id: 'call-computer',
        running: true,
      },
    } as UIStreamEvent)
    expect(store.guiToolUseRequested).toMatchObject({
      toolCallId: 'call-computer',
      toolName: 'computer_observe',
      seq: 2,
    })

    streamHandler?.({
      type: 'end',
      run_id: lastRunId,
      session_id: 'session-1',
    } as UIStreamEvent)
    await expect(sending).resolves.toMatchObject({ ok: true })
  })

  it('returns startup stream errors to the composer when no assistant output exists', async () => {
    const store = useChatStore()
    const onBeforeTurnAppend = vi.fn()
    const onTurnAppendAborted = vi.fn()

    await store.selectBot('bot-1')
    const result = await store.sendMessage('hello', undefined, {
      onBeforeTurnAppend,
      onTurnAppendAborted,
      workspaceTargetId: 'computer-b',
    })

    expect(result).toMatchObject({
      ok: false,
      stage: 'startup',
      error: 'model failed',
      restoreInput: 'hello',
    })
    expect(store.messages).toHaveLength(0)
    expect(store.startupSendFailure).toMatchObject({
      botId: 'bot-1',
      sessionId: 'session-1',
      error: 'model failed',
      restoreInput: 'hello',
    })
    expect(onBeforeTurnAppend).toHaveBeenCalledOnce()
    expect(onTurnAppendAborted).toHaveBeenCalledOnce()
    expect(sentWSMessages.at(-1)).toMatchObject({
      type: 'message',
      workspace_target_id: 'computer-b',
    })
  })

  it('localizes structured WebSocket errors by stable code', async () => {
    const acpSession = {
      id: 'session-1',
      bot_id: 'bot-1',
      title: 'Codex',
      type: 'chat',
      session_mode: 'chat',
      runtime_type: 'acp_agent',
      runtime_metadata: {
        acp_agent_id: 'codex',
        project_path: '/data',
      },
    }
    api.fetchSessions.mockResolvedValue({ items: [acpSession], nextCursor: null })
    api.fetchSession.mockResolvedValue(acpSession)
    api.fetchMessagesUI.mockResolvedValue([])
    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      {
        type: 'error',
        message: 'backend fallback',
        feedback: {
          code: 'acp.config_update_failed',
          args: {},
          detail: 'backend fallback',
        },
      } as UIStreamEvent,
    ]
    const store = useChatStore()

    await store.selectBot('bot-1')
    await store.selectSession('session-1')
    const result = await store.sendMessage('hello')

    expect(result).toMatchObject({
      ok: false,
      stage: 'startup',
      error: 'The external agent could not apply the selected settings. Please retry.',
      errorCode: 'acp.config_update_failed',
    })
    expect(store.messages).toHaveLength(0)
    expect(store.startupSendFailure).toMatchObject({
      error: 'The external agent could not apply the selected settings. Please retry.',
      restoreInput: 'hello',
    })
  })

  it('uses structured API feedback for startup send failures', async () => {
    api.createSession.mockRejectedValueOnce({
      body: {
        i18n_key: 'chat.acp.agentNotConfigured',
        message: 'raw backend message',
      },
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    const result = await store.sendMessage('hello')

    expect(result).toMatchObject({
      ok: false,
      stage: 'startup',
      error: 'External agent setup is incomplete for this bot.',
      restoreInput: 'hello',
    })
    expect(store.startupSendFailure).toMatchObject({
      error: 'External agent setup is incomplete for this bot.',
      restoreInput: 'hello',
    })
  })

  it('handles /new codex in WebUI without sending it to the model', async () => {
    const store = useChatStore()

    await store.selectBot('bot-1')
    const result = await store.sendMessage('/new codex')
    applyLatestDraftRequest(store)

    expect(result.ok).toBe(true)
    expect(api.createSession).not.toHaveBeenCalled()
    expect(sentWSMessages).toHaveLength(0)
    expect(store.sessionId).toBeNull()
    expect(store.pendingACPSessionMetadata).toEqual({
      acp_agent_id: 'codex',
      project_path: '/data',
      acp_project_mode: 'project',
    })
  })

  it('handles /new codex from an existing session as a fresh ACP composer', async () => {
    sendEvents = [{ type: 'end' } as UIStreamEvent]
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Existing', type: 'chat' }],
      nextCursor: null,
    })
    api.createSession.mockResolvedValueOnce({
      id: 'acp-session-1',
      bot_id: 'bot-1',
      title: '',
      type: 'acp_agent',
      runtime_type: 'acp_agent',
      runtime_metadata: {
        acp_agent_id: 'codex',
        project_path: '/data',
        acp_project_mode: 'project',
      },
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.messages.push({
      id: 'existing-user',
      role: 'user',
      text: 'old message',
      attachments: [],
      timestamp: new Date().toISOString(),
      streaming: false,
      isSelf: true,
    })

    const commandResult = await store.sendMessage('/new codex')
    applyLatestDraftRequest(store)

    expect(commandResult.ok).toBe(true)
    expect(store.sessionId).toBeNull()
    expect(store.messages).toHaveLength(0)
    expect(store.pendingACPSessionMetadata?.acp_agent_id).toBe('codex')

    const sendResult = await store.sendMessage('hello codex')

    expect(sendResult.ok).toBe(true)
    expect(api.createSession).toHaveBeenCalledWith('bot-1', expect.objectContaining({
      type: 'chat',
      sessionMode: 'chat',
      runtimeType: 'acp_agent',
      runtimeMetadata: expect.objectContaining({ acp_agent_id: 'codex' }),
    }))
    expect(sentWSMessages.at(-1)).toMatchObject({
      session_id: 'acp-session-1',
      text: 'hello codex',
    })
  })

  it('handles /new chat codex in WebUI as a fresh ACP chat composer', async () => {
    const store = useChatStore()

    await store.selectBot('bot-1')
    const result = await store.sendMessage('/new chat codex')
    applyLatestDraftRequest(store)

    expect(result.ok).toBe(true)
    expect(sentWSMessages).toHaveLength(0)
    expect(store.sessionId).toBeNull()
    expect(store.pendingACPSessionMetadata?.acp_agent_id).toBe('codex')
    expect(store.activeChatTarget).toMatchObject({
      kind: 'draft-acp',
      runtimeType: 'acp_agent',
      isACP: true,
      isPendingACP: true,
      metadata: expect.objectContaining({ acp_agent_id: 'codex' }),
    })
  })

  it('keeps draft activation eligible for default ACP without clearing staged ACP', async () => {
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageDefaultACPSession({ agentId: 'codex', projectPath: '/data', projectMode: 'project' })
    store.selectDraft({ explicitSelection: false })

    expect(store.sessionId).toBeNull()
    expect(store.pendingACPSessionMetadata?.acp_agent_id).toBe('codex')
    expect(store.hasExplicitSessionSelection).toBe(false)
    expect(store.activeChatTarget).toMatchObject({
      kind: 'draft-acp',
      explicitSelection: false,
      runtimeType: 'acp_agent',
    })
  })

  it('restages the bot default ACP when opening a non-explicit draft after an ACP session', async () => {
    sdk.getBotsByBotIdSettings.mockResolvedValue({
      data: {
        chat_runtime: 'acp_agent',
        chat_acp_agent_id: 'codex',
        chat_acp_project_path: '/data',
        chat_acp_project_mode: 'project',
      },
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    await store.createACPSession({ agentId: 'codex' })

    expect(store.sessionId).toBe('session-1')
    expect(store.pendingACPSessionMetadata).toBeNull()
    expect(store.hasExplicitSessionSelection).toBe(true)
    sdk.getBotsByBotIdSettings.mockClear()

    store.selectDraft({ explicitSelection: false })

    expect(store.sessionId).toBeNull()
    expect(store.pendingACPSessionMetadata).toEqual({
      acp_agent_id: 'codex',
      project_path: '/data',
      acp_project_mode: 'project',
    })
    expect(store.hasExplicitSessionSelection).toBe(false)
    expect(sdk.getBotsByBotIdSettings).not.toHaveBeenCalled()
    expect(store.activeChatTarget).toMatchObject({
      kind: 'draft-acp',
      explicitSelection: false,
      metadata: expect.objectContaining({ acp_agent_id: 'codex' }),
    })
  })

  it('keeps an explicit draft as Memoh even when the bot default runtime is ACP', async () => {
    sdk.getBotsByBotIdSettings.mockResolvedValue({
      data: {
        chat_runtime: 'acp_agent',
        chat_acp_agent_id: 'codex',
      },
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    await store.createACPSession({ agentId: 'codex' })
    sdk.getBotsByBotIdSettings.mockClear()

    store.selectDraft({ explicitSelection: true })
    await flushPromises()

    expect(sdk.getBotsByBotIdSettings).not.toHaveBeenCalled()
    expect(store.sessionId).toBeNull()
    expect(store.pendingACPSessionMetadata).toBeNull()
    expect(store.hasExplicitSessionSelection).toBe(true)
    expect(store.activeChatTarget).toMatchObject({
      kind: 'draft-native',
      explicitSelection: true,
      runtimeType: 'model',
      isACP: false,
    })
  })

  it('treats bare /new as an explicit empty composer override', async () => {
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageDefaultACPSession({ agentId: 'codex', projectPath: '/data', projectMode: 'project' })
    const result = await store.sendMessage('/new')
    applyLatestDraftRequest(store)

    expect(result.ok).toBe(true)
    expect(store.sessionId).toBeNull()
    expect(store.pendingACPSessionMetadata).toBeNull()
    expect(store.hasExplicitSessionSelection).toBe(true)
  })

  it('uses matching default ACP project settings for /new codex', async () => {
    sdk.getBotsByBotIdSettings.mockResolvedValue({
      data: {
        chat_runtime: 'acp_agent',
        chat_acp_agent_id: 'codex',
        chat_acp_project_path: '/data/custom',
        chat_acp_project_mode: 'project',
      },
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    const result = await store.sendMessage('/new codex')
    applyLatestDraftRequest(store)

    expect(result.ok).toBe(true)
    expect(store.pendingACPSessionMetadata).toMatchObject({
      acp_agent_id: 'codex',
      project_path: '/data/custom',
      acp_project_mode: 'project',
    })
  })

  it('handles /new discuss codex in WebUI as a fresh ACP discuss composer', async () => {
    const store = useChatStore()

    await store.selectBot('bot-1')
    const result = await store.sendMessage('/new discuss codex')
    applyLatestDraftRequest(store)

    expect(result.ok).toBe(true)
    expect(sentWSMessages).toHaveLength(0)
    expect(store.sessionId).toBeNull()
    expect(store.pendingACPSessionMetadata?.acp_agent_id).toBe('codex')

    sendEvents = [{ type: 'end' } as UIStreamEvent]
    const sendResult = await store.sendMessage('start discuss')

    expect(sendResult.ok).toBe(true)
    expect(api.createSession).toHaveBeenCalledWith('bot-1', expect.objectContaining({
      type: 'discuss',
      sessionMode: 'discuss',
      runtimeType: 'acp_agent',
    }))
  })

  it('merges ACP approval tool messages into the existing tool block by call id', async () => {
    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      {
        type: 'message',
        data: {
          id: 1,
          type: 'tool',
          name: 'exec',
          input: { command: 'make test' },
          tool_call_id: 'mcp-http-call-1',
          running: true,
        },
      } as UIStreamEvent,
      {
        type: 'message',
        data: {
          id: 1000007,
          type: 'tool',
          name: 'exec',
          input: { command: 'make test' },
          tool_call_id: 'mcp-http-call-1',
          running: false,
          approval: {
            approval_id: 'approval-1',
            short_id: 7,
            status: 'pending',
            can_approve: true,
          },
        },
      } as UIStreamEvent,
      { type: 'error', message: 'stop after visible output' } as UIStreamEvent,
    ]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const result = await store.sendMessage('run command')

    expect(result).toMatchObject({ ok: false, stage: 'stream' })
    const assistant = store.messages.find(turn => turn.role === 'assistant')
    expect(assistant?.role).toBe('assistant')
    if (!assistant || assistant.role !== 'assistant') {
      throw new Error('assistant turn was not created')
    }
    expect(assistant.messages.filter(block => block.type === 'tool')).toHaveLength(1)
    const tool = assistant.messages.find(block => block.type === 'tool')
    expect(tool).toMatchObject({
      id: 1,
      type: 'tool',
      toolCallId: 'mcp-http-call-1',
      running: false,
      approval: {
        approval_id: 'approval-1',
        status: 'pending',
      },
    })
  })

  it('allows responding to ACP approval while the original stream is still active', async () => {
    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      {
        type: 'message',
        data: {
          id: 1,
          type: 'tool',
          name: 'exec',
          input: { command: 'pwd' },
          tool_call_id: 'call-pwd',
          running: false,
          approval: {
            approval_id: 'approval-pwd',
            short_id: 9,
            status: 'pending',
            can_approve: true,
          },
        },
      } as UIStreamEvent,
    ]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sendPromise = store.sendMessage('run pwd')
    await flushPromises()
    await flushPromises()

    expect(store.streaming).toBe(true)
    const initialMessageCount = store.messages.length
    const assistant = store.messages.find(turn => turn.role === 'assistant')
    if (!assistant || assistant.role !== 'assistant') {
      throw new Error('assistant turn was not created')
    }
    const tool = assistant.messages.find(block => block.type === 'tool')
    expect(tool).toMatchObject({
      toolCallId: 'call-pwd',
      approval: {
        approval_id: 'approval-pwd',
        status: 'pending',
      },
    })

    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      { type: 'end' } as UIStreamEvent,
    ]
    await store.respondToolApproval(tool!.approval!, 'approve')
    await flushPromises()

    expect(sentWSMessages.at(-1)).toMatchObject({
      type: 'tool_approval_response',
      session_id: 'session-1',
      approval_id: 'approval-pwd',
      decision: 'approve',
    })
    expect(store.messages).toHaveLength(initialMessageCount)
    const updatedAssistant = store.messages.find(turn => turn.role === 'assistant')
    if (!updatedAssistant || updatedAssistant.role !== 'assistant') {
      throw new Error('assistant turn was not found after approval')
    }
    const updatedTool = updatedAssistant.messages.find(block => block.type === 'tool')
    expect(updatedTool?.approval).toMatchObject({
      approval_id: 'approval-pwd',
      status: 'approved',
      can_approve: false,
    })

    const originalRunId = wsRunId(0)
    streamHandler?.({
      type: 'message',
      run_id: originalRunId,
      session_id: 'session-1',
      data: {
        id: 1,
        type: 'tool',
        name: 'exec',
        input: { command: 'pwd' },
        tool_call_id: 'call-pwd',
        running: false,
        approval: {
          approval_id: 'approval-pwd',
          short_id: 9,
          status: 'pending',
          can_approve: true,
        },
      },
    } as UIStreamEvent)
    const staleAssistant = store.messages.find(turn => turn.role === 'assistant')
    if (!staleAssistant || staleAssistant.role !== 'assistant') {
      throw new Error('assistant turn was not found after stale pending')
    }
    const staleTool = staleAssistant.messages.find(block => block.type === 'tool')
    expect(staleTool?.approval).toMatchObject({
      approval_id: 'approval-pwd',
      status: 'approved',
      can_approve: false,
    })

    streamHandler?.({ type: 'end', run_id: originalRunId, session_id: 'session-1' } as UIStreamEvent)
    await expect(sendPromise).resolves.toMatchObject({ ok: true })
  })

  it('rolls the optimistic approval back to pending when the response stream errors', async () => {
    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      {
        type: 'message',
        data: {
          id: 1,
          type: 'tool',
          name: 'exec',
          input: { command: 'pwd' },
          tool_call_id: 'call-pwd',
          running: false,
          approval: {
            approval_id: 'approval-pwd',
            short_id: 9,
            status: 'pending',
            can_approve: true,
          },
        },
      } as UIStreamEvent,
    ]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sendPromise = store.sendMessage('run pwd')
    await flushPromises()
    await flushPromises()

    const assistant = store.messages.find(turn => turn.role === 'assistant')
    if (!assistant || assistant.role !== 'assistant') {
      throw new Error('assistant turn was not created')
    }
    const tool = assistant.messages.find(block => block.type === 'tool')

    // The approval response stream fails before the server applies the decision.
    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      { type: 'error', message: 'approval failed' } as UIStreamEvent,
    ]
    await store.respondToolApproval(tool!.approval!, 'approve')
    await flushPromises()

    expect(toast.error).toHaveBeenCalledWith('approval failed')
    const rolledBackTool = assistant.messages.find(block => block.type === 'tool')
    expect(rolledBackTool?.approval).toMatchObject({
      approval_id: 'approval-pwd',
      status: 'pending',
      can_approve: true,
    })

    // The user can retry, and the retry goes through.
    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      { type: 'end' } as UIStreamEvent,
    ]
    const retried = await store.respondToolApproval(rolledBackTool!.approval!, 'approve')
    await flushPromises()

    expect(retried).toBe(true)
    const approvalResponses = sentWSMessages.filter(message => message.type === 'tool_approval_response')
    expect(approvalResponses).toHaveLength(2)
    const retriedTool = assistant.messages.find(block => block.type === 'tool')
    expect(retriedTool?.approval).toMatchObject({
      status: 'approved',
      can_approve: false,
    })

    const originalRunId = wsRunId(0)
    streamHandler?.({ type: 'end', run_id: originalRunId, session_id: 'session-1' } as UIStreamEvent)
    await expect(sendPromise).resolves.toMatchObject({ ok: true })
  })

  it('rolls back a detached approval turn when the response fails after switching sessions', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'session-1', bot_id: 'bot-1', title: 'Approval', type: 'chat' },
        { id: 'session-2', bot_id: 'bot-1', title: 'Other', type: 'chat' },
      ],
      nextCursor: null,
    })
    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      {
        type: 'message',
        data: {
          id: 1,
          type: 'tool',
          name: 'exec',
          input: { command: 'pwd' },
          tool_call_id: 'call-detached',
          running: false,
          approval: {
            approval_id: 'approval-detached',
            short_id: 12,
            status: 'pending',
            can_approve: true,
          },
        },
      } as UIStreamEvent,
    ]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sendPromise = store.sendMessage('run pwd')
    await flushPromises()
    const assistant = store.messages.find(turn => turn.role === 'assistant')
    if (!assistant || assistant.role !== 'assistant') throw new Error('approval turn was not streamed')
    const tool = assistant.messages.find(block => block.type === 'tool')
    if (!tool?.approval) throw new Error('approval block was not streamed')

    sendEvents = [{ type: 'start' } as UIStreamEvent]
    await store.respondToolApproval(tool.approval, 'approve')
    const responseRunId = wsRunId(-1)
    await store.selectSession('session-2')
    streamHandler?.({
      type: 'error',
      run_id: responseRunId,
      session_id: 'session-1',
      message: 'approval failed',
    } as UIStreamEvent)
    await flushPromises()
    await flushPromises()

    await store.selectSession('session-1')
    await flushPromises()
    await flushPromises()

    const assistantTurns = store.messages.filter(turn => turn.role === 'assistant')
    expect(assistantTurns).toHaveLength(1)
    expect(assistantTurns[0]).toBe(assistant)
    expect(assistant.streaming).toBe(true)
    expect(tool.approval).toMatchObject({
      approval_id: 'approval-detached',
      status: 'pending',
      can_approve: true,
    })

    store.abort()
    await expect(sendPromise).resolves.toMatchObject({ ok: false, stage: 'stream' })
    expect(assistant.streaming).toBe(false)
  })

  it('sends each ACP approval response only once while the response is in flight', async () => {
    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      {
        type: 'message',
        data: {
          id: 1,
          type: 'tool',
          name: 'exec',
          input: { command: 'pwd' },
          tool_call_id: 'call-pwd',
          running: false,
          approval: {
            approval_id: 'approval-pwd',
            short_id: 9,
            status: 'pending',
            can_approve: true,
          },
        },
      } as UIStreamEvent,
    ]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sendPromise = store.sendMessage('run pwd')
    await flushPromises()
    await flushPromises()

    const assistant = store.messages.find(turn => turn.role === 'assistant')
    if (!assistant || assistant.role !== 'assistant') {
      throw new Error('assistant turn was not created')
    }
    const tool = assistant.messages.find(block => block.type === 'tool')
    if (!tool?.approval) {
      throw new Error('tool approval was not created')
    }
    const approval = tool.approval

    sendEvents = [{ type: 'start' } as UIStreamEvent]
    await store.respondToolApproval(approval, 'approve')
    await store.respondToolApproval(approval, 'approve')
    await flushPromises()

    const approvalResponses = sentWSMessages.filter(message => message.type === 'tool_approval_response')
    expect(approvalResponses).toHaveLength(1)

    const approvalRunId = wsRunIdForType('tool_approval_response')
    streamHandler?.({ type: 'end', run_id: approvalRunId, session_id: 'session-1' } as UIStreamEvent)
    const originalRunId = wsRunId(0)
    streamHandler?.({ type: 'end', run_id: originalRunId, session_id: 'session-1' } as UIStreamEvent)
    await expect(sendPromise).resolves.toMatchObject({ ok: true })
  })

  it('aborts a visible approval response, rolls back its decision, and unlocks retry', async () => {
    sendEvents = [{ type: 'start' } as UIStreamEvent]
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    const store = useChatStore()

    await store.selectBot('bot-1')
    const approval: UIToolApproval = {
      approval_id: 'approval-pwd',
      short_id: 9,
      status: 'pending',
      can_approve: true,
    }
    store.messages.push(approvalTurn(approval))
    await expect(store.respondToolApproval(approval, 'approve')).resolves.toBe(true)
    const responseRunId = wsRunIdForType('tool_approval_response')
    const ws = api.connectWebSocket.mock.results.at(-1)?.value as { abort: ReturnType<typeof vi.fn> }

    store.abort()
    await flushPromises()

    expect(ws.abort).toHaveBeenCalledTimes(1)
    expect(ws.abort).toHaveBeenCalledWith(responseRunId)
    expect(store.streaming).toBe(false)
    const block = store.messages[0]?.role === 'assistant' ? store.messages[0].messages[0] : null
    expect(block?.type).toBe('tool')
    if (block?.type !== 'tool' || !block.approval) throw new Error('approval block missing')
    expect(block.approval).toMatchObject({ status: 'pending', can_approve: true })

    sendEvents = [{ type: 'start' } as UIStreamEvent, { type: 'end' } as UIStreamEvent]
    await expect(store.respondToolApproval(block.approval, 'approve')).resolves.toBe(true)
  })

  it('keeps the approved tool block visible while the response stream continues', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    sendEvents = [{ type: 'start' } as UIStreamEvent]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const approval: UIToolApproval = {
      approval_id: 'approval-pwd',
      short_id: 9,
      status: 'pending',
      can_approve: true,
    }
    store.messages.push(approvalTurn(approval, 0))

    await expect(store.respondToolApproval(approval, 'approve')).resolves.toBe(true)
    const responseRunId = wsRunId(-1)
    streamHandler?.({
      type: 'message',
      run_id: responseRunId,
      session_id: 'session-1',
      data: { id: 0, type: 'reasoning', content: 'Running the approved tool' },
    } as UIStreamEvent)

    const assistant = store.messages.find(turn => turn.role === 'assistant')
    if (!assistant || assistant.role !== 'assistant') throw new Error('assistant turn was not found')
    expect(assistant.messages.map(block => [block.id, block.type])).toEqual([
      [0, 'tool'],
      [1, 'reasoning'],
    ])
    const tool = assistant.messages.find(block => block.type === 'tool')
    expect(tool?.approval).toMatchObject({ status: 'approved', can_approve: false })

    streamHandler?.({ type: 'end', run_id: responseRunId, session_id: 'session-1' } as UIStreamEvent)
    await flushPromises()
  })

  it('aborts silent approval and original streams once and ignores late response events', async () => {
    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      {
        type: 'message',
        data: {
          id: 1,
          type: 'tool',
          name: 'exec',
          input: { command: 'pwd' },
          tool_call_id: 'call-pwd',
          running: false,
          approval: {
            approval_id: 'approval-pwd',
            short_id: 9,
            status: 'pending',
            can_approve: true,
          },
        },
      } as UIStreamEvent,
    ]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sending = store.sendMessage('run pwd')
    await flushPromises()
    const assistant = store.messages.find(turn => turn.role === 'assistant')
    if (!assistant || assistant.role !== 'assistant') throw new Error('assistant turn missing')
    const tool = assistant.messages.find(block => block.type === 'tool')
    if (!tool?.approval) throw new Error('approval block missing')
    const originalRunId = wsRunId(0)

    sendEvents = [{ type: 'start' } as UIStreamEvent]
    await expect(store.respondToolApproval(tool.approval, 'approve')).resolves.toBe(true)
    const responseRunId = wsRunIdForType('tool_approval_response')
    const ws = api.connectWebSocket.mock.results.at(-1)?.value as { abort: ReturnType<typeof vi.fn> }
    const messageCount = store.messages.length

    store.abort()
    await expect(sending).resolves.toMatchObject({ ok: false, stage: 'stream' })
    await flushPromises()

    expect(ws.abort).toHaveBeenCalledTimes(2)
    expect(ws.abort).toHaveBeenCalledWith(originalRunId)
    expect(ws.abort).toHaveBeenCalledWith(responseRunId)
    expect(tool.approval).toMatchObject({ status: 'pending', can_approve: true })
    expect(store.messages).toHaveLength(messageCount)

    streamHandler?.({
      type: 'message',
      run_id: responseRunId,
      session_id: 'session-1',
      data: { id: 99, type: 'text', content: 'late approval output' },
    } as UIStreamEvent)
    streamHandler?.({
      type: 'error',
      run_id: responseRunId,
      session_id: 'session-1',
      message: 'late approval error',
    } as UIStreamEvent)
    expect(store.messages).toHaveLength(messageCount)
    expect(toast.error).not.toHaveBeenCalledWith('late approval error')
  })

  it('does not optimistically submit tool approval while websocket is disconnected', async () => {
    api.connectWebSocket.mockImplementationOnce((_botId: string, _onStreamEvent: UIStreamEventHandler) => ({
      get connected() {
        return false
      },
      send: vi.fn((message: Record<string, unknown>) => {
        sentWSMessages.push(message)
      }),
      abort: vi.fn(),
      close: vi.fn(),
      onOpen: null,
      onClose: null,
    }))
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    const store = useChatStore()

    await store.selectBot('bot-1')
    const approval: UIToolApproval = {
      approval_id: 'approval-pwd',
      short_id: 9,
      status: 'pending',
      can_approve: true,
    }
    store.messages.push(approvalTurn(approval))

    const result = await store.respondToolApproval(approval, 'approve')
    await flushPromises()

    expect(result).toBe(false)
    expect(sentWSMessages).toHaveLength(0)
    expect(toast.error).toHaveBeenCalledWith('Connection lost. Reconnect and try again.')
    expect(store.messages).toHaveLength(1)
    const block = store.messages[0]?.role === 'assistant' ? store.messages[0].messages[0] : null
    expect(block?.type).toBe('tool')
    if (block?.type === 'tool') {
      expect(block.approval).toMatchObject({ status: 'pending', can_approve: true })
    }
  })

  it('rolls back tool approval optimistic state when websocket send throws', async () => {
    api.connectWebSocket.mockImplementationOnce((_botId: string, _onStreamEvent: UIStreamEventHandler) => ({
      get connected() {
        return true
      },
      send: vi.fn(() => {
        throw new Error('send failed')
      }),
      abort: vi.fn(),
      close: vi.fn(),
      onOpen: null,
      onClose: null,
    }))
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    const store = useChatStore()

    await store.selectBot('bot-1')
    const approval: UIToolApproval = {
      approval_id: 'approval-pwd',
      short_id: 9,
      status: 'pending',
      can_approve: true,
    }
    store.messages.push(approvalTurn(approval))

    const result = await store.respondToolApproval(approval, 'approve')
    await flushPromises()

    expect(result).toBe(false)
    expect(toast.error).toHaveBeenCalledWith('send failed')
    expect(store.messages).toHaveLength(1)
    const block = store.messages[0]?.role === 'assistant' ? store.messages[0].messages[0] : null
    expect(block?.type).toBe('tool')
    if (block?.type === 'tool') {
      expect(block.approval).toMatchObject({ status: 'pending', can_approve: true })
    }
  })

  it('creates ACP sessions without a placeholder title', async () => {
    api.createSession.mockResolvedValueOnce({
      id: 'acp-session-1',
      bot_id: 'bot-1',
      title: '',
      type: 'acp_agent',
      metadata: {
        acp_agent_id: 'codex',
        project_path: '/data/app',
      },
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    await store.createACPSession({
      agentId: 'codex',
      projectPath: '/data/app',
      projectMode: 'project',
    })

    expect(api.createSession).toHaveBeenLastCalledWith('bot-1', expect.objectContaining({
      title: '',
      type: 'chat',
      sessionMode: 'chat',
      runtimeType: 'acp_agent',
      metadata: {},
      runtimeMetadata: {
        acp_agent_id: 'codex',
        project_path: '/data/app',
        acp_project_mode: 'project',
      },
    }))
  })

  it('defaults new ACP sessions to the workspace root project', async () => {
    api.createSession.mockResolvedValueOnce({
      id: 'acp-session-1',
      bot_id: 'bot-1',
      title: '',
      type: 'acp_agent',
      metadata: {
        acp_agent_id: 'codex',
        project_path: '/data',
        acp_project_mode: 'project',
      },
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    await store.createACPSession({
      agentId: 'codex',
    })

    expect(api.createSession).toHaveBeenLastCalledWith('bot-1', expect.objectContaining({
      type: 'chat',
      sessionMode: 'chat',
      runtimeType: 'acp_agent',
      metadata: {},
      runtimeMetadata: {
        acp_agent_id: 'codex',
        project_path: '/data',
        acp_project_mode: 'project',
      },
    }))
  })

  it('defers ACP session creation until the first message is sent', async () => {
    sendEvents = [{ type: 'end' } as UIStreamEvent]
    api.createSession.mockResolvedValueOnce({
      id: 'acp-session-1',
      bot_id: 'bot-1',
      title: '',
      type: 'acp_agent',
      metadata: {
        acp_agent_id: 'codex',
        project_path: '/data',
        acp_project_mode: 'project',
      },
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })

    expect(api.createSession).not.toHaveBeenCalled()
    expect(store.sessionId).toBeNull()
    expect(store.pendingACPSessionMetadata).toEqual({
      acp_agent_id: 'codex',
      project_path: '/data',
      acp_project_mode: 'project',
    })

    const result = await store.sendMessage('hello codex')

    expect(result.ok).toBe(true)
    expect(api.createSession).toHaveBeenCalledTimes(1)
    expect(api.createSession).toHaveBeenCalledWith('bot-1', expect.objectContaining({
      type: 'chat',
      sessionMode: 'chat',
      runtimeType: 'acp_agent',
      metadata: {},
      runtimeMetadata: {
        acp_agent_id: 'codex',
        project_path: '/data',
        acp_project_mode: 'project',
      },
    }))
    expect(store.sessionId).toBe('acp-session-1')
    expect(store.pendingACPSessionMetadata).toBeNull()
    expect(sentWSMessages[0]).toMatchObject({
      session_id: 'acp-session-1',
      text: 'hello codex',
    })
  })

  it('keeps a pending default ACP stage across session list initialization refreshes', async () => {
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageDefaultACPSession({ agentId: 'codex', projectPath: '/data', projectMode: 'project' })

    api.fetchSessions.mockResolvedValueOnce({
      items: [{
        id: 'history-session-1',
        bot_id: 'bot-1',
        title: 'History',
        type: 'chat',
      }],
      nextCursor: null,
    })

    await store.initialize()

    expect(store.sessionId).toBeNull()
    expect(store.pendingACPSessionMetadata).toEqual({
      acp_agent_id: 'codex',
      project_path: '/data',
      acp_project_mode: 'project',
    })
    expect(store.hasExplicitSessionSelection).toBe(false)
    expect(api.createACPRuntime).not.toHaveBeenCalled()
  })

  it('allows default ACP staging to override a restored historical session selection', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{
        id: 'history-session-1',
        bot_id: 'bot-1',
        title: 'History',
        type: 'chat',
      }],
      nextCursor: null,
    })
    const selection = useChatSelectionStore()
    selection.setBot('bot-1')
    selection.setSession('history-session-1')
    const store = useChatStore()

    await store.initialize()

    expect(store.sessionId).toBe('history-session-1')
    expect(store.hasExplicitSessionSelection).toBe(false)

    store.stageDefaultACPSession({ agentId: 'codex', projectPath: '/data', projectMode: 'project' })

    expect(store.sessionId).toBeNull()
    expect(store.pendingACPSessionMetadata).toEqual({
      acp_agent_id: 'codex',
      project_path: '/data',
      acp_project_mode: 'project',
    })
    expect(store.hasExplicitSessionSelection).toBe(false)
  })

  it('does not restore an auto-picked historical session when default chat runtime is ACP', async () => {
    api.fetchSessions.mockResolvedValue({
      items: [{
        id: 'history-session-1',
        bot_id: 'bot-1',
        title: 'History',
        type: 'chat',
      }],
      nextCursor: null,
    })
    sdk.getBotsByBotIdSettings.mockResolvedValue({
      data: {
        chat_runtime: 'acp_agent',
        chat_acp_agent_id: 'codex',
      },
    })
    const selection = useChatSelectionStore()
    selection.setBot('bot-1')
    selection.setSession('history-session-1')
    const store = useChatStore()

    await store.initialize()
    await flushPromises()

    expect(sdk.getBotsByBotIdSettings).toHaveBeenCalled()
    expect(store.sessionId).toBeNull()
    expect(store.hasExplicitSessionSelection).toBe(false)
    expect(store.messages).toEqual([])
    expect(api.fetchMessagesUI).not.toHaveBeenCalled()
  })

  it('restores an explicitly selected historical session when default chat runtime is ACP', async () => {
    api.fetchSessions.mockResolvedValue({
      items: [{
        id: 'history-session-1',
        bot_id: 'bot-1',
        title: 'History',
        type: 'chat',
      }],
      nextCursor: null,
    })
    sdk.getBotsByBotIdSettings.mockResolvedValue({
      data: {
        chat_runtime: 'acp_agent',
        chat_acp_agent_id: 'codex',
      },
    })
    const selection = useChatSelectionStore()
    selection.setBot('bot-1')
    selection.setSession('history-session-1', { explicitSelection: true })
    const store = useChatStore()

    await store.initialize()

    expect(sdk.getBotsByBotIdSettings).toHaveBeenCalled()
    expect(store.sessionId).toBe('history-session-1')
    expect(store.hasExplicitSessionSelection).toBe(true)
    expect(store.pendingACPSessionMetadata).toBeNull()
  })

  it('hydrates an explicitly restored ACP session that is outside the first session page', async () => {
    api.fetchSessions.mockImplementation(async () => ({
      items: [{
        id: 'visible-session-1',
        bot_id: 'bot-1',
        title: 'Visible',
        type: 'chat',
        session_mode: 'chat',
        runtime_type: 'model',
      }],
      nextCursor: 'next-page',
    }))
    api.fetchSession.mockResolvedValueOnce({
      id: 'acp-session-hidden',
      bot_id: 'bot-1',
      title: 'Codex',
      type: 'chat',
      session_mode: 'chat',
      runtime_type: 'acp_agent',
      runtime_metadata: {
        acp_agent_id: 'codex',
        project_path: '/data',
        acp_project_mode: 'project',
      },
    })
    sdk.getBotsByBotIdSettings.mockResolvedValue({
      data: {
        chat_runtime: 'acp_agent',
        chat_acp_agent_id: 'codex',
      },
    })
    const selection = useChatSelectionStore()
    selection.setBot('bot-1')
    selection.setSession('acp-session-hidden', { explicitSelection: true })
    const store = useChatStore()

    await store.initialize()
    await flushPromises()

    expect(store.sessionId).toBe('acp-session-hidden')
    expect(api.fetchSession).toHaveBeenCalledWith('bot-1', 'acp-session-hidden')
    expect(store.hasExplicitSessionSelection).toBe(true)
    expect(store.activeSession).toMatchObject({
      id: 'acp-session-hidden',
      runtime_type: 'acp_agent',
      runtime_metadata: expect.objectContaining({ acp_agent_id: 'codex' }),
    })
    expect(store.activeChatTarget).toMatchObject({
      kind: 'session',
      sessionId: 'acp-session-hidden',
      runtimeType: 'acp_agent',
      isACP: true,
      metadata: expect.objectContaining({ acp_agent_id: 'codex' }),
    })
    expect(store.pendingACPSessionMetadata).toBeNull()
  })

  it('updates an early-read active target when a restored ACP session arrives in the first page', async () => {
    const sessionsResponse = {
      items: [{
        id: 'acp-session-visible',
        bot_id: 'bot-1',
        title: 'Codex visible',
        type: 'chat',
        session_mode: 'chat',
        runtime_type: 'acp_agent',
        runtime_metadata: {
          acp_agent_id: 'codex',
          project_path: '/data',
          acp_project_mode: 'project',
        },
      }],
      nextCursor: null,
    }
    let resolveSessions!: (value: typeof sessionsResponse) => void
    api.fetchSessions.mockImplementation(() => new Promise(resolve => {
      resolveSessions = resolve
    }))
    const selection = useChatSelectionStore()
    selection.setBot('bot-1')
    selection.setSession('acp-session-visible', { explicitSelection: true })
    const store = useChatStore()

    expect(store.activeChatTarget).toMatchObject({
      kind: 'session',
      sessionId: 'acp-session-visible',
      runtimeType: 'unknown',
      isACP: false,
    })

    await flushPromises()
    resolveSessions(sessionsResponse)
    await flushPromises()
    await flushPromises()

    expect(store.activeSession).toMatchObject({
      id: 'acp-session-visible',
      runtime_type: 'acp_agent',
    })
    expect(store.activeChatTarget).toMatchObject({
      kind: 'session',
      sessionId: 'acp-session-visible',
      runtimeType: 'acp_agent',
      isACP: true,
      metadata: expect.objectContaining({ acp_agent_id: 'codex' }),
    })
  })

  it('keeps an explicit empty Memoh composer across session list initialization refreshes', async () => {
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageDefaultACPSession({ agentId: 'codex', projectPath: '/data', projectMode: 'project' })
    store.resetToEmptyComposer({ explicitSelection: true })

    api.fetchSessions.mockResolvedValueOnce({
      items: [{
        id: 'history-session-1',
        bot_id: 'bot-1',
        title: 'History',
        type: 'chat',
      }],
      nextCursor: null,
    })

    await store.initialize()

    expect(store.sessionId).toBeNull()
    expect(store.pendingACPSessionMetadata).toBeNull()
    expect(store.hasExplicitSessionSelection).toBe(true)
    expect(api.createACPRuntime).not.toHaveBeenCalled()
  })

  it('keeps a manually staged ACP agent explicit so the default stage cannot reclaim it', async () => {
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageDefaultACPSession({ agentId: 'codex', projectPath: '/data', projectMode: 'project' })
    store.stageACPSession({ agentId: 'claude-code', projectPath: '/data/other', projectMode: 'project' })

    api.fetchSessions.mockResolvedValueOnce({
      items: [{
        id: 'history-session-1',
        bot_id: 'bot-1',
        title: 'History',
        type: 'chat',
      }],
      nextCursor: null,
    })

    await store.initialize()

    expect(store.sessionId).toBeNull()
    expect(store.pendingACPSessionMetadata).toEqual({
      acp_agent_id: 'claude-code',
      project_path: '/data/other',
      acp_project_mode: 'project',
    })
    expect(store.hasExplicitSessionSelection).toBe(true)
    expect(api.createACPRuntime).not.toHaveBeenCalled()
  })

  it('creates a warm runtime for the staged agent and binds it on first send', async () => {
    sendEvents = [{ type: 'end' } as UIStreamEvent]
    api.createSession.mockResolvedValueOnce({
      id: 'acp-session-1',
      bot_id: 'bot-1',
      title: '',
      type: 'acp_agent',
      metadata: {
        acp_agent_id: 'codex',
        project_path: '/data',
        acp_project_mode: 'project',
      },
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })
    await store.ensurePendingACPRuntime()

    // The runtime ID is server generated; the client never invents one.
    expect(api.createACPRuntime).toHaveBeenCalledWith('bot-1', expect.objectContaining({
      agentId: 'codex',
      projectPath: '/data',
    }))
    expect(store.pendingACPRuntimeId).toBe('rt_warm')
    expect(store.pendingACPRuntimeStatus?.models?.available_models).toHaveLength(2)

    await store.setPendingACPModel('gpt-5.1-codex-high')
    expect(store.pendingACPRuntimeStatus?.models?.current_model_id).toBe('gpt-5.1-codex-high')
    expect(api.setACPRuntimeModelByID).toHaveBeenCalledWith('bot-1', 'rt_warm', 'gpt-5.1-codex-high')

    await store.setPendingACPReasoning('high')
    expect(store.pendingACPRuntimeStatus?.reasoning?.current_effort).toBe('high')
    expect(api.setACPRuntimeReasoningByID).toHaveBeenCalledWith('bot-1', 'rt_warm', 'high')

    // Binding rides on session creation. The turn carries the selected model,
    // so send does not need another runtime setup request.
    const result = await store.sendMessage('hello codex', undefined, {
      modelId: 'gpt-5.1-codex-high',
      reasoningEffort: 'high',
    })

    expect(result.ok).toBe(true)
    expect(api.createSession).toHaveBeenCalledTimes(1)
    expect(api.createSession).toHaveBeenLastCalledWith('bot-1', expect.objectContaining({
      type: 'chat',
      sessionMode: 'chat',
      runtimeType: 'acp_agent',
      acpRuntimeId: 'rt_warm',
    }))
    expect(api.setACPRuntimeModel).not.toHaveBeenCalled()
    expect(api.closeACPRuntime).not.toHaveBeenCalled()
    expect(sentWSMessages[0]).toMatchObject({
      session_id: 'acp-session-1',
      reasoning_effort: 'high',
      text: 'hello codex',
      model_id: 'gpt-5.1-codex-high',
    })
  })

  it('refreshes a staged runtime instead of reusing a stale capability snapshot', async () => {
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })
    await store.ensurePendingACPRuntime()

    api.fetchACPRuntimeByID.mockResolvedValueOnce({
      runtime_id: 'rt_warm',
      agent_id: 'codex',
      state: 'idle',
      models: {
        current_model_id: 'gpt-5.1-codex-high',
        available_models: [{ id: 'gpt-5.1-codex-high', name: 'GPT-5.1 Codex High' }],
      },
      reasoning: {
        current_effort: 'xhigh',
        available_efforts: [{ id: 'xhigh', name: 'Extra high' }],
      },
    })

    const refreshed = await store.ensurePendingACPRuntime()

    expect(api.fetchACPRuntimeByID).toHaveBeenCalledWith('bot-1', 'rt_warm')
    expect(api.createACPRuntime).toHaveBeenCalledTimes(1)
    expect(refreshed?.models?.current_model_id).toBe('gpt-5.1-codex-high')
    expect(store.pendingACPRuntimeStatus?.reasoning?.current_effort).toBe('xhigh')
  })

  it('recreates a staged runtime when capability refresh reports it was reaped', async () => {
    api.createACPRuntime
      .mockResolvedValueOnce({
        runtime_id: 'rt_warm',
        agent_id: 'codex',
        state: 'idle',
        models: { current_model_id: 'gpt-5.1-codex', available_models: [] },
      })
      .mockResolvedValueOnce({
        runtime_id: 'rt_fresh',
        agent_id: 'codex',
        state: 'idle',
        models: { current_model_id: 'gpt-5.1-codex-high', available_models: [] },
      })
    api.fetchACPRuntimeByID.mockRejectedValueOnce({ body: { code: 'acp.runtime_not_found' } })
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })
    await store.ensurePendingACPRuntime()
    const recreated = await store.ensurePendingACPRuntime()

    expect(api.fetchACPRuntimeByID).toHaveBeenCalledWith('bot-1', 'rt_warm')
    expect(api.createACPRuntime).toHaveBeenCalledTimes(2)
    expect(recreated?.runtime_id).toBe('rt_fresh')
    expect(store.pendingACPRuntimeId).toBe('rt_fresh')
  })

  it('starts a new runtime when the agent changes while a create is in flight', async () => {
    let resolveFirst!: (value: unknown) => void
    api.createACPRuntime
      .mockImplementationOnce(() => new Promise((resolve) => {
        resolveFirst = resolve
      }))
      .mockResolvedValueOnce({
        runtime_id: 'rt_claude',
        agent_id: 'claude-code',
        state: 'idle',
        models: { current_model_id: 'claude-default', available_models: [] },
      })
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })
    const first = store.ensurePendingACPRuntime()

    // Switching agents mid-create must NOT reuse the codex create promise:
    // the new staging starts its own runtime immediately.
    store.stageACPSession({ agentId: 'claude-code' })
    const second = await store.ensurePendingACPRuntime()

    expect(api.createACPRuntime).toHaveBeenCalledTimes(2)
    expect(api.createACPRuntime).toHaveBeenLastCalledWith('bot-1', expect.objectContaining({
      agentId: 'claude-code',
    }))
    expect(store.pendingACPRuntimeId).toBe('rt_claude')
    expect(second?.runtime_id).toBe('rt_claude')

    // The late codex runtime is discarded, never adopted into claude staging.
    resolveFirst({
      runtime_id: 'rt_codex',
      agent_id: 'codex',
      state: 'idle',
      models: { current_model_id: 'gpt-5.1-codex', available_models: [] },
    })
    await first
    expect(api.closeACPRuntime).toHaveBeenCalledWith('bot-1', 'rt_codex')
    expect(store.pendingACPRuntimeId).toBe('rt_claude')
  })

  it('starts a new runtime when the project changes while a create is in flight', async () => {
    let resolveFirst!: (value: unknown) => void
    api.createACPRuntime
      .mockImplementationOnce(() => new Promise((resolve) => {
        resolveFirst = resolve
      }))
      .mockResolvedValueOnce({
        runtime_id: 'rt_other-project',
        agent_id: 'codex',
        state: 'idle',
        models: { current_model_id: 'gpt-5.1-codex', available_models: [] },
      })
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })
    const first = store.ensurePendingACPRuntime()

    store.stageACPSession({ agentId: 'codex', projectPath: '/data/other' })
    await store.ensurePendingACPRuntime()

    expect(api.createACPRuntime).toHaveBeenCalledTimes(2)
    expect(api.createACPRuntime).toHaveBeenLastCalledWith('bot-1', expect.objectContaining({
      projectPath: '/data/other',
    }))
    expect(store.pendingACPRuntimeId).toBe('rt_other-project')

    // The old project's runtime must not be accepted into the new staging.
    resolveFirst({
      runtime_id: 'rt_old-project',
      agent_id: 'codex',
      state: 'idle',
      models: { current_model_id: 'gpt-5.1-codex', available_models: [] },
    })
    await first
    expect(api.closeACPRuntime).toHaveBeenCalledWith('bot-1', 'rt_old-project')
    expect(store.pendingACPRuntimeId).toBe('rt_other-project')
  })

  it('ignores a stale create failure after staging changes', async () => {
    let rejectFirst!: (error: unknown) => void
    api.createACPRuntime
      .mockImplementationOnce(() => new Promise((_, reject) => {
        rejectFirst = reject
      }))
      .mockResolvedValueOnce({
        runtime_id: 'rt_claude',
        agent_id: 'claude-code',
        state: 'idle',
        models: { current_model_id: 'claude-default', available_models: [] },
      })
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })
    const first = store.ensurePendingACPRuntime()

    store.stageACPSession({ agentId: 'claude-code' })
    await store.ensurePendingACPRuntime()
    expect(store.pendingACPRuntimeId).toBe('rt_claude')

    rejectFirst({ message: 'codex create failed' })
    await expect(first).resolves.toBeUndefined()
    expect(store.pendingACPRuntimeId).toBe('rt_claude')
  })

  it('abandons a stale model heal when staging changes mid-flight', async () => {
    api.createACPRuntime
      .mockResolvedValueOnce({
        runtime_id: 'rt_warm',
        agent_id: 'codex',
        state: 'idle',
        models: { current_model_id: 'gpt-5.1-codex', available_models: [] },
      })
      .mockResolvedValueOnce({
        runtime_id: 'rt_claude',
        agent_id: 'claude-code',
        state: 'idle',
        models: { current_model_id: 'claude-default', available_models: [] },
      })
    let rejectPatch!: (error: unknown) => void
    api.setACPRuntimeModelByID.mockImplementationOnce(() => new Promise((_, reject) => {
      rejectPatch = reject
    }))
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })
    await store.ensurePendingACPRuntime()
    expect(store.pendingACPRuntimeId).toBe('rt_warm')

    // The model PATCH hangs; the user switches agents meanwhile.
    const pick = store.setPendingACPModel('gpt-5.1-codex-high')
    store.stageACPSession({ agentId: 'claude-code' })
    await store.ensurePendingACPRuntime()
    expect(store.pendingACPRuntimeId).toBe('rt_claude')

    // The old PATCH now fails with runtime-not-found: the heal must detect
    // the staging switch and exit silently — no recreate for the old
    // staging, no model PATCH against the claude runtime, no revert.
    rejectPatch({ message: 'runtime not found' })
    await pick

    expect(api.createACPRuntime).toHaveBeenCalledTimes(2)
    expect(api.setACPRuntimeModelByID).toHaveBeenCalledTimes(1)
    expect(store.pendingACPRuntimeId).toBe('rt_claude')
  })

  it('abandons a stale model heal when the same agent is re-staged mid-flight', async () => {
    api.createACPRuntime
      .mockResolvedValueOnce({
        runtime_id: 'rt_warm',
        agent_id: 'codex',
        state: 'idle',
        models: { current_model_id: 'gpt-5.1-codex', available_models: [] },
      })
      .mockResolvedValueOnce({
        runtime_id: 'rt_new',
        agent_id: 'codex',
        state: 'idle',
        models: { current_model_id: 'gpt-5.1-codex', available_models: [] },
      })
    let rejectPatch!: (error: unknown) => void
    api.setACPRuntimeModelByID.mockImplementationOnce(() => new Promise((_, reject) => {
      rejectPatch = reject
    }))
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })
    await store.ensurePendingACPRuntime()

    // ABA: pick hangs → user leaves ACP → re-stages the SAME agent. The
    // staging key matches again, but the model intent was reset, so the
    // late heal must not push the abandoned model onto the new runtime.
    const pick = store.setPendingACPModel('gpt-5.1-codex-high')
    store.clearPendingACPSession()
    store.stageACPSession({ agentId: 'codex' })
    await store.ensurePendingACPRuntime()
    expect(store.pendingACPRuntimeId).toBe('rt_new')

    rejectPatch({ message: 'runtime not found' })
    await pick

    expect(api.setACPRuntimeModelByID).toHaveBeenCalledTimes(1)
    expect(store.pendingACPRuntimeId).toBe('rt_new')
  })

  it('leaves staged runtime creation retryable when a model pick cannot start it', async () => {
    api.createACPRuntime.mockRejectedValueOnce({ message: 'runtime create failed' })
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })

    await expect(store.setPendingACPModel('gpt-5.1-codex-high')).rejects.toMatchObject({
      message: 'runtime create failed',
    })
    expect(store.pendingACPRuntimeId).toBe('')
  })

  it('recreates a reaped staged runtime when a model is picked after idling', async () => {
    api.createACPRuntime
      .mockResolvedValueOnce({
        runtime_id: 'rt_warm',
        agent_id: 'codex',
        state: 'idle',
        models: { current_model_id: 'gpt-5.1-codex', available_models: [] },
      })
      .mockResolvedValueOnce({
        runtime_id: 'rt_fresh',
        agent_id: 'codex',
        state: 'idle',
        models: { current_model_id: 'gpt-5.1-codex', available_models: [] },
      })
    api.setACPRuntimeModelByID
      .mockRejectedValueOnce({ body: { code: 'acp.runtime_not_found' } })
      .mockResolvedValueOnce({
        runtime_id: 'rt_fresh',
        agent_id: 'codex',
        state: 'idle',
        models: { current_model_id: 'gpt-5.1-codex-high', available_models: [] },
      })
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })
    await store.ensurePendingACPRuntime()
    expect(store.pendingACPRuntimeId).toBe('rt_warm')

    // rt_warm was idle-reaped server-side; the pick must heal transparently.
    await store.setPendingACPModel('gpt-5.1-codex-high')

    expect(api.createACPRuntime).toHaveBeenCalledTimes(2)
    expect(api.setACPRuntimeModelByID).toHaveBeenLastCalledWith('bot-1', 'rt_fresh', 'gpt-5.1-codex-high')
    expect(store.pendingACPRuntimeId).toBe('rt_fresh')
    expect(store.pendingACPRuntimeStatus?.models?.current_model_id).toBe('gpt-5.1-codex-high')
  })

  it('discards a staged runtime that finishes starting after the agent changed', async () => {
    let resolveCreate!: (value: unknown) => void
    api.createACPRuntime.mockImplementationOnce(() => new Promise((resolve) => {
      resolveCreate = resolve
    }))
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })
    const ensurePromise = store.ensurePendingACPRuntime()

    // The user clears the staged agent while the runtime is still starting.
    store.clearPendingACPSession()
    resolveCreate({
      runtime_id: 'rt_late',
      agent_id: 'codex',
      state: 'idle',
      models: { current_model_id: 'gpt-5.1-codex', available_models: [] },
    })
    await ensurePromise

    // The late runtime is closed instead of being adopted into empty staging.
    expect(store.pendingACPRuntimeId).toBe('')
    expect(api.closeACPRuntime).toHaveBeenCalledWith('bot-1', 'rt_late')
  })

  it('responds to user input over websocket and marks the block answered', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    sendEvents = [{ type: 'agent_end' } as UIStreamEvent]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const userInput = singleSelectUserInput()
    store.messages.push(askUserTurn(userInput))

    await store.respondUserInput(userInput, { answers: [{ question_id: 'q1', option_ids: ['q1.o1'] }] })
    await flushPromises()

    expect(sentWSMessages.at(-1)).toMatchObject({
      type: 'user_input_response',
      session_id: 'session-1',
      user_input_id: 'input-1',
      short_id: 4,
      answers: [{ question_id: 'q1', option_ids: ['q1.o1'] }],
      canceled: false,
    })
    const block = store.messages[0]?.role === 'assistant'
      ? store.messages[0].messages[0]
      : null
    expect(block?.type).toBe('tool')
    if (block?.type === 'tool') {
      expect(block.userInput?.status).toBe('submitted')
      expect(block.userInput?.can_respond).toBe(false)
    }
  })

  it('keeps the answered ask_user block visible while the response stream continues', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    sendEvents = [{ type: 'start' } as UIStreamEvent]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const userInput = singleSelectUserInput()
    store.messages.push(askUserTurn(userInput, 'call-ask', 0))

    await store.respondUserInput(userInput, { answers: [{ question_id: 'q1', option_ids: ['q1.o1'] }] })
    const responseRunId = wsRunId(-1)
    streamHandler?.({
      type: 'message',
      run_id: responseRunId,
      session_id: 'session-1',
      data: { id: 0, type: 'reasoning', content: 'Continuing after your answer' },
    } as UIStreamEvent)

    const assistant = store.messages.find(turn => turn.role === 'assistant')
    if (!assistant || assistant.role !== 'assistant') throw new Error('assistant turn was not found')
    expect(assistant.messages.map(block => [block.id, block.type])).toEqual([
      [0, 'tool'],
      [1, 'reasoning'],
    ])
    const askUser = assistant.messages.find(block => block.type === 'tool')
    expect(askUser?.userInput).toMatchObject({ status: 'submitted', can_respond: false })

    streamHandler?.({
      type: 'message',
      run_id: responseRunId,
      session_id: 'session-1',
      data: { id: 0, type: 'reasoning', content: 'Still continuing' },
    } as UIStreamEvent)
    expect(assistant.messages.map(block => [block.id, block.type])).toEqual([
      [0, 'tool'],
      [1, 'reasoning'],
    ])
    expect(assistant.messages[1]).toMatchObject({ content: 'Still continuing' })

    streamHandler?.({ type: 'end', run_id: responseRunId, session_id: 'session-1' } as UIStreamEvent)
    await flushPromises()
  })

  it('cancels user input over websocket and marks the block canceled', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    sendEvents = [{ type: 'agent_end' } as UIStreamEvent]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const userInput = singleSelectUserInput()
    store.messages.push(askUserTurn(userInput))

    await store.respondUserInput(userInput, { canceled: true, reason: 'user_canceled' })
    await flushPromises()

    expect(sentWSMessages.at(-1)).toMatchObject({
      type: 'user_input_response',
      session_id: 'session-1',
      user_input_id: 'input-1',
      short_id: 4,
      canceled: true,
      reason: 'user_canceled',
    })
    const block = store.messages[0]?.role === 'assistant'
      ? store.messages[0].messages[0]
      : null
    expect(block?.type).toBe('tool')
    if (block?.type === 'tool') {
      expect(block.userInput?.status).toBe('canceled')
      expect(block.userInput?.can_respond).toBe(false)
    }
  })

  it('does not optimistically submit user input while websocket is disconnected', async () => {
    api.connectWebSocket.mockImplementationOnce((_botId: string, _onStreamEvent: UIStreamEventHandler) => ({
      get connected() {
        return false
      },
      send: vi.fn((message: Record<string, unknown>) => {
        sentWSMessages.push(message)
      }),
      abort: vi.fn(),
      close: vi.fn(),
      onOpen: null,
      onClose: null,
    }))
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    const store = useChatStore()

    await store.selectBot('bot-1')
    const userInput = singleSelectUserInput()
    store.messages.push(askUserTurn(userInput))

    await store.respondUserInput(userInput, { answers: [{ question_id: 'q1', option_ids: ['q1.o1'] }] })
    await flushPromises()

    expect(sentWSMessages).toHaveLength(0)
    expect(toast.error).toHaveBeenCalledWith('Connection lost. Reconnect and try again.')
    expect(store.messages).toHaveLength(1)
    const block = store.messages[0]?.role === 'assistant'
      ? store.messages[0].messages[0]
      : null
    expect(block?.type).toBe('tool')
    if (block?.type === 'tool') {
      expect(block.userInput?.status).toBe('pending')
      expect(block.userInput?.can_respond).toBe(true)
    }
  })

  it('rolls back user input optimistic state when websocket send throws', async () => {
    api.connectWebSocket.mockImplementationOnce((_botId: string, _onStreamEvent: UIStreamEventHandler) => ({
      get connected() {
        return true
      },
      send: vi.fn(() => {
        throw new Error('send failed')
      }),
      abort: vi.fn(),
      close: vi.fn(),
      onOpen: null,
      onClose: null,
    }))
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    const store = useChatStore()

    await store.selectBot('bot-1')
    const userInput = singleSelectUserInput()
    store.messages.push(askUserTurn(userInput))

    await store.respondUserInput(userInput, { answers: [{ question_id: 'q1', option_ids: ['q1.o1'] }] })
    await flushPromises()

    expect(toast.error).toHaveBeenCalledWith('send failed')
    expect(store.messages).toHaveLength(1)
    const block = store.messages[0]?.role === 'assistant'
      ? store.messages[0].messages[0]
      : null
    expect(block?.type).toBe('tool')
    if (block?.type === 'tool') {
      expect(block.userInput?.status).toBe('pending')
      expect(block.userInput?.can_respond).toBe(true)
    }
  })

  it('responds to multi-select and text questions over websocket', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    sendEvents = [{ type: 'end' } as UIStreamEvent]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const userInput = {
      user_input_id: 'input-1',
      short_id: 4,
      status: 'pending',
      questions: [
        {
          id: 'q1',
          text: 'Which plans?',
          kind: 'multi_select' as const,
          options: [
            { id: 'q1.o1', label: 'Plan A' },
            { id: 'q1.o2', label: 'Plan B' },
          ],
        },
        {
          id: 'q2',
          text: 'Anything else?',
          kind: 'text' as const,
        },
      ],
      can_respond: true,
    }
    store.messages.push({
      id: 'assistant-1',
      role: 'assistant',
      messages: [{
        id: 1,
        type: 'tool',
        name: 'ask_user',
        input: { questions: [{ text: 'Which plans?', kind: 'multi_select' }] },
        tool_call_id: 'call-ask',
        toolCallId: 'call-ask',
        toolName: 'ask_user',
        running: false,
        done: true,
        result: null,
        userInput,
      }],
      timestamp: new Date().toISOString(),
      streaming: false,
    })

    await store.respondUserInput(userInput, {
      answers: [
        { question_id: 'q1', option_ids: ['q1.o1', 'q1.o2'] },
        { question_id: 'q2', text: 'nothing else' },
      ],
    })
    await flushPromises()

    expect(sentWSMessages.at(-1)).toMatchObject({
      type: 'user_input_response',
      session_id: 'session-1',
      user_input_id: 'input-1',
      answers: [
        { question_id: 'q1', option_ids: ['q1.o1', 'q1.o2'] },
        { question_id: 'q2', text: 'nothing else' },
      ],
      canceled: false,
    })
  })

  it('does not refresh a user input response stream while the original session stream is still active', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    sendEvents = [{ type: 'end' } as UIStreamEvent]
    const store = useChatStore()

    await store.selectBot('bot-1')
    api.fetchMessagesUI.mockClear()

    streamHandler?.({ type: 'start', run_id: 'run-main', session_id: 'session-1' } as UIStreamEvent)
    expect(store.isSessionStreaming('bot-1', 'session-1')).toBe(true)

    const userInput = singleSelectUserInput()
    store.messages.push(askUserTurn(userInput))

    await store.respondUserInput(userInput, { answers: [{ question_id: 'q1', option_ids: ['q1.o1'] }] })
    await flushPromises()

    expect(api.fetchMessagesUI).not.toHaveBeenCalled()

    streamHandler?.({
      type: 'message',
      run_id: 'run-main',
      session_id: 'session-1',
      data: {
        id: 2,
        type: 'tool',
        name: 'ask_user',
        input: { questions: [{ text: 'Second question?', kind: 'single_select' }] },
        tool_call_id: 'call-ask-2',
        running: false,
        user_input: singleSelectUserInput('input-2'),
      },
    } as UIStreamEvent)

    const hasSecondPendingInput = store.messages.some(message => message.role === 'assistant' && message.messages.some((block) => {
      return block.type === 'tool' && block.userInput?.user_input_id === 'input-2' && block.userInput.status === 'pending'
    }))
    expect(hasSecondPendingInput).toBe(true)
    expect(api.fetchMessagesUI).not.toHaveBeenCalled()

    streamHandler?.({ type: 'end', run_id: 'run-main', session_id: 'session-1' } as UIStreamEvent)
    await flushPromises()

    expect(api.fetchMessagesUI).toHaveBeenCalledTimes(1)
  })

  it('stamps session updated_at from the server message time, not the client clock or a reorder', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat', updated_at: '2026-01-01T00:00:00Z' },
      { id: 'session-2', bot_id: 'bot-1', title: 'B', type: 'chat', updated_at: '2026-01-02T00:00:00Z' },
    ], nextCursor: null })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    sessionsActivityHandler?.({
      type: 'session_touched',
      session_id: 'session-2',
      updated_at: '2026-01-03T00:00:00Z',
    })
    await flushPromises()

    const updated = store.sessions.find(session => session.id === 'session-2')
    expect(updated?.updated_at).toBe('2026-01-03T00:00:00Z')
    expect(store.sessions.map(session => session.id)).toEqual(['session-1', 'session-2'])
  })

  it('keeps unknown background task snapshots non-active when hydrating messages', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    api.fetchMessagesUI.mockResolvedValueOnce([{
      id: 'assistant-1',
      role: 'assistant',
      messages: [{
        id: 1,
        type: 'tool',
        name: 'spawn_agent',
        input: { id: 'agent-a' },
        tool_call_id: 'call-spawn',
        running: false,
        background_task: {
          task_id: 'bg-1',
          status: 'unknown',
          agent_id: 'agent-a',
          agent_session_id: 'child-1',
        },
      }],
      timestamp: new Date().toISOString(),
    }])
    const store = useChatStore()

    await store.selectBot('bot-1')

    const tool = store.messages[0]?.role === 'assistant'
      ? store.messages[0].messages[0]
      : null
    expect(tool?.type).toBe('tool')
    if (tool?.type === 'tool') {
      expect(tool.backgroundTask?.status).toBe('unknown')
      expect(tool.running).toBe(false)
      expect(tool.done).toBe(true)
    }
  })

  it('hydrates skill activation user turns after a page refresh when kind is omitted', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'skill-user-1',
        role: 'user',
        text: '/flutter-adding-home-screen-widgets',
        skill_activation: {
          skills: [{
            name: 'flutter-adding-home-screen-widgets',
            display_name: 'Flutter adding home screen widgets',
            source_kind: 'managed',
            state: 'effective',
          }],
        },
        attachments: [],
        timestamp: '2026-07-03T00:00:00.000Z',
      },
      {
        id: 'skill-user-2',
        role: 'user',
        text: '/flutter-adding-home-screen-widgets please add widgets',
        skill_activation: {
          skills: [{
            name: 'flutter-adding-home-screen-widgets',
            display_name: 'Flutter adding home screen widgets',
            source_kind: 'managed',
            state: 'effective',
          }],
        },
        attachments: [],
        timestamp: '2026-07-03T00:00:01.000Z',
      },
      {
        id: 'skill-user-3',
        role: 'user',
        text: 'The user activated the following skill for this turn without an additional prompt: Flutter adding home screen widgets.',
        skill_activation: {
          skills: [{
            name: 'flutter-adding-home-screen-widgets',
            display_name: 'Flutter adding home screen widgets',
            source_kind: 'managed',
            state: 'effective',
          }],
        },
        attachments: [],
        timestamp: '2026-07-03T00:00:02.000Z',
      },
    ])
    const store = useChatStore()

    await store.selectBot('bot-1')

    expect(store.messages).toHaveLength(3)
    expect(store.messages[0]).toMatchObject({
      id: 'skill-user-1',
      role: 'user',
      text: '',
      userMessageKind: 'skill_activation',
      skillActivation: {
        skills: [{
          name: 'flutter-adding-home-screen-widgets',
          display_name: 'Flutter adding home screen widgets',
        }],
      },
    })
    expect(store.messages[1]).toMatchObject({
      id: 'skill-user-2',
      role: 'user',
      text: 'please add widgets',
      userMessageKind: 'skill_activation',
      skillActivation: {
        skills: [{
          name: 'flutter-adding-home-screen-widgets',
          display_name: 'Flutter adding home screen widgets',
        }],
      },
    })
    expect(store.messages[2]).toMatchObject({
      id: 'skill-user-3',
      role: 'user',
      text: '',
      userMessageKind: 'skill_activation',
    })
  })

  it('applies live background task output and completion events to existing tool blocks', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    api.fetchMessagesUI.mockResolvedValueOnce([{
      id: 'assistant-1',
      role: 'assistant',
      messages: [{
        id: 1,
        type: 'tool',
        name: 'spawn_agent',
        input: { id: 'agent-a' },
        tool_call_id: 'call-spawn',
        running: true,
        background_task: {
          task_id: 'bg-1',
          status: 'running',
          agent_id: 'agent-a',
          agent_session_id: 'child-1',
        },
      }],
      timestamp: new Date().toISOString(),
    }])
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    _sessionMessageHandler?.({
      type: 'background_task',
      event: 'output',
      session_id: 'session-1',
      task: {
        task_id: 'bg-1',
        status: 'running',
        session_id: 'session-1',
        agent_id: 'agent-a',
        agent_session_id: 'child-1',
        chunk: 'first line\n',
      },
    } as never)

    let tool = store.messages[0]?.role === 'assistant'
      ? store.messages[0].messages[0]
      : null
    expect(tool?.type).toBe('tool')
    if (tool?.type === 'tool') {
      expect(tool.backgroundTask?.status).toBe('running')
      expect(tool.backgroundTask?.outputTail).toBe('first line\n')
      expect(tool.background_task?.output_tail).toBe('first line\n')
      expect(tool.running).toBe(true)
      expect(tool.done).toBe(false)
    }

    _sessionMessageHandler?.({
      type: 'background_task',
      event: 'completed',
      session_id: 'session-1',
      task: {
        task_id: 'bg-1',
        status: 'completed',
        session_id: 'session-1',
        agent_id: 'agent-a',
        agent_session_id: 'child-1',
        output_tail: 'final line\n',
      },
    } as never)

    tool = store.messages[0]?.role === 'assistant'
      ? store.messages[0].messages[0]
      : null
    expect(tool?.type).toBe('tool')
    if (tool?.type === 'tool') {
      expect(tool.backgroundTask?.status).toBe('completed')
      expect(tool.backgroundTask?.outputTail).toBe('final line\n')
      expect(tool.background_task?.output_tail).toBe('final line\n')
      expect(tool.running).toBe(false)
      expect(tool.done).toBe(true)
    }
  })

  it('updates remembered hidden session summaries from title events reactively', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'parent-1', bot_id: 'bot-1', title: 'Parent', type: 'chat' },
    ], nextCursor: null })
    api.fetchMessagesUI.mockResolvedValue([])
    api.fetchSession.mockResolvedValueOnce({
      id: 'session-subagent',
      bot_id: 'bot-1',
      title: 'Initial subagent title',
      type: 'subagent',
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await store.selectSession('session-subagent')
    await flushPromises()

    _sessionMessageHandler?.({
      type: 'session_title_updated',
      session_id: 'session-subagent',
      title: 'Updated subagent title',
    } as never)
    await flushPromises()

    expect(store.knownSessionSummary('session-subagent')?.title).toBe('Updated subagent title')
    expect(store.knownSessions.find(session => session.id === 'session-subagent')?.title).toBe('Updated subagent title')
  })

  it('keeps remembered hidden session title updates after the visible session list refreshes', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'parent-1', bot_id: 'bot-1', title: 'Parent', type: 'chat' },
    ], nextCursor: null })
    api.fetchMessagesUI.mockResolvedValue([])
    api.fetchSession.mockResolvedValueOnce({
      id: 'session-subagent',
      bot_id: 'bot-1',
      title: 'Initial subagent title',
      type: 'subagent',
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await store.selectSession('session-subagent')
    await flushPromises()

    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'parent-1', bot_id: 'bot-1', title: 'Parent', type: 'chat' },
    ], nextCursor: null })
    await store.initialize()
    await flushPromises()

    _sessionMessageHandler?.({
      type: 'session_title_updated',
      session_id: 'session-subagent',
      title: 'Updated subagent title',
    } as never)
    await flushPromises()

    expect(store.knownSessionSummary('session-subagent')?.title).toBe('Updated subagent title')
    expect(store.knownSessions.find(session => session.id === 'session-subagent')?.title).toBe('Updated subagent title')
  })

  it('refreshes recents when a remembered hidden chat session receives activity', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-visible', bot_id: 'bot-1', title: 'Visible', type: 'chat' },
    ], nextCursor: null })
    api.fetchSession.mockResolvedValueOnce({
      id: 'session-hidden',
      bot_id: 'bot-1',
      title: 'Hidden',
      type: 'chat',
      updated_at: '2026-06-01T00:00:00.000Z',
    })
    api.fetchMessagesUI.mockResolvedValue([])
    const store = useChatStore()
    await store.selectBot('bot-1')
    await store.selectSession('session-hidden')
    await flushPromises()

    expect(store.sessions.map(session => session.id)).toEqual(['session-visible'])
    expect(store.knownSessionSummary('session-hidden')).toMatchObject({
      id: 'session-hidden',
      type: 'chat',
    })

    api.fetchSessions.mockResolvedValueOnce({ items: [
      {
        id: 'session-hidden',
        bot_id: 'bot-1',
        title: 'Hidden',
        type: 'chat',
        updated_at: '2026-06-23T10:00:00.000Z',
      },
      { id: 'session-visible', bot_id: 'bot-1', title: 'Visible', type: 'chat' },
    ], nextCursor: null })
    sessionsActivityHandler?.({
      type: 'session_touched',
      session_id: 'session-hidden',
      updated_at: '2026-06-23T10:00:00.000Z',
    })
    await flushPromises()

    expect(api.fetchSessions).toHaveBeenCalledTimes(2)
    expect(store.sessions.map(session => session.id)).toEqual(['session-hidden', 'session-visible'])
  })

  it('refreshes pending user input after response stream failure', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    const store = useChatStore()

    await store.selectBot('bot-1')
    const userInput = singleSelectUserInput()
    store.messages.push(askUserTurn(userInput))
    api.fetchMessagesUI.mockResolvedValueOnce([{
      id: 'assistant-1',
      role: 'assistant',
      messages: [{
        id: 1,
        type: 'tool',
        name: 'ask_user',
        input: { questions: [{ text: 'Which plan?', kind: 'single_select' }] },
        tool_call_id: 'call-ask',
        running: false,
        user_input: userInput,
      }],
      timestamp: new Date().toISOString(),
    }])

    await store.respondUserInput(userInput, { answers: [{ question_id: 'q1', option_ids: ['q1.o1'] }] })
    await flushPromises()
    await flushPromises()

    const block = store.messages[0]?.role === 'assistant'
      ? store.messages[0].messages[0]
      : null
    expect(block?.type).toBe('tool')
    if (block?.type === 'tool') {
      expect(block.userInput?.status).toBe('pending')
      expect(block.userInput?.can_respond).toBe(true)
    }
  })

  it('rolls back user input on abort without refreshing the old session', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    sendEvents = [{ type: 'start' } as UIStreamEvent]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const userInput = singleSelectUserInput()
    store.messages.push(askUserTurn(userInput))
    api.fetchMessagesUI.mockClear()

    await store.respondUserInput(userInput, { answers: [{ question_id: 'q1', option_ids: ['q1.o1'] }] })
    const responseRunId = wsRunId(-1)
    const ws = api.connectWebSocket.mock.results.at(-1)?.value as { abort: ReturnType<typeof vi.fn> }
    store.abort()
    await flushPromises()

    expect(ws.abort).toHaveBeenCalledWith(responseRunId)
    expect(api.fetchMessagesUI).not.toHaveBeenCalled()
    expect(store.messages).toHaveLength(1)
    const block = store.messages[0]?.role === 'assistant' ? store.messages[0].messages[0] : null
    expect(block?.type).toBe('tool')
    if (block?.type === 'tool') {
      expect(block.userInput).toMatchObject({ status: 'pending', can_respond: true })
    }
  })

  it('does not refresh a previous bot after user-input teardown', async () => {
    api.fetchBots.mockResolvedValue([
      { id: 'bot-1', status: 'active', name: 'Bot 1' },
      { id: 'bot-2', status: 'active', name: 'Bot 2' },
    ])
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' },
    ], nextCursor: null })
    sendEvents = [{ type: 'start' } as UIStreamEvent]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const userInput = singleSelectUserInput()
    store.messages.push(askUserTurn(userInput))
    await store.respondUserInput(userInput, { answers: [{ question_id: 'q1', option_ids: ['q1.o1'] }] })
    api.fetchMessagesUI.mockClear()

    await store.selectBot('bot-2')
    await flushPromises()

    expect(store.currentBotId).toBe('bot-2')
    expect(api.fetchMessagesUI).not.toHaveBeenCalledWith(
      'bot-1',
      'session-1',
      expect.anything(),
    )
  })

  it('deduplicates concurrent ACP runtime ensure calls', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'acp-session-1', bot_id: 'bot-1', title: '', type: 'acp_agent' },
    ], nextCursor: null })
    let resolveRuntime!: (value: unknown) => void
    api.ensureACPRuntime.mockReturnValueOnce(new Promise(resolve => {
      resolveRuntime = resolve
    }))
    const store = useChatStore()

    await store.selectBot('bot-1')
    const first = store.ensureACPRuntime('acp-session-1')
    const second = store.ensureACPRuntime('acp-session-1')
    expect(api.ensureACPRuntime).toHaveBeenCalledTimes(1)

    resolveRuntime({
      session_id: 'acp-session-1',
      agent_id: 'codex',
      models: {
        current_model_id: 'gpt-5.1-codex',
        available_models: [{ id: 'gpt-5.1-codex', name: 'GPT-5.1 Codex' }],
      },
    })
    await Promise.all([first, second])

    expect(api.ensureACPRuntime).toHaveBeenCalledTimes(1)
    expect(store.acpRuntimeStatuses[store.acpRuntimeKey('bot-1', 'acp-session-1')]?.models?.available_models).toHaveLength(1)
  })

  it('clears ACP runtime state with the authenticated user scope', async () => {
    const windowTarget = new EventTarget()
    vi.stubGlobal('window', windowTarget)
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'acp-session-1', bot_id: 'bot-1', title: '', type: 'acp_agent' },
    ], nextCursor: null })
    const store = useChatStore()

    await store.selectBot('bot-1')
    await store.ensureACPRuntime('acp-session-1')
    const key = store.acpRuntimeKey('bot-1', 'acp-session-1')
    expect(store.acpRuntimeStatuses[key]).toBeDefined()

    windowTarget.dispatchEvent(new CustomEvent(AUTH_SESSION_CLEARED_EVENT, {
      detail: { reason: 'logout' },
    }))

    expect(store.acpRuntimeStatuses).toEqual({})
    expect(store.acpRuntimePending).toEqual({})
  })

  it('closes a staged ACP runtime with its owner bot on auth reset', async () => {
    const windowTarget = new EventTarget()
    vi.stubGlobal('window', windowTarget)
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })
    await store.ensurePendingACPRuntime()
    expect(store.pendingACPRuntimeId).toBe('rt_warm')

    windowTarget.dispatchEvent(new CustomEvent(AUTH_SESSION_CLEARED_EVENT, {
      detail: { reason: 'logout' },
    }))

    expect(api.closeACPRuntime).toHaveBeenCalledWith('bot-1', 'rt_warm')
    expect(store.pendingACPRuntimeId).toBe('')
  })

  it('does not restore bots from an initialization response after auth reset', async () => {
    const windowTarget = new EventTarget()
    vi.stubGlobal('window', windowTarget)
    const oldBots = deferred<Array<{ id: string; status: string; name: string }>>()
    api.fetchBots.mockReturnValueOnce(oldBots.promise)
    const store = useChatStore()

    const initializing = store.initialize()
    await flushPromises()
    windowTarget.dispatchEvent(new CustomEvent(AUTH_SESSION_CLEARED_EVENT, {
      detail: { reason: 'logout' },
    }))
    oldBots.resolve([{ id: 'old-user-bot', status: 'active', name: 'Old' }])
    await initializing

    expect(store.bots).toEqual([])
    expect(store.currentBotId).toBeNull()
    expect(api.fetchSessions).not.toHaveBeenCalledWith('old-user-bot')
  })

  it('does not apply a late bot refresh after auth reset', async () => {
    const windowTarget = new EventTarget()
    vi.stubGlobal('window', windowTarget)
    const store = useChatStore()
    await store.selectBot('bot-1')
    const oldRefresh = deferred<Array<{ id: string; status: string; name: string }>>()
    api.fetchBots.mockReturnValueOnce(oldRefresh.promise)

    const refreshing = store.refreshBots()
    windowTarget.dispatchEvent(new CustomEvent(AUTH_SESSION_CLEARED_EVENT, {
      detail: { reason: 'logout' },
    }))
    oldRefresh.resolve([{ id: 'old-user-bot', status: 'active', name: 'Old' }])
    await refreshing

    expect(store.bots).toEqual([])
    expect(store.currentBotId).toBeNull()
  })

  it('refreshes the session list when message events arrive for an unknown session', async () => {
    api.fetchSessions
      .mockResolvedValueOnce({ items: [
        { id: 'session-old', bot_id: 'bot-1', title: 'Old', type: 'chat' },
      ], nextCursor: null })
      .mockResolvedValueOnce({ items: [
        { id: 'session-new', bot_id: 'bot-1', title: 'New from channel', type: 'chat' },
        { id: 'session-old', bot_id: 'bot-1', title: 'Old', type: 'chat' },
      ], nextCursor: null })
    const store = useChatStore()

    await store.selectBot('bot-1')
    expect(store.sessionId).toBe('session-old')

    sessionsActivityHandler?.({
      type: 'session_touched',
      session_id: 'session-new',
      updated_at: '2026-06-02T10:00:00.000Z',
    })
    await flushPromises()

    expect(api.fetchSessions).toHaveBeenCalledTimes(2)
    expect(store.sessions.map(session => session.id)).toEqual(['session-new', 'session-old'])
    expect(store.sessionId).toBe('session-old')
  })

  it('renders stream errors in the chat transcript after assistant output starts', async () => {
    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      {
        type: 'message',
        data: { id: 0, type: 'text', content: 'partial response' },
      } as UIStreamEvent,
      { type: 'error', message: 'model failed' } as UIStreamEvent,
    ]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const result = await store.sendMessage('hello')

    expect(result).toMatchObject({ ok: false, stage: 'stream', error: 'model failed' })
    expect(store.messages).toHaveLength(2)
    expect(store.messages[0]).toMatchObject({ role: 'user', text: 'hello' })
    expect(store.messages[1]).toMatchObject({
      role: 'assistant',
      messages: [
        { type: 'text', content: 'partial response' },
        { type: 'error', content: 'model failed' },
      ],
      streaming: false,
    })
    expect(store.startupSendFailure).toBeNull()
  })

  it('aborts the active session stream through the websocket and settles the send', async () => {
    sendEvents = [{ type: 'start' } as UIStreamEvent]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sending = store.sendMessage('hello')
    await flushPromises()
    await flushPromises()

    const assistant = store.messages.find(turn => turn.role === 'assistant')
    const ws = api.connectWebSocket.mock.results.at(-1)?.value as { abort: ReturnType<typeof vi.fn> }
    expect(store.streaming).toBe(true)
    expect(assistant?.streaming).toBe(true)

    store.abort()

    await expect(sending).resolves.toMatchObject({ ok: false, stage: 'stream' })
    expect(ws.abort).toHaveBeenCalledWith(lastRunId)
    expect(store.streaming).toBe(false)
    expect(assistant?.streaming).toBe(false)
  })

  it('keeps an ephemeral error visible when refresh returns only the persisted user turn', async () => {
    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      {
        type: 'message',
        data: { id: 0, type: 'text', content: 'partial response' },
      } as UIStreamEvent,
      { type: 'error', message: 'model failed' } as UIStreamEvent,
    ]
    const store = useChatStore()

    await store.selectBot('bot-1')
    await store.sendMessage('hello')

    api.fetchMessagesUI.mockResolvedValueOnce([{
      role: 'user',
      id: 'server-user-1',
      text: 'hello',
      timestamp: '2026-05-17T08:00:00.000Z',
    }])
    streamHandler?.({ type: 'end', run_id: lastRunId, session_id: lastSessionId } as UIStreamEvent)
    await flushPromises()

    expect(store.messages).toHaveLength(2)
    expect(store.messages[0]).toMatchObject({ role: 'user', text: 'hello' })
    expect(store.messages[1]).toMatchObject({
      role: 'assistant',
      messages: [{ type: 'error', content: 'model failed' }],
      streaming: false,
    })
  })

  it('replaces the latest assistant immediately when retry starts', async () => {
    sendEvents = [{ type: 'start' } as UIStreamEvent]
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'user-1',
        role: 'user',
        text: 'hello',
        attachments: [],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'assistant-old',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'old answer' }],
        timestamp: '2026-05-17T08:00:01.000Z',
        streaming: false,
      },
    ])
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    const retry = store.retryLatestAssistant('assistant-old', { workspaceTargetId: 'computer-b' })
    await flushPromises()

    expect(sentWSMessages.at(-1)).toMatchObject({
      type: 'retry_message',
      session_id: 'session-1',
      message_id: 'assistant-old',
      workspace_target_id: 'computer-b',
    })
    expect(store.messages.map(message => message.id)).not.toContain('assistant-old')
    expect(store.messages.map(message => message.role)).toEqual(['user', 'assistant'])
    expect(store.messages[1]).toMatchObject({
      role: 'assistant',
      streaming: true,
      __optimistic: true,
    })

    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'user-1',
        role: 'user',
        text: 'hello',
        attachments: [],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'assistant-new',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'new answer' }],
        timestamp: '2026-05-17T08:00:02.000Z',
        streaming: false,
      },
    ])
    streamHandler?.({ type: 'end', run_id: lastRunId, session_id: lastSessionId } as UIStreamEvent)
    await retry
    await flushPromises()

    expect(store.messages.map(message => message.id)).toEqual(['user-1', 'assistant-new'])
  })

  it('moves fork divider anchor to the previous inherited assistant when retry replaces the fork anchor', async () => {
    sendEvents = [{ type: 'start' } as UIStreamEvent]
    api.fetchSessions.mockResolvedValueOnce({
      items: [{
        id: 'fork-session',
        bot_id: 'bot-1',
        title: 'Fork',
        type: 'chat',
        created_at: '2026-05-17T08:00:05.000Z',
        metadata: {
          forked_from: {
            session_id: 'source-session',
            title: 'Source',
            message_id: 'source-assistant',
            fork_message_id: 'assistant-old',
          },
        },
      }],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'user-1',
        role: 'user',
        text: 'first',
        attachments: [],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'assistant-prev',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'previous answer' }],
        timestamp: '2026-05-17T08:00:01.000Z',
        streaming: false,
      },
      {
        id: 'user-2',
        role: 'user',
        text: 'second',
        attachments: [],
        timestamp: '2026-05-17T08:00:02.000Z',
      },
      {
        id: 'assistant-old',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'old answer' }],
        timestamp: '2026-05-17T08:00:03.000Z',
        streaming: false,
      },
    ])
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    const retry = store.retryLatestAssistant('assistant-old')
    await flushPromises()

    expect(store.activeChatTarget.metadata.forked_from).toMatchObject({
      fork_message_id: 'assistant-prev',
    })

    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'user-1',
        role: 'user',
        text: 'first',
        attachments: [],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'assistant-prev',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'previous answer' }],
        timestamp: '2026-05-17T08:00:01.000Z',
        streaming: false,
      },
      {
        id: 'user-2',
        role: 'user',
        text: 'second',
        attachments: [],
        timestamp: '2026-05-17T08:00:02.000Z',
      },
      {
        id: 'assistant-new',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'new answer' }],
        timestamp: '2026-05-17T08:00:06.000Z',
        streaming: false,
      },
    ])
    streamHandler?.({ type: 'end', run_id: lastRunId, session_id: lastSessionId } as UIStreamEvent)
    await retry
    await flushPromises()

    expect(store.activeChatTarget.metadata.forked_from).toMatchObject({
      fork_message_id: 'assistant-prev',
    })
  })

  it('clears fork divider anchor when retry replaces the only inherited assistant', async () => {
    sendEvents = [{ type: 'start' } as UIStreamEvent]
    api.fetchSessions.mockResolvedValueOnce({
      items: [{
        id: 'fork-session',
        bot_id: 'bot-1',
        title: 'Fork',
        type: 'chat',
        created_at: '2026-05-17T08:00:05.000Z',
        metadata: {
          forked_from: {
            session_id: 'source-session',
            title: 'Source',
            message_id: 'source-assistant',
            fork_message_id: 'assistant-old',
          },
        },
      }],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'user-1',
        role: 'user',
        text: 'hello',
        attachments: [],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'assistant-old',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'old answer' }],
        timestamp: '2026-05-17T08:00:01.000Z',
        streaming: false,
      },
    ])
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    const retry = store.retryLatestAssistant('assistant-old')
    await flushPromises()

    expect(store.activeChatTarget.metadata.forked_from).toMatchObject({
      session_id: 'source-session',
      message_id: 'source-assistant',
    })
    expect((store.activeChatTarget.metadata.forked_from as Record<string, unknown>).fork_message_id).toBeUndefined()

    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'user-1',
        role: 'user',
        text: 'hello',
        attachments: [],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'assistant-new',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'new answer' }],
        timestamp: '2026-05-17T08:00:06.000Z',
        streaming: false,
      },
    ])
    streamHandler?.({ type: 'end', run_id: lastRunId, session_id: lastSessionId } as UIStreamEvent)
    await retry
    await flushPromises()

    expect((store.activeChatTarget.metadata.forked_from as Record<string, unknown>).fork_message_id).toBeUndefined()
  })

  it('moves fork divider anchor when edit replaces the fork anchor tail', async () => {
    sendEvents = [{ type: 'start' } as UIStreamEvent]
    api.fetchSessions.mockResolvedValueOnce({
      items: [{
        id: 'fork-session',
        bot_id: 'bot-1',
        title: 'Fork',
        type: 'chat',
        created_at: '2026-05-17T08:00:05.000Z',
        metadata: {
          forked_from: {
            session_id: 'source-session',
            title: 'Source',
            message_id: 'source-assistant',
            fork_message_id: 'assistant-old',
          },
        },
      }],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'user-1',
        role: 'user',
        text: 'first',
        attachments: [],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'assistant-prev',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'previous answer' }],
        timestamp: '2026-05-17T08:00:01.000Z',
        streaming: false,
      },
      {
        id: 'user-2',
        role: 'user',
        text: 'second',
        attachments: [],
        timestamp: '2026-05-17T08:00:02.000Z',
      },
      {
        id: 'assistant-old',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'old answer' }],
        timestamp: '2026-05-17T08:00:03.000Z',
        streaming: false,
      },
    ])
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    const edit = store.editLatestUser('user-2', 'edited second')
    await flushPromises()

    expect(store.activeChatTarget.metadata.forked_from).toMatchObject({
      fork_message_id: 'assistant-prev',
    })

    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'user-1',
        role: 'user',
        text: 'first',
        attachments: [],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'assistant-prev',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'previous answer' }],
        timestamp: '2026-05-17T08:00:01.000Z',
        streaming: false,
      },
      {
        id: 'user-new',
        role: 'user',
        text: 'edited second',
        attachments: [],
        timestamp: '2026-05-17T08:00:06.000Z',
      },
      {
        id: 'assistant-new',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'new answer' }],
        timestamp: '2026-05-17T08:00:07.000Z',
        streaming: false,
      },
    ])
    streamHandler?.({ type: 'end', run_id: lastRunId, session_id: lastSessionId } as UIStreamEvent)
    await edit
    await flushPromises()

    expect(store.activeChatTarget.metadata.forked_from).toMatchObject({
      fork_message_id: 'assistant-prev',
    })
  })

  it('restores the old assistant when retry fails before streaming starts', async () => {
    sendEvents = [{ type: 'error', message: 'model failed' } as UIStreamEvent]
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'user-1',
        role: 'user',
        text: 'hello',
        attachments: [],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'assistant-old',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'old answer' }],
        timestamp: '2026-05-17T08:00:01.000Z',
        streaming: false,
      },
    ])
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    const result = await store.retryLatestAssistant('assistant-old')

    expect(result).toMatchObject({ ok: false, stage: 'startup', error: 'model failed' })
    expect(store.messages.map(message => message.id)).toEqual(['user-1', 'assistant-old'])
  })

  it('does not restore a failed retry tail into a different active session', async () => {
    sendEvents = []
    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' },
        { id: 'session-b', bot_id: 'bot-1', title: 'B', type: 'chat' },
      ],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockImplementation((_botId: string, sessionId: string) => {
      if (sessionId === 'session-a') {
        return Promise.resolve([
          {
            id: 'user-a',
            role: 'user',
            text: 'hello',
            attachments: [],
            timestamp: '2026-05-17T08:00:00.000Z',
          },
          {
            id: 'assistant-old',
            role: 'assistant',
            messages: [{ id: 1, type: 'text', content: 'old answer' }],
            timestamp: '2026-05-17T08:00:01.000Z',
            streaming: false,
          },
        ])
      }
      if (sessionId === 'session-b') {
        return Promise.resolve([
          {
            id: 'user-b',
            role: 'user',
            text: 'other chat',
            attachments: [],
            timestamp: '2026-05-17T09:00:00.000Z',
          },
        ])
      }
      return Promise.resolve([])
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    const retry = store.retryLatestAssistant('assistant-old')
    await flushPromises()
    expect(store.messages.map(message => message.id)).toEqual(['user-a', expect.any(String)])
    const retryRunId = lastRunId

    await store.selectSession('session-b')
    await flushPromises()
    expect(store.messages.map(message => message.id)).toEqual(['user-b'])

    streamHandler?.({
      type: 'error',
      run_id: retryRunId,
      session_id: 'session-a',
      message: 'model failed',
    } as UIStreamEvent)
    const result = await retry
    await flushPromises()

    expect(result).toMatchObject({ ok: false, stage: 'startup', error: 'model failed' })
    expect(store.sessionId).toBe('session-b')
    expect(store.messages.map(message => message.id)).toEqual(['user-b'])
  })

  it('replaces the latest user turn tail immediately when edit starts', async () => {
    sendEvents = [{ type: 'start' } as UIStreamEvent]
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'user-1',
        role: 'user',
        text: 'old prompt',
        attachments: [],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'assistant-old',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'old answer' }],
        timestamp: '2026-05-17T08:00:01.000Z',
        streaming: false,
      },
    ])
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    const edit = store.editLatestUser('user-1', 'new prompt', { workspaceTargetId: 'computer-a' })
    await flushPromises()

    expect(sentWSMessages.at(-1)).toMatchObject({
      type: 'edit_message',
      session_id: 'session-1',
      message_id: 'user-1',
      text: 'new prompt',
      workspace_target_id: 'computer-a',
    })
    expect(store.messages.map(message => message.id)).not.toContain('user-1')
    expect(store.messages.map(message => message.id)).not.toContain('assistant-old')
    expect(store.messages.map(message => message.role)).toEqual(['user', 'assistant'])
    expect(store.messages[0]).toMatchObject({
      role: 'user',
      text: 'new prompt',
      __optimistic: true,
    })
    expect(store.messages[1]).toMatchObject({
      role: 'assistant',
      streaming: true,
      __optimistic: true,
    })

    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'user-new',
        role: 'user',
        text: 'new prompt',
        attachments: [],
        timestamp: '2026-05-17T08:00:02.000Z',
      },
      {
        id: 'assistant-new',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'new answer' }],
        timestamp: '2026-05-17T08:00:03.000Z',
        streaming: false,
      },
    ])
    streamHandler?.({ type: 'end', run_id: lastRunId, session_id: lastSessionId } as UIStreamEvent)
    await edit
    await flushPromises()

    expect(store.messages.map(message => message.id)).toEqual(['user-new', 'assistant-new'])
  })

  it('restores the old latest turn tail when edit fails before streaming starts', async () => {
    sendEvents = [{ type: 'error', message: 'model failed' } as UIStreamEvent]
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'user-1',
        role: 'user',
        text: 'old prompt',
        attachments: [],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'assistant-old',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'old answer' }],
        timestamp: '2026-05-17T08:00:01.000Z',
        streaming: false,
      },
    ])
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    const result = await store.editLatestUser('user-1', 'new prompt')

    expect(result).toMatchObject({
      ok: false,
      stage: 'startup',
      error: 'model failed',
      restoreInput: 'new prompt',
    })
    expect(store.messages.map(message => message.id)).toEqual(['user-1', 'assistant-old'])
  })

  it('does not restore a failed edit tail into a different active session', async () => {
    sendEvents = []
    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' },
        { id: 'session-b', bot_id: 'bot-1', title: 'B', type: 'chat' },
      ],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockImplementation((_botId: string, sessionId: string) => {
      if (sessionId === 'session-a') {
        return Promise.resolve([
          {
            id: 'user-a',
            role: 'user',
            text: 'old prompt',
            attachments: [],
            timestamp: '2026-05-17T08:00:00.000Z',
          },
          {
            id: 'assistant-a',
            role: 'assistant',
            messages: [{ id: 1, type: 'text', content: 'old answer' }],
            timestamp: '2026-05-17T08:00:01.000Z',
            streaming: false,
          },
        ])
      }
      if (sessionId === 'session-b') {
        return Promise.resolve([
          {
            id: 'user-b',
            role: 'user',
            text: 'other chat',
            attachments: [],
            timestamp: '2026-05-17T09:00:00.000Z',
          },
        ])
      }
      return Promise.resolve([])
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    const edit = store.editLatestUser('user-a', 'new prompt')
    await flushPromises()
    expect(store.messages.map(message => message.role)).toEqual(['user', 'assistant'])
    expect(store.messages[0]).toMatchObject({ role: 'user', text: 'new prompt' })
    const editRunId = lastRunId

    await store.selectSession('session-b')
    await flushPromises()
    expect(store.messages.map(message => message.id)).toEqual(['user-b'])

    streamHandler?.({
      type: 'error',
      run_id: editRunId,
      session_id: 'session-a',
      message: 'model failed',
    } as UIStreamEvent)
    const result = await edit
    await flushPromises()

    expect(result).toMatchObject({
      ok: false,
      stage: 'startup',
      error: 'model failed',
      restoreInput: 'new prompt',
    })
    expect(store.sessionId).toBe('session-b')
    expect(store.messages.map(message => message.id)).toEqual(['user-b'])
  })

  it('does not edit a latest user turn with attachments until attachment preservation is supported', async () => {
    sendEvents = []
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'user-1',
        role: 'user',
        text: 'old prompt',
        attachments: [{
          content_hash: 'hash-1',
          role: 'user',
          ordinal: 0,
          mime: 'image/png',
          size_bytes: 12,
          storage_key: 'asset-1',
          name: 'image.png',
        }],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'assistant-old',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'old answer' }],
        timestamp: '2026-05-17T08:00:01.000Z',
        streaming: false,
      },
    ])
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    const result = await store.editLatestUser('user-1', 'new prompt')

    expect(result).toMatchObject({ ok: false, stage: 'startup' })
    expect(sentWSMessages).toHaveLength(0)
    expect(store.messages.map(message => message.id)).toEqual(['user-1', 'assistant-old'])
    expect(store.messages[0]).toMatchObject({
      role: 'user',
      attachments: [expect.objectContaining({ content_hash: 'hash-1' })],
    })
  })

  it('keeps fork source anchored to the copied message after switching to the fork session', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'source-session', bot_id: 'bot-1', title: 'Source', type: 'chat' }],
      nextCursor: null,
    })
    const sourceTurns = [
      {
        id: 'source-user',
        role: 'user' as const,
        text: 'hello',
        attachments: [],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'source-assistant',
        role: 'assistant' as const,
        messages: [{ id: 1, type: 'text' as const, content: 'answer' }],
        timestamp: '2026-05-17T08:00:01.000Z',
        streaming: false,
      },
    ]
    const forkTurns = [
      {
        id: 'fork-user',
        role: 'user' as const,
        text: 'hello',
        attachments: [],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'fork-assistant',
        role: 'assistant' as const,
        messages: [{ id: 1, type: 'text' as const, content: 'answer' }],
        timestamp: '2026-05-17T08:00:01.000Z',
        streaming: false,
      },
    ]
    api.fetchMessagesUI.mockImplementation((_botId: string, sessionId: string) => {
      if (sessionId === 'source-session') return Promise.resolve(sourceTurns)
      if (sessionId === 'fork-session') return Promise.resolve(forkTurns)
      return Promise.resolve([])
    })
    api.forkSessionFromMessage.mockResolvedValueOnce({
      id: 'fork-session',
      bot_id: 'bot-1',
      title: 'Source fork',
      type: 'chat',
      metadata: {
        forked_from: {
          session_id: 'source-session',
          title: 'Source',
          message_id: 'source-assistant',
          fork_message_id: 'fork-final-raw-message',
        },
      },
    })
    api.fetchSessions.mockResolvedValueOnce({
      items: [{
        id: 'fork-session',
        bot_id: 'bot-1',
        title: 'Source fork',
        type: 'chat',
        metadata: {
          forked_from: {
            session_id: 'source-session',
            title: 'Source',
            message_id: 'source-assistant',
          },
        },
      }],
      nextCursor: null,
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    const ok = await store.forkMessage('source-assistant', { title: 'Custom fork name' })
    await applyLatestForkRequest(store)
    await flushPromises()

    expect(ok).toBe(true)
    expect(api.forkSessionFromMessage).toHaveBeenCalledWith('bot-1', 'source-session', 'source-assistant', { title: 'Custom fork name' })
    expect(store.sessionId).toBe('fork-session')
    expect(store.messages.map(message => message.id)).toEqual(['fork-user', 'fork-assistant'])
    expect(store.activeChatTarget.metadata.forked_from).toMatchObject({
      session_id: 'source-session',
      message_id: 'source-assistant',
      fork_message_id: 'fork-assistant',
    })
  })

  it('keeps fork metadata when a stale session list refresh does not include the fork session', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'source-session', bot_id: 'bot-1', title: 'Source', type: 'chat' }],
      nextCursor: null,
    })
    const sourceTurns = [
      {
        id: 'source-user',
        role: 'user' as const,
        text: 'hello',
        attachments: [],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'source-assistant',
        role: 'assistant' as const,
        messages: [{ id: 1, type: 'text' as const, content: 'answer' }],
        timestamp: '2026-05-17T08:00:01.000Z',
        streaming: false,
      },
    ]
    const forkTurns = [
      {
        id: 'fork-user',
        role: 'user' as const,
        text: 'hello',
        attachments: [],
        timestamp: '2026-05-17T08:00:00.000Z',
      },
      {
        id: 'fork-assistant',
        role: 'assistant' as const,
        messages: [{ id: 1, type: 'text' as const, content: 'answer' }],
        timestamp: '2026-05-17T08:00:01.000Z',
        streaming: false,
      },
    ]
    api.fetchMessagesUI.mockImplementation((_botId: string, sessionId: string) => {
      if (sessionId === 'source-session') return Promise.resolve(sourceTurns)
      if (sessionId === 'fork-session') return Promise.resolve(forkTurns)
      return Promise.resolve([])
    })
    api.forkSessionFromMessage.mockResolvedValueOnce({
      id: 'fork-session',
      bot_id: 'bot-1',
      title: 'Source fork',
      type: 'chat',
      metadata: {
        forked_from: {
          session_id: 'source-session',
          title: 'Source',
          message_id: 'source-assistant',
          fork_message_id: 'fork-assistant',
        },
      },
    })
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'source-session', bot_id: 'bot-1', title: 'Source', type: 'chat' }],
      nextCursor: null,
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    const ok = await store.forkMessage('source-assistant')
    await applyLatestForkRequest(store)
    await flushPromises()

    expect(ok).toBe(true)
    expect(store.sessionId).toBe('fork-session')
    expect(store.activeChatTarget.metadata.forked_from).toMatchObject({
      session_id: 'source-session',
      message_id: 'source-assistant',
      fork_message_id: 'fork-assistant',
    })
    expect(store.knownSessionSummary('fork-session')?.metadata?.forked_from).toMatchObject({
      fork_message_id: 'fork-assistant',
    })
  })

  it('keeps a forked ask-user continuation in one assistant turn when hydration resolves late', async () => {
    sendEvents = []
    const userInput = singleSelectUserInput()
    const forkTurns = [
      {
        id: 'fork-user', role: 'user' as const, text: 'use ask_user', attachments: [],
        timestamp: '2026-07-11T00:00:00.000Z',
      },
      {
        id: 'fork-assistant',
        role: 'assistant' as const,
        messages: [{
          id: 1,
          type: 'tool' as const,
          name: 'ask_user',
          input: { questions: [{ text: 'Which plan?', kind: 'single_select' }] },
          tool_call_id: 'call-ask',
          running: false,
          user_input: userInput,
        }],
        timestamp: '2026-07-11T00:00:01.000Z',
      },
    ]
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'source-session', bot_id: 'bot-1', title: 'Source', type: 'chat' }],
      nextCursor: null,
    })
    api.forkSessionFromMessage.mockResolvedValueOnce({
      id: 'fork-session', bot_id: 'bot-1', title: 'Source fork', type: 'chat',
    })
    const hydration = deferred<typeof forkTurns>()
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()
    api.fetchMessagesUI
      .mockResolvedValueOnce(forkTurns)
      .mockReturnValueOnce(hydration.promise)

    await store.forkMessage('source-assistant')
    await applyLatestForkRequest(store)
    await flushPromises()
    expect(store.messages.filter(message => message.role === 'assistant')).toHaveLength(1)

    await store.respondUserInput(userInput, {
      answers: [{ question_id: 'q1', option_ids: ['q1.o1'] }],
    })
    const responseRunId = wsRunId(-1)
    streamHandler?.({
      type: 'message', run_id: responseRunId, session_id: 'fork-session',
      data: { id: 2, type: 'text', content: 'continuation' },
    } as UIStreamEvent)

    hydration.resolve(forkTurns)
    await flushPromises()
    await flushPromises()

    const assistantTurns = store.messages.filter(message => message.role === 'assistant')
    expect(assistantTurns).toHaveLength(1)
    expect(assistantTurns[0]).toMatchObject({
      streaming: true,
      messages: [
        { type: 'tool', userInput: { user_input_id: 'input-1', status: 'submitted' } },
        { type: 'text', content: 'continuation' },
      ],
    })

    api.fetchMessagesUI.mockResolvedValueOnce(forkTurns)
    streamHandler?.({ type: 'end', run_id: responseRunId, session_id: 'fork-session' } as UIStreamEvent)
    await flushPromises()
  })

  it('routes a late fork response to its origin view without changing the focused Session', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'source-session', bot_id: 'bot-1', title: 'Source', type: 'chat' },
        { id: 'other-session', bot_id: 'bot-1', title: 'Other', type: 'chat' },
      ],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockImplementation((_botId: string, sessionId: string) => {
      if (sessionId === 'source-session') {
        return Promise.resolve([
          {
            id: 'source-assistant',
            role: 'assistant' as const,
            messages: [{ id: 1, type: 'text' as const, content: 'answer' }],
            timestamp: '2026-05-17T08:00:01.000Z',
            streaming: false,
          },
        ])
      }
      if (sessionId === 'other-session') {
        return Promise.resolve([
          {
            id: 'other-user',
            role: 'user' as const,
            text: 'other',
            attachments: [],
            timestamp: '2026-05-17T09:00:00.000Z',
          },
        ])
      }
      return Promise.resolve([])
    })
    let resolveFork!: (session: unknown) => void
    api.forkSessionFromMessage.mockReturnValueOnce(new Promise(resolve => {
      resolveFork = resolve
    }))
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    const targetA = { botId: 'bot-1', sessionId: 'source-session', viewId: 'chat:a' }
    const targetB = { botId: 'bot-1', sessionId: 'other-session', viewId: 'chat:b' }
    store.bindChatView(targetA.viewId, targetA, true)
    store.focusChatView(targetA.viewId)
    const fork = store.forkMessage('source-assistant', { target: targetA })
    await flushPromises()
    store.bindChatView(targetB.viewId, targetB, true)
    store.focusChatView(targetB.viewId)
    await store.selectSession('other-session')
    await flushPromises()
    resolveFork({
      id: 'fork-session',
      bot_id: 'bot-1',
      title: 'Source fork',
      type: 'chat',
      metadata: {
        forked_from: {
          session_id: 'source-session',
          title: 'Source',
          message_id: 'source-assistant',
        },
      },
    })
    const ok = await fork
    await flushPromises()

    expect(ok).toBe(true)
    expect(store.sessionId).toBe('other-session')
    expect(store.messages.map(message => message.id)).toEqual(['other-user'])
    expect(store.knownSessionSummary('fork-session')).toMatchObject({ id: 'fork-session' })
    expect(api.fetchMessagesUI).toHaveBeenCalledWith('bot-1', 'fork-session', expect.anything())
    expect(store.forkedSessionRequested).toMatchObject({
      botId: 'bot-1',
      viewId: targetA.viewId,
      expectedSessionId: 'source-session',
      sessionId: 'fork-session',
      activate: true,
    })
  })

  it('drops a late Fork result after the authenticated scope resets', async () => {
    const windowTarget = new EventTarget()
    vi.stubGlobal('window', windowTarget)
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'source-session', bot_id: 'bot-1', title: 'Source', type: 'chat' }],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockResolvedValueOnce([{
      id: 'source-assistant',
      role: 'assistant',
      messages: [{ id: 1, type: 'text', content: 'answer' }],
      timestamp: '2026-07-11T00:00:00Z',
      streaming: false,
    }])
    const response = deferred<{
      id: string
      bot_id: string
      title: string
      type: string
    }>()
    api.forkSessionFromMessage.mockReturnValueOnce(response.promise)
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    const fork = store.forkMessage('source-assistant')
    windowTarget.dispatchEvent(new CustomEvent(AUTH_SESSION_CLEARED_EVENT, {
      detail: { reason: 'logout' },
    }))
    response.resolve({ id: 'old-fork', bot_id: 'bot-1', title: 'Old fork', type: 'chat' })

    await expect(fork).resolves.toBe(true)
    expect(store.forkedSessionRequested).toBeNull()
    expect(store.knownSessionSummary('old-fork')).toBeNull()
    expect(api.fetchMessagesUI).not.toHaveBeenCalledWith('bot-1', 'old-fork', expect.anything())
  })

  it('does not fork non-chat sessions', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'discuss-session', bot_id: 'bot-1', title: 'Discuss', type: 'discuss' }],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'assistant-1',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'answer' }],
        timestamp: '2026-05-17T08:00:01.000Z',
        streaming: false,
      },
    ])
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    const ok = await store.forkMessage('assistant-1')

    expect(ok).toBe(false)
    expect(api.forkSessionFromMessage).not.toHaveBeenCalled()
  })

  it('sends disable as an explicit reasoning effort override', async () => {
    sendEvents = []
    const sent: Array<{ reasoning_effort?: string; invocation_id?: string; session_id?: string }> = []
    api.connectWebSocket.mockImplementation((_botId: string, onStreamEvent: UIStreamEventHandler) => {
      streamHandler = onStreamEvent
      return {
        get connected() {
          return true
        },
        send: vi.fn((message: { reasoning_effort?: string; invocation_id?: string; session_id?: string }) => {
          sent.push(message)
          const runId = `run-${++wsRunSeq}`
          onStreamEvent({
            type: 'run_accepted',
            run_id: runId,
            invocation_id: message.invocation_id,
            session_id: message.session_id,
          } as UIStreamEvent)
          onStreamEvent({ type: 'start', run_id: runId, session_id: message.session_id } as UIStreamEvent)
          onStreamEvent({ type: 'end', run_id: runId, session_id: message.session_id } as UIStreamEvent)
        }),
        abort: vi.fn(),
        close: vi.fn(),
        onOpen: null,
        onClose: null,
      }
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.overrideReasoningEffort = REASONING_EFFORT_DISABLE
    const result = await store.sendMessage('hello')

    expect(result).toMatchObject({ ok: true })
    expect(sent).toHaveLength(1)
    expect(sent[0].reasoning_effort).toBe(REASONING_EFFORT_DISABLE)
  })

  it('keeps late quick action events scoped to the composer that sent them', async () => {
    api.fetchBots.mockResolvedValueOnce([
      { id: 'bot-1', status: 'active', name: 'Bot 1' },
      { id: 'bot-2', status: 'active', name: 'Bot 2' },
    ])
    let resolveQuickAction: (value: UIStreamEvent) => void = () => {}
    api.executeQuickAction.mockImplementationOnce(() => new Promise<UIStreamEvent>((resolve) => {
      resolveQuickAction = resolve
    }))
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sendPromise = store.sendMessage('/help', undefined, { composerScope: 'bot-1:draft-a' })
    await flushPromises()

    api.fetchSession.mockResolvedValueOnce({
      id: 'session-b',
      bot_id: 'bot-1',
      title: 'Session B',
      type: 'chat',
    })
    await store.selectSession('session-b')
    resolveQuickAction({
      type: 'command_result',
      terminal: true,
      result: { kind: 'text', text: 'Help' },
    } as UIStreamEvent)
    await sendPromise

    const draftCommandEvent = store.commandEventForScope({ botId: 'bot-1', composerScope: 'bot-1:draft-a' })
    expect(draftCommandEvent).toMatchObject({
      type: 'command_result',
      bot_id: 'bot-1',
      composer_scope: 'bot-1:draft-a',
    })
    expect(draftCommandEvent?.session_id).toBeUndefined()
    expect(store.commandEvent).toBeNull()
  })

  it('sends selected session ids as quick action capability context', async () => {
    api.executeQuickAction.mockResolvedValueOnce({
      type: 'command_result',
      terminal: true,
      result: { kind: 'text', text: 'Help' },
    } as UIStreamEvent)
    api.fetchSession.mockResolvedValueOnce({
      id: 'session-a',
      bot_id: 'bot-1',
      title: 'Session A',
      type: 'chat',
    })
    const store = useChatStore()
    const onBeforeTurnAppend = vi.fn()
    const onTurnAppendAborted = vi.fn()

    await store.selectBot('bot-1')
    await store.selectSession('session-a')
    const result = await store.sendMessage('/help', undefined, {
      composerScope: 'bot-1:panel-a',
      onBeforeTurnAppend,
      onTurnAppendAborted,
    })

    expect(result).toMatchObject({ ok: true })
    expect(api.executeQuickAction).toHaveBeenCalledWith('bot-1', 'help', expect.objectContaining({
      composerScope: 'bot-1:panel-a',
      sessionId: 'session-a',
      skillActivationAllowed: true,
    }))
    expect(onBeforeTurnAppend).not.toHaveBeenCalled()
    expect(onTurnAppendAborted).not.toHaveBeenCalled()
  })

  it('keeps quick action transport failures as startup command errors', async () => {
    api.executeQuickAction.mockRejectedValueOnce(new Error('network unavailable'))
    const store = useChatStore()

    await store.selectBot('bot-1')
    const result = await store.sendMessage('/help', undefined, { composerScope: 'bot-1:panel-a' })

    expect(result).toMatchObject({
      ok: false,
      stage: 'startup',
      restoreInput: '/help',
      error: 'network unavailable',
    })
    const commandEvent = store.commandEventForScope({ botId: 'bot-1', composerScope: 'bot-1:panel-a' })
    expect(commandEvent).toMatchObject({
      type: 'command_error',
      composer_scope: 'bot-1:panel-a',
      error: { code: 'generic', message: 'network unavailable' },
    })
  })

  it('keeps direct skill slash websocket startup failures restorable', async () => {
    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      { type: 'error', message: 'model failed' } as UIStreamEvent,
    ]
    const store = useChatStore()

    await store.selectBot('bot-1')
    const result = await store.sendMessage('/wat', undefined, { composerScope: 'bot-1:panel-a' })

    expect(result).toMatchObject({
      ok: false,
      stage: 'startup',
      restoreInput: '/wat',
      error: 'model failed',
    })
    expect(sentWSMessages[0]).toMatchObject({
      type: 'message',
      text: '/wat',
      composer_scope: 'bot-1:panel-a',
    })
    expect(sentWSMessages[0]?.requested_skills).toBeUndefined()
  })

  it('rejects direct skill activation in pending ACP drafts before sending websocket chat', async () => {
    sendEvents = []
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })
    const result = await store.sendMessage('/flutter-adding-home-screen-widgets', undefined, {
      composerScope: 'bot-1:draft-a',
    })

    expect(result).toMatchObject({
      ok: false,
      stage: 'startup',
      restoreInput: '/flutter-adding-home-screen-widgets',
    })
    expect(sentWSMessages).toHaveLength(0)
    expect(api.createSession).not.toHaveBeenCalled()
    expect(store.loading).toBe(false)
    const commandEvent = store.commandEventForScope({ botId: 'bot-1', composerScope: 'bot-1:draft-a' })
    expect(commandEvent).toMatchObject({
      type: 'command_error',
      error: { code: 'unsupported_skill_slash_context' },
    })
  })

  it('rejects skill list quick action in pending ACP drafts without reading the catalog', async () => {
    api.executeQuickAction.mockResolvedValueOnce({
      type: 'command_error',
      terminal: true,
      composer_scope: 'bot-1:draft-a',
      error: { code: 'unsupported_skill_slash_context', message: 'unsupported' },
    } as UIStreamEvent)
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })
    const result = await store.sendMessage('/skill list', undefined, {
      composerScope: 'bot-1:draft-a',
    })

    expect(result).toMatchObject({
      ok: false,
      stage: 'startup',
      restoreInput: '/skill list',
    })
    expect(api.executeQuickAction).toHaveBeenCalledWith('bot-1', 'skill.list', expect.objectContaining({
      composerScope: 'bot-1:draft-a',
      skillActivationAllowed: false,
    }))
    expect(sentWSMessages).toHaveLength(0)
    expect(store.loading).toBe(false)
    const commandEvent = store.commandEventForScope({ botId: 'bot-1', composerScope: 'bot-1:draft-a' })
    expect(commandEvent).toMatchObject({
      type: 'command_error',
      error: { code: 'unsupported_skill_slash_context' },
    })
  })

  it('shows ACP help without skill entry points', async () => {
    api.executeQuickAction.mockResolvedValueOnce({
      type: 'command_result',
      terminal: true,
      composer_scope: 'bot-1:draft-a',
      result: {
        kind: 'list',
        items: [{ id: 'help', title: '/help', kind: 'quick_action' }],
        text: 'Available Web quick actions: /help.',
      },
    } as UIStreamEvent)
    const store = useChatStore()

    await store.selectBot('bot-1')
    store.stageACPSession({ agentId: 'codex' })
    const result = await store.sendMessage('/help', undefined, {
      composerScope: 'bot-1:draft-a',
    })

    expect(result).toMatchObject({ ok: true })
    expect(api.executeQuickAction).toHaveBeenCalledWith('bot-1', 'help', expect.objectContaining({
      composerScope: 'bot-1:draft-a',
      skillActivationAllowed: false,
    }))
    expect(sentWSMessages).toHaveLength(0)
    const commandEvent = store.commandEventForScope({ botId: 'bot-1', composerScope: 'bot-1:draft-a' })
    expect(commandEvent).toMatchObject({
      type: 'command_result',
      result: {
        kind: 'list',
        items: [
          expect.objectContaining({ id: 'help' }),
        ],
      },
    })
    expect(commandEvent?.result?.items?.some(item => item.id === 'skill.list')).toBe(false)
    expect(commandEvent?.result?.text).not.toContain('/skill list')
    expect(store.loading).toBe(false)
  })

  it('sends only skill name in requested skill websocket payloads', async () => {
    sendEvents = [{ type: 'start' } as UIStreamEvent, { type: 'end' } as UIStreamEvent]
    api.fetchSession.mockResolvedValueOnce({
      id: 'session-a',
      bot_id: 'bot-1',
      title: 'Session A',
      type: 'chat',
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    await store.selectSession('session-a')
    const result = await store.sendMessage('hello with skill', undefined, {
      requestedSkills: [{
        name: 'alpha',
        display_name: 'Alpha',
        description: 'Display-only description',
        source_kind: 'managed',
        state: 'effective',
      }],
      composerScope: 'bot-1:panel-a',
    })

    expect(result).toMatchObject({ ok: true })
    expect(sentWSMessages[0]?.requested_skills).toEqual([{
      name: 'alpha',
    }])
    expect(JSON.stringify(sentWSMessages[0]?.requested_skills)).not.toContain('Display-only description')
    expect(JSON.stringify(sentWSMessages[0]?.requested_skills)).not.toContain('managed')
  })

  it('inserts direct skill activation only after the server user_message ack', async () => {
    sendEvents = []
    api.fetchSession.mockResolvedValueOnce({
      id: 'session-a',
      bot_id: 'bot-1',
      title: 'Session A',
      type: 'chat',
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    await store.selectSession('session-a')
    const sendPromise = store.sendMessage('/flutter-adding-home-screen-widgets', undefined, {
      composerScope: 'bot-1:panel-a',
    })
    await flushPromises()
    const runId = wsRunId(0)

    expect(store.messages).toHaveLength(0)
    expect(sentWSMessages[0]).toMatchObject({
      type: 'message',
      text: '/flutter-adding-home-screen-widgets',
    })
    expect(sentWSMessages[0]?.requested_skills).toBeUndefined()

    streamHandler?.({
      type: 'user_message',
      run_id: runId,
      session_id: 'session-a',
      data: {
        id: 'msg-skill',
        role: 'user',
        text: '',
        user_message_kind: 'skill_activation',
        skill_activation: {
          skills: [{
            name: 'flutter-adding-home-screen-widgets',
            display_name: 'Flutter adding home screen widgets',
            description: 'Safe display summary',
            source_kind: 'managed',
            state: 'effective',
          }],
        },
        timestamp: '2026-07-03T00:00:00.000Z',
      },
    } as UIStreamEvent)
    await flushPromises()

    expect(store.messages).toHaveLength(2)
    expect(store.messages[0]).toMatchObject({
      role: 'user',
      text: '',
      userMessageKind: 'skill_activation',
      skillActivation: {
        skills: [{
          name: 'flutter-adding-home-screen-widgets',
          display_name: 'Flutter adding home screen widgets',
        }],
      },
    })
    expect(store.messages[1]).toMatchObject({ role: 'assistant', streaming: true })

    streamHandler?.({
      type: 'message',
      run_id: runId,
      session_id: 'session-a',
      data: { id: 1, type: 'text', content: 'Done' },
    } as UIStreamEvent)
    expect(store.messages[1]).toMatchObject({
      role: 'assistant',
      messages: [{ type: 'text', content: 'Done' }],
      streaming: true,
    })

    streamHandler?.({ type: 'end', run_id: runId, session_id: 'session-a' } as UIStreamEvent)
    await expect(sendPromise).resolves.toMatchObject({ ok: true })
  })

  it('blocks a second deferred draft send while the first stream is still unbound', async () => {
    sendEvents = []
    const store = useChatStore()

    await store.selectBot('bot-1')
    const firstSend = store.sendMessage('first activation', undefined, {
      requestedSkills: [{ name: 'alpha' }],
      composerScope: 'bot-1:draft-a',
    })
    await flushPromises()
    const runId = wsRunId(0)

    expect(store.streaming).toBe(true)
    const secondResult = await store.sendMessage('second activation', undefined, {
      requestedSkills: [{ name: 'beta' }],
      composerScope: 'bot-1:draft-a',
    })
    expect(secondResult).toMatchObject({ ok: false, stage: 'startup' })
    expect(sentWSMessages).toHaveLength(1)

    streamHandler?.({ type: 'end', run_id: runId } as UIStreamEvent)
    await expect(firstSend).resolves.toMatchObject({ ok: true })
    expect(store.streaming).toBe(false)
  })

  it('aborts a deferred draft stream before session_created binds it', async () => {
    sendEvents = []
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sending = store.sendMessage('activate', undefined, {
      requestedSkills: [{ name: 'alpha' }],
      composerScope: 'bot-1:draft-a',
    })
    await flushPromises()
    const runId = wsRunId(0)

    store.abort()

    await expect(sending).resolves.toMatchObject({ ok: false })
    expect(abortedWSRuns).toContain(runId)
    expect(store.streaming).toBe(false)
  })

  it('names an outbound turn by invocation and never sends a stream id', async () => {
    sendEvents = []
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sending = store.sendMessage('hello')
    await flushPromises()

    const sent = sentWSMessages[0]!
    expect(sent).toMatchObject({ type: 'message', invocation_id: expect.any(String) })
    expect(sent).not.toHaveProperty('stream_id')
    // The client mints the invocation; only the server names the run, and it is
    // that name a stop has to address.
    expect(sent.invocation_id).not.toBe(wsRunId(0))

    store.abort()
    await expect(sending).resolves.toMatchObject({ ok: false })
    expect(abortedWSRuns).toEqual([wsRunId(0)])
  })

  it('replays a stop pressed before the run was accepted', async () => {
    sendEvents = []
    // Withhold acceptance so the stop lands while the turn has no run id.
    api.connectWebSocket.mockImplementation((_botId: string, onStreamEvent: UIStreamEventHandler) => {
      streamHandler = onStreamEvent
      return {
        get connected() {
          return true
        },
        send: vi.fn((message: Record<string, unknown>) => {
          sentWSMessages.push(message)
        }),
        abort: vi.fn((runId: string) => {
          abortedWSRuns.push(runId)
        }),
        close: vi.fn(),
        onOpen: null,
        onClose: null,
      }
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sending = store.sendMessage('hello')
    await flushPromises()
    const invocationId = sentWSMessages[0]!.invocation_id as string

    store.abort()
    expect(abortedWSRuns).toEqual([])

    streamHandler?.({
      type: 'run_accepted',
      run_id: 'run-late',
      invocation_id: invocationId,
      session_id: 'session-1',
    } as UIStreamEvent)

    await expect(sending).resolves.toMatchObject({ ok: false })
    expect(abortedWSRuns).toEqual(['run-late'])
  })

  it('fails a rejected submission with the code the server refused it by', async () => {
    sendEvents = []
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sending = store.sendMessage('hello')
    await flushPromises()
    const invocationId = wsInvocationId(0)

    streamHandler?.({
      type: 'run_rejected',
      invocation_id: invocationId,
      session_id: 'session-1',
      code: 'session_runtime.session_busy',
      message: 'This conversation is still working on the previous message.',
    } as UIStreamEvent)

    // The turn never started, so the composer gets its input back along with the
    // stable code it needs to decide whether retrying is worth offering.
    await expect(sending).resolves.toMatchObject({
      ok: false,
      stage: 'startup',
      errorCode: 'session_runtime.session_busy',
      restoreInput: 'hello',
    })
    expect(store.streaming).toBe(false)
  })

  it('keeps the first created-session correlation when a stream receives a conflicting duplicate', async () => {
    sendEvents = []
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sending = store.sendMessage('activate', undefined, {
      requestedSkills: [{ name: 'alpha' }],
      composerScope: 'bot-1:draft-a',
    })
    await flushPromises()
    const invocationId = wsInvocationId(0)
    const runId = wsRunId(0)

    streamHandler?.({ type: 'session_created', invocation_id: invocationId, session_id: 'session-first' } as UIStreamEvent)
    streamHandler?.({ type: 'session_created', invocation_id: invocationId, session_id: 'session-conflict' } as UIStreamEvent)

    expect(store.sessionId).toBe('session-first')
    expect(store.knownSessionSummary('session-first')).not.toBeNull()
    expect(store.knownSessionSummary('session-conflict')).toBeNull()

    streamHandler?.({ type: 'end', run_id: runId, session_id: 'session-first' } as UIStreamEvent)
    await expect(sending).resolves.toMatchObject({ ok: true })
  })

  it('ignores late messages for a terminal stream instead of resurrecting it', async () => {
    sendEvents = [{ type: 'start' } as UIStreamEvent, { type: 'end' } as UIStreamEvent]
    const store = useChatStore()

    await store.selectBot('bot-1')
    await expect(store.sendMessage('hello')).resolves.toMatchObject({ ok: true })
    const messageCount = store.messages.length

    streamHandler?.({
      type: 'message',
      run_id: lastRunId,
      session_id: lastSessionId,
      data: { id: 1, type: 'text', content: 'late' },
    } as UIStreamEvent)

    expect(store.messages).toHaveLength(messageCount)
    expect(store.streaming).toBe(false)
  })

  it('does not select a late session_created event after the user switches sessions', async () => {
    sendEvents = []
    api.fetchSession.mockImplementation(async (_botId: string, sessionID: string) => ({
      id: sessionID,
      bot_id: 'bot-1',
      title: sessionID,
      type: 'chat',
    }))
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sendPromise = store.sendMessage('hello with skill', undefined, {
      requestedSkills: [{ name: 'alpha' }],
      composerScope: 'bot-1:draft-a',
    })
    await flushPromises()
    const invocationId = wsInvocationId(0)
    const runId = wsRunId(0)

    await store.selectSession('session-b')
    streamHandler?.({ type: 'session_created', invocation_id: invocationId, session_id: 'created-session' } as UIStreamEvent)
    await flushPromises()

    expect(store.sessionId).toBe('session-b')
    // The hidden Draft view still owns this stream, so its new Session is
    // remembered without stealing global focus from session-b.
    expect(store.knownSessionSummary('created-session')).not.toBeNull()

    streamHandler?.({ type: 'end', run_id: runId, session_id: 'created-session' } as UIStreamEvent)
    await sendPromise

    expect(store.sessionId).toBe('session-b')
  })

  it('deletes a deferred draft session when requested skill preflight fails after session_created', async () => {
    sendEvents = []
    const store = useChatStore()
    const requestedSkill = { name: 'alpha' }
    const attachment = {
      type: 'file',
      base64: 'data:text/plain;base64,aGVsbG8=',
      mime: 'text/plain',
      name: 'note.txt',
    }

    await store.selectBot('bot-1')
    const sendPromise = store.sendMessage('hello with skill', [attachment], {
      requestedSkills: [requestedSkill],
      composerScope: 'bot-1:draft-a',
    })
    await flushPromises()
    const invocationId = wsInvocationId(0)

    streamHandler?.({ type: 'session_created', invocation_id: invocationId, session_id: 'created-session' } as UIStreamEvent)
    await flushPromises()
    expect(store.sessionId).toBe('created-session')

    streamHandler?.({
      type: 'command_error',
      invocation_id: invocationId,
      session_id: 'created-session',
      composer_scope: 'bot-1:draft-a',
      terminal: true,
      error: {
        code: 'unsupported_skill_slash_context',
        message: 'Requested skills are not supported here.',
      },
    } as UIStreamEvent)
    const result = await sendPromise

    expect(result).toMatchObject({
      ok: false,
      stage: 'startup',
      restoreInput: 'hello with skill',
      restoreAttachments: [attachment],
      restoreRequestedSkills: [requestedSkill],
    })
    expect(api.deleteSession).toHaveBeenCalledWith('bot-1', 'created-session')
    expect(store.deletedSession).toEqual({
      id: 'created-session',
      botId: 'bot-1',
      seq: 1,
      composerScope: 'bot-1:draft-a',
    })
    expect(store.sessionId).toBeNull()
    expect(store.knownSessionSummary('created-session')).toBeNull()
    expect(store.messages).toHaveLength(0)
    const draftCommandEvent = store.commandEventForScope({ botId: 'bot-1', composerScope: 'bot-1:draft-a' })
    expect(draftCommandEvent).toMatchObject({
      type: 'command_error',
      bot_id: 'bot-1',
      composer_scope: 'bot-1:draft-a',
    })
    expect(draftCommandEvent?.session_id).toBeUndefined()
    expect(store.startupSendFailureFor({
      botId: 'bot-1',
      sessionId: null,
      viewId: 'draft-a',
    }, 'bot-1:draft-a')).toMatchObject({
      botId: 'bot-1',
      composerScope: 'bot-1:draft-a',
      restoreInput: 'hello with skill',
      restoreAttachments: [attachment],
      restoreRequestedSkills: [requestedSkill],
    })
  })

  it('keeps the current session when deferred draft failure arrives after a session switch', async () => {
    sendEvents = []
    api.fetchSession.mockImplementation(async (_botId: string, sessionID: string) => ({
      id: sessionID,
      bot_id: 'bot-1',
      title: sessionID,
      type: 'chat',
    }))
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sendPromise = store.sendMessage('hello with skill', undefined, {
      requestedSkills: [{ name: 'alpha' }],
      composerScope: 'bot-1:draft-a',
    })
    await flushPromises()
    const invocationId = wsInvocationId(0)

    await store.selectSession('session-b')
    streamHandler?.({ type: 'session_created', invocation_id: invocationId, session_id: 'created-session' } as UIStreamEvent)
    streamHandler?.({
      type: 'command_error',
      invocation_id: invocationId,
      session_id: 'created-session',
      composer_scope: 'bot-1:draft-a',
      terminal: true,
      error: {
        code: 'unsupported_skill_slash_context',
        message: 'Requested skills are not supported here.',
      },
    } as UIStreamEvent)
    const result = await sendPromise

    expect(result).toMatchObject({ ok: false, stage: 'startup' })
    expect(api.deleteSession).toHaveBeenCalledWith('bot-1', 'created-session')
    expect(store.deletedSession).toEqual({
      id: 'created-session',
      botId: 'bot-1',
      seq: 1,
      composerScope: 'bot-1:draft-a',
    })
    expect(store.sessionId).toBe('session-b')
    expect(store.knownSessionSummary('created-session')).toBeNull()
  })

  it('keeps deferred draft websocket errors scoped away from the switched session', async () => {
    sendEvents = []
    api.fetchSession.mockImplementation(async (_botId: string, sessionID: string) => ({
      id: sessionID,
      bot_id: 'bot-1',
      title: sessionID,
      type: 'chat',
    }))
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sendPromise = store.sendMessage('hello with skill', undefined, {
      requestedSkills: [{ name: 'alpha' }],
      composerScope: 'bot-1:draft-a',
    })
    await flushPromises()
    const invocationId = wsInvocationId(0)
    const runId = wsRunId(0)

    await store.selectSession('session-b')
    streamHandler?.({ type: 'session_created', invocation_id: invocationId, session_id: 'created-session' } as UIStreamEvent)
    streamHandler?.({ type: 'error', run_id: runId, session_id: 'created-session', message: 'model failed' } as UIStreamEvent)
    const result = await sendPromise

    expect(result).toMatchObject({ ok: false, stage: 'startup', composerScope: 'bot-1:draft-a' })
    expect(api.deleteSession).toHaveBeenCalledWith('bot-1', 'created-session')
    expect(store.deletedSession).toEqual({
      id: 'created-session',
      botId: 'bot-1',
      seq: 1,
      composerScope: 'bot-1:draft-a',
    })
    expect(store.sessionId).toBe('session-b')
    expect(store.startupSendFailure).toBeNull()
  })

  it('keeps the current command event when an off-scope command event arrives', async () => {
    const store = useChatStore()

    await store.selectBot('bot-1')
    api.fetchSession.mockResolvedValueOnce({
      id: 'session-b',
      bot_id: 'bot-1',
      title: 'Session B',
      type: 'chat',
    })
    await store.selectSession('session-b')

    store.showCommandError('current_error', 'Current error', {
      botId: 'bot-1',
      sessionId: 'session-b',
      composerScope: 'bot-1:draft-a',
    })
    store.showCommandError('late_error', 'Late draft error', {
      botId: 'bot-1',
      composerScope: 'bot-1:draft-a',
    })

    expect(store.commandEvent).toMatchObject({
      type: 'command_error',
      session_id: 'session-b',
      error: { code: 'current_error' },
    })
    expect(store.commandEventForScope({ botId: 'bot-1', composerScope: 'bot-1:draft-a' })).toMatchObject({
      type: 'command_error',
      error: { code: 'late_error' },
    })
  })

  it('ignores queued websocket events from a previous bot connection', async () => {
    api.fetchBots.mockResolvedValue([
      { id: 'bot-1', status: 'active', name: 'Bot 1' },
      { id: 'bot-2', status: 'active', name: 'Bot 2' },
    ])
    const handlers: Array<{ botId: string; handler: UIStreamEventHandler }> = []
    api.connectWebSocket.mockImplementation((botId: string, handler: UIStreamEventHandler) => {
      handlers.push({ botId, handler })
      return {
        get connected() {
          return true
        },
        send: vi.fn(),
        abort: vi.fn(),
        close: vi.fn(),
        onOpen: null,
        onClose: null,
      }
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    const staleHandler = handlers.find(entry => entry.botId === 'bot-1')?.handler
    expect(staleHandler).toBeDefined()

    await store.selectBot('bot-2')
    staleHandler?.({ type: 'start', run_id: 'old-stream', session_id: 'old-session' } as UIStreamEvent)
    staleHandler?.({
      type: 'message',
      run_id: 'old-stream',
      session_id: 'old-session',
      data: { id: 0, type: 'text', content: 'late old-bot output' },
    } as UIStreamEvent)

    expect(store.currentBotId).toBe('bot-2')
    expect(store.isSessionStreaming('bot-1', 'old-session')).toBe(false)
    expect(store.messages).toHaveLength(0)
  })

  it('reattaches an active assistant stream after switching away and back', async () => {
    sendEvents = []
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' },
      { id: 'session-b', bot_id: 'bot-1', title: 'B', type: 'chat' },
    ], nextCursor: null })
    let returningToSessionA = false
    api.fetchMessagesUI.mockImplementation(async (_botId: string, targetSessionId: string) => {
      if (returningToSessionA && targetSessionId === 'session-a') {
        return [{
          id: 'server-user-a',
          role: 'user',
          text: 'first',
          attachments: [],
          timestamp: '2026-07-10T00:00:00.000Z',
        }]
      }
      return []
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    const sending = store.sendMessage('first')
    await flushPromises()
    const runId = wsRunId(0)
    streamHandler?.({
      type: 'message',
      run_id: runId,
      session_id: 'session-a',
      data: { id: 0, type: 'text', content: 'before switch' },
    } as UIStreamEvent)

    await store.selectSession('session-b')
    await flushPromises()
    expect(store.messages).toHaveLength(0)

    returningToSessionA = true
    await store.selectSession('session-a')
    await flushPromises()
    await flushPromises()
    expect(store.messages.map(turn => turn.role)).toEqual(['user', 'assistant'])
    expect(store.messages[1]).toMatchObject({
      role: 'assistant',
      messages: [{ type: 'text', content: 'before switch' }],
      streaming: true,
    })

    streamHandler?.({
      type: 'message',
      run_id: runId,
      session_id: 'session-a',
      data: { id: 1, type: 'text', content: 'after return' },
    } as UIStreamEvent)
    expect(store.messages[1]).toMatchObject({
      role: 'assistant',
      messages: [
        { type: 'text', content: 'before switch' },
        { type: 'text', content: 'after return' },
      ],
    })

    streamHandler?.({ type: 'end', run_id: runId, session_id: 'session-a' } as UIStreamEvent)
    await expect(sending).resolves.toMatchObject({ ok: true })
  })

  it('replaces the hydrated assistant twin when reattaching an active stream', async () => {
    sendEvents = []
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' },
      { id: 'session-b', bot_id: 'bot-1', title: 'B', type: 'chat' },
    ], nextCursor: null })
    let returningToSessionA = false
    api.fetchMessagesUI.mockImplementation(async (_botId: string, targetSessionId: string) => {
      if (!returningToSessionA || targetSessionId !== 'session-a') return []
      return [
        { id: 'server-user-a', role: 'user', text: 'first', attachments: [], timestamp: '2026-07-10T00:00:00.000Z' },
        {
          id: 'server-assistant-a',
          role: 'assistant',
          messages: [{ id: 0, type: 'text', content: 'persisted' }],
          timestamp: '2026-07-10T00:00:01.000Z',
        },
      ]
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sending = store.sendMessage('first')
    await flushPromises()
    const runId = wsRunId(0)
    streamHandler?.({
      type: 'message', run_id: runId, session_id: 'session-a',
      data: { id: 0, type: 'text', content: 'live' },
    } as UIStreamEvent)

    await store.selectSession('session-b')
    returningToSessionA = true
    await store.selectSession('session-a')
    await flushPromises()
    await flushPromises()

    expect(store.messages.filter(turn => turn.role === 'assistant')).toHaveLength(1)
    expect(store.messages.at(-1)).toMatchObject({
      role: 'assistant',
      streaming: true,
      messages: [{ type: 'text', content: 'live' }],
    })

    streamHandler?.({ type: 'end', run_id: runId, session_id: 'session-a' } as UIStreamEvent)
    await sending
  })

  it('reattaches an active stream even when return hydration fails', async () => {
    sendEvents = []
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' },
      { id: 'session-b', bot_id: 'bot-1', title: 'B', type: 'chat' },
    ], nextCursor: null })
    let returningToSessionA = false
    api.fetchMessagesUI.mockImplementation(async (_botId: string, targetSessionId: string) => {
      if (returningToSessionA && targetSessionId === 'session-a') throw new Error('offline')
      return []
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    const sending = store.sendMessage('first')
    await flushPromises()
    const runId = wsRunId(0)
    streamHandler?.({
      type: 'message', run_id: runId, session_id: 'session-a',
      data: { id: 0, type: 'text', content: 'live' },
    } as UIStreamEvent)

    await store.selectSession('session-b')
    returningToSessionA = true
    await store.selectSession('session-a')
    await flushPromises()
    await flushPromises()

    // The keyed Session view survives the round trip, including its optimistic
    // user turn; a failed refresh no longer reconstructs only the assistant.
    expect(store.messages.map(turn => turn.role)).toEqual(['user', 'assistant'])
    expect(store.messages[1]).toMatchObject({ role: 'assistant', streaming: true })

    returningToSessionA = false
    streamHandler?.({ type: 'end', run_id: runId, session_id: 'session-a' } as UIStreamEvent)
    await sending
  })

  it('routes interleaved websocket events by stream id', async () => {
    // Two parallel assistant streams in two sessions: each turn must be
    // updated by its own stream id, never crossed. Cross-session view
    // visibility (showing session A's content while on session B) is no
    // longer a thing — switching sessions issues a fresh REST refresh, and
    // any in-flight stream on a non-active session keeps writing to its
    // optimistic turn but the user does not see it until the server has
    // persisted the response and a switch-back triggers a fetch.
    sendEvents = []
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' },
      { id: 'session-b', bot_id: 'bot-1', title: 'B', type: 'chat' },
    ], nextCursor: null })
    api.fetchMessagesUI.mockResolvedValue([])

    const sent: Array<{ invocation_id?: string; session_id?: string; run_id?: string }> = []
    api.connectWebSocket.mockImplementation((_botId: string, onStreamEvent: UIStreamEventHandler) => {
      streamHandler = onStreamEvent
      return {
        get connected() {
          return true
        },
        send: vi.fn((message: { invocation_id?: string; session_id?: string; run_id?: string }) => {
          const runId = `run-${++wsRunSeq}`
          sent.push({ ...message, run_id: runId })
          onStreamEvent({
            type: 'run_accepted',
            run_id: runId,
            invocation_id: message.invocation_id,
            session_id: message.session_id,
          } as UIStreamEvent)
        }),
        abort: vi.fn(),
        close: vi.fn(),
        onOpen: null,
        onClose: null,
      }
    })

    const store = useChatStore()

    await store.selectBot('bot-1')
    const first = store.sendMessage('first')
    await flushPromises()

    await store.selectSession('session-b')
    await flushPromises()
    const second = store.sendMessage('second')
    await flushPromises()

    const runA = sent.find(item => item.session_id === 'session-a')?.run_id
    const runB = sent.find(item => item.session_id === 'session-b')?.run_id
    expect(runA).toBeTruthy()
    expect(runB).toBeTruthy()
    expect(store.isSessionStreaming('bot-1', 'session-a')).toBe(true)
    expect(store.isSessionStreaming('bot-1', 'session-b')).toBe(true)

    streamHandler?.({
      type: 'message',
      run_id: runA,
      session_id: 'session-a',
      data: { id: 0, type: 'text', content: 'answer A' },
    } as UIStreamEvent)
    streamHandler?.({
      type: 'message',
      run_id: runB,
      session_id: 'session-b',
      data: { id: 0, type: 'text', content: 'answer B' },
    } as UIStreamEvent)
    // Active session is B, so its optimistic turn shows the message.
    expect(store.sessionId).toBe('session-b')
    expect(store.messages).toEqual(expect.arrayContaining([
      expect.objectContaining({
        role: 'assistant',
        messages: [expect.objectContaining({ type: 'text', content: 'answer B' })],
      }),
    ]))

    streamHandler?.({ type: 'end', run_id: runA, session_id: 'session-a' } as UIStreamEvent)
    streamHandler?.({ type: 'end', run_id: runB, session_id: 'session-b' } as UIStreamEvent)
    await first
    await second
  })

  it('hydrates hidden subagent session summaries after selecting them', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-parent', bot_id: 'bot-1', title: 'Parent', type: 'chat' },
    ], nextCursor: null })
    api.fetchSession.mockResolvedValueOnce({
      id: 'session-subagent',
      bot_id: 'bot-1',
      title: 'Subagent',
      type: 'subagent',
      parent_session_id: 'session-parent',
    })
    api.fetchMessagesUI.mockResolvedValue([])

    const store = useChatStore()
    await store.selectBot('bot-1')
    await store.selectSession('session-subagent')

    expect(api.fetchSession).toHaveBeenCalledWith('bot-1', 'session-subagent')
    expect(store.activeSession).toMatchObject({
      id: 'session-subagent',
      type: 'subagent',
    })
    expect(store.knownSessionSummary('session-subagent')).toMatchObject({
      id: 'session-subagent',
      type: 'subagent',
    })
    expect(store.activeChatReadOnly).toBe(true)

    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-parent', bot_id: 'bot-1', title: 'Parent', type: 'chat' },
    ], nextCursor: null })
    await store.initialize()

    expect(store.sessionId).toBe('session-subagent')
    expect(store.activeSession).toMatchObject({
      id: 'session-subagent',
      type: 'subagent',
    })
    expect(store.knownSessionSummary('session-subagent')).toMatchObject({
      id: 'session-subagent',
      type: 'subagent',
    })
  })

  it('hydrates a missing summary even when selecting the already-persisted session id', async () => {
    api.fetchSession.mockResolvedValueOnce({
      id: 'acp-session-hidden',
      bot_id: 'bot-1',
      title: 'Codex',
      type: 'chat',
      session_mode: 'chat',
      runtime_type: 'acp_agent',
      runtime_metadata: {
        acp_agent_id: 'codex',
        project_path: '/data',
        acp_project_mode: 'project',
      },
    })
    api.fetchMessagesUI.mockResolvedValue([])
    const selection = useChatSelectionStore()
    selection.setBot('bot-1')
    selection.setSession('acp-session-hidden', { explicitSelection: true })

    const store = useChatStore()
    await store.selectSession('acp-session-hidden')

    expect(api.fetchSession).toHaveBeenCalledWith('bot-1', 'acp-session-hidden')
    expect(store.sessionId).toBe('acp-session-hidden')
    expect(store.hasExplicitSessionSelection).toBe(true)
    expect(store.activeSession).toMatchObject({
      id: 'acp-session-hidden',
      runtime_type: 'acp_agent',
      runtime_metadata: expect.objectContaining({ acp_agent_id: 'codex' }),
    })
  })

  it('switches to hidden sessions before their summary hydration resolves', async () => {
    api.fetchSessions.mockResolvedValueOnce({ items: [
      { id: 'session-visible', bot_id: 'bot-1', title: 'Visible', type: 'chat' },
    ], nextCursor: null })
    api.fetchMessagesUI.mockResolvedValueOnce([{
      id: 'visible-message',
      role: 'user',
      text: 'visible',
      attachments: [],
      timestamp: '2026-06-23T09:00:00.000Z',
    }])
    let resolveFetchSession: (session: unknown) => void = () => {}
    api.fetchSession.mockImplementationOnce(() => new Promise((resolve) => {
      resolveFetchSession = resolve
    }))
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    const selection = store.selectSession('session-hidden')
    await flushPromises()

    expect(store.sessionId).toBe('session-hidden')
    expect(store.messages).toEqual([])

    resolveFetchSession({
      id: 'session-hidden',
      bot_id: 'bot-1',
      title: 'Hidden',
      type: 'subagent',
      parent_session_id: 'session-visible',
    })
    await selection

    expect(store.activeSession).toMatchObject({
      id: 'session-hidden',
      type: 'subagent',
    })
  })

  it('paginates the sessions list and clears hasMoreSessions when the cursor is exhausted', async () => {
    api.fetchSessions
      .mockResolvedValueOnce({
        items: [
          { id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' },
          { id: 'session-2', bot_id: 'bot-1', title: 'B', type: 'chat' },
        ],
        nextCursor: 'cursor-2',
      })
      .mockResolvedValueOnce({
        items: [
          // Duplicate must be deduped; new entry appends.
          { id: 'session-2', bot_id: 'bot-1', title: 'B', type: 'chat' },
          { id: 'session-3', bot_id: 'bot-1', title: 'C', type: 'chat' },
        ],
        nextCursor: null,
      })
    const store = useChatStore()

    await store.selectBot('bot-1')
    expect(store.sessions.map(s => s.id)).toEqual(['session-1', 'session-2'])
    expect(store.hasMoreSessions).toBe(true)
    expect(store.sessionsCursor).toBe('cursor-2')

    await store.loadMoreSessions()

    expect(api.fetchSessions).toHaveBeenLastCalledWith('bot-1', { cursor: 'cursor-2' })
    expect(store.sessions.map(s => s.id)).toEqual(['session-1', 'session-2', 'session-3'])
    expect(store.hasMoreSessions).toBe(false)
    expect(store.sessionsCursor).toBeNull()

    // Further load attempts are a no-op once the cursor is exhausted.
    await store.loadMoreSessions()
    expect(api.fetchSessions).toHaveBeenCalledTimes(2)
  })

  it('resets hasLoadedOlder on initialize so a fresh bot does not inherit the previous scroll-back flag', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
      nextCursor: null,
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    // Simulate the user having scrolled back in the previous session.
    store._hasLoadedOlder = true
    expect(store._hasLoadedOlder).toBe(true)

    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-2', bot_id: 'bot-2', title: 'Chat 2', type: 'chat' }],
      nextCursor: null,
    })
    await store.selectBot('bot-2')
    await flushPromises()

    expect(store._hasLoadedOlder).toBe(false)
  })

  it('does not duplicate the optimistic user turn when stream-end refresh returns the persisted version', async () => {
    sendEvents = [
      { type: 'start' } as UIStreamEvent,
      {
        type: 'message',
        data: { id: 0, type: 'text', content: 'hello' },
      } as UIStreamEvent,
    ]
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
      nextCursor: null,
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    // Drive the actual send: this appends an optimistic user turn plus a
    // streaming assistant turn. The WS mock auto-replays sendEvents.
    const sendPromise = store.sendMessage('hi')
    await flushPromises()
    expect(store.messages.map(m => m.role)).toEqual(['user', 'assistant'])

    // Stream-end triggers refreshCurrentSession; the persisted user turn
    // carries server ids different from the optimistic ids and a server
    // timestamp slightly OLDER than the optimistic client timestamp (clock
    // skew). The previous timestamp-based merge heuristic misclassified
    // this as "user has scrolled back" and merged the two copies; the fix
    // keys off the explicit hasLoadedOlder flag instead.
    const past = new Date(Date.now() - 1_000).toISOString()
    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'server-user',
        role: 'user',
        text: 'hi',
        attachments: [],
        timestamp: past,
      },
      {
        id: 'server-assistant',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'hello', running: false }],
        timestamp: past,
      },
    ])
    streamHandler?.({ type: 'end', run_id: lastRunId, session_id: lastSessionId } as UIStreamEvent)
    await sendPromise
    await flushPromises()

    expect(store.messages.map(m => m.role)).toEqual(['user', 'assistant'])
    expect(store.messages[0]).toMatchObject({ role: 'user', text: 'hi' })
    expect(store._hasLoadedOlder).toBe(false)
  })

  it('keeps loadingMessages owned by the latest session start when an earlier refresh resolves late', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [],
      nextCursor: null,
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    // Inject sessions directly so we can drive selectSession ourselves
    // (the initialize path would auto-pick the first session and consume
    // the first fetchMessagesUI mock).
    store.sessions.push(
      { id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' } as never,
      { id: 'session-b', bot_id: 'bot-1', title: 'B', type: 'chat' } as never,
    )

    let resolveA: (v: unknown[]) => void = () => {}
    api.fetchMessagesUI.mockImplementationOnce(() => new Promise((resolve) => {
      resolveA = resolve as (v: unknown[]) => void
    }))
    store.selectSession('session-a')
    await flushPromises()
    expect(store.loadingMessages).toBe(true)

    let resolveB: (v: unknown[]) => void = () => {}
    api.fetchMessagesUI.mockImplementationOnce(() => new Promise((resolve) => {
      resolveB = resolve as (v: unknown[]) => void
    }))
    store.selectSession('session-b')
    await flushPromises()
    expect(store.loadingMessages).toBe(true)

    // A's late refresh resolves: its `finally` MUST NOT clear B's flag.
    resolveA([])
    await flushPromises()
    expect(store.loadingMessages).toBe(true)

    resolveB([])
    await flushPromises()
    expect(store.loadingMessages).toBe(false)
  })

  it('preserves scrolled-back history when an SSE refresh fires', async () => {
    // Initialize via fetchMessagesUI returning the most recent page; the
    // initialize path auto-selects the only session and triggers the
    // initial transcript refresh.
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
      nextCursor: null,
    })
    // Initial page must be >= PAGE_SIZE (30) so hasMoreOlder stays true and
    // loadOlderMessages is willing to fetch the older page.
    const initialPage = Array.from({ length: 30 }, (_, idx) => ({
      id: `recent-${idx}`,
      role: 'user' as const,
      text: `recent ${idx}`,
      attachments: [],
      timestamp: `2026-06-19T00:01:${String(idx).padStart(2, '0')}Z`,
    }))
    api.fetchMessagesUI.mockResolvedValueOnce(initialPage)
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()
    await flushPromises()
    expect(store.hasMoreOlder).toBe(true)
    expect(store.messages.length).toBe(30)

    // User scrolls back and pulls in older content.
    api.fetchMessagesUI.mockResolvedValueOnce([
      { id: 'msg-1', role: 'user', text: 'oldest', attachments: [], timestamp: '2026-06-19T00:00:01Z' },
      { id: 'msg-2', role: 'user', text: 'older', attachments: [], timestamp: '2026-06-19T00:00:02Z' },
    ])
    await store.loadOlderMessages()
    expect(store._hasLoadedOlder).toBe(true)
    expect(store.messages[0]?.id).toBe('msg-1')
    expect(store.messages[1]?.id).toBe('msg-2')
    expect(store.messages.length).toBe(32)

    // SSE-triggered refresh fetches only the most recent page; merge MUST
    // preserve the older content the user pulled in.
    api.fetchMessagesUI.mockResolvedValueOnce(initialPage)
    _sessionMessageHandler?.({
      type: 'message_created',
      message: { id: 'recent-29', session_id: 'session-1', created_at: '2026-06-19T00:01:29Z' },
    } as never)
    await new Promise(r => setTimeout(r, 150))
    await flushPromises()
    expect(store.messages[0]?.id).toBe('msg-1')
    expect(store.messages[1]?.id).toBe('msg-2')
  })

  it('replaces a scrolled-back optimistic user turn with its server twin instead of duplicating', async () => {
    // The id-keyed dedup in the previous mergeMessages happily kept the
    // optimistic and server copies side by side while the user was scrolled
    // back. Logical-turn matching (role + content + ~timestamp), gated on
    // the explicit `__optimistic` flag, collapses them in place. The flag
    // (not id shape) is what isOptimisticTurn keys off, so this test also
    // pins that a server turn whose id contains dashes (UUID-like) is NOT
    // treated as optimistic — the prior heuristic would have misclassified
    // it and eaten real history.
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
      nextCursor: null,
    })
    const initialPage = Array.from({ length: 30 }, (_, idx) => ({
      id: `recent-${idx}`,
      role: 'user' as const,
      text: `recent ${idx}`,
      attachments: [],
      timestamp: `2026-06-19T00:01:${String(idx).padStart(2, '0')}Z`,
    }))
    api.fetchMessagesUI.mockResolvedValueOnce(initialPage)
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()
    await flushPromises()

    // Pull older history so hasLoadedOlder flips on. Use a UUID-shaped id to
    // exercise the path where a server id contains dashes; the flag-based
    // isOptimisticTurn must still leave it alone.
    const dashedServerId = '550e8400-e29b-41d4-a716-446655440000'
    api.fetchMessagesUI.mockResolvedValueOnce([
      { id: dashedServerId, role: 'user', text: 'oldest', attachments: [], timestamp: '2026-06-19T00:00:01Z' },
    ])
    await store.loadOlderMessages()
    expect(store._hasLoadedOlder).toBe(true)
    const baseLength = store.messages.length
    // The dashed-id server turn survived the merge — it must not be classified
    // as optimistic regardless of id shape.
    expect(store.messages.some(m => m.id === dashedServerId)).toBe(true)

    // Push an optimistic user turn directly: simulates send while scrolled
    // back, without engaging the streaming machinery the surrounding suite
    // already covers. The id intentionally has no dash so the test cannot
    // accidentally pass via id-shape heuristics — the __optimistic flag is
    // what drives the merge.
    const optimisticTimestamp = '2026-06-19T00:02:00Z'
    store.messages.push({
      id: '1700000000000',
      role: 'user',
      text: 'just sent',
      attachments: [],
      timestamp: optimisticTimestamp,
      streaming: false,
      isSelf: true,
      __optimistic: true,
    } as never)
    expect(store.messages.length).toBe(baseLength + 1)

    // SSE-triggered refresh returns the server twin (different id, same
    // role+content, timestamp within 5s).
    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'server-user-1',
        role: 'user',
        text: 'just sent',
        attachments: [],
        timestamp: '2026-06-19T00:02:01Z',
      },
    ])
    _sessionMessageHandler?.({
      type: 'message_created',
      message: { id: 'server-user-1', session_id: 'session-1', created_at: '2026-06-19T00:02:01Z' },
    } as never)
    await new Promise(r => setTimeout(r, 150))
    await flushPromises()

    // Both copies would survive id-keyed dedup. Consolidation keeps the
    // optimistic render id stable so keyed chat DOM (including turn reserve)
    // does not remount, while recording the canonical server id separately.
    expect(store.messages.length).toBe(baseLength + 1)
    const justSent = store.messages.filter(m => m.role === 'user' && (m as { text?: string }).text === 'just sent')
    expect(justSent.length).toBe(1)
    expect(justSent[0]).toMatchObject({
      id: '1700000000000',
      serverId: 'server-user-1',
    })
    // And the dashed-id server turn from the older page is still here.
    expect(store.messages.some(m => m.id === dashedServerId)).toBe(true)
  })

  it('keeps hasMoreOlder true after a short initial page (turn count is not a terminal signal)', async () => {
    // The server pages by raw `bot_history_messages` rows but returns merged
    // UI turns, so a 30-row page collapses to ~28 turns even when thousands
    // of rows remain — the old `turns.length >= PAGE_SIZE` check truncated
    // long sessions to one page on first paint (Project Memoh: 1144 raw rows
    // collapsed to 28 turns => hasMoreOlder=false => scroll-up blocked).
    // Initial loads now stay optimistic; `loadOlderMessages` flips the flag
    // off the first time the server returns an authoritatively empty page.
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
      nextCursor: null,
    })
    const shortPage = Array.from({ length: 5 }, (_, idx) => ({
      id: `msg-${idx}`,
      role: 'user' as const,
      text: 'hi',
      attachments: [],
      timestamp: `2026-06-19T00:00:${String(idx).padStart(2, '0')}Z`,
    }))
    api.fetchMessagesUI.mockResolvedValueOnce(shortPage)
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()
    await flushPromises()
    expect(store.messages.length).toBe(5)
    expect(store.hasMoreOlder).toBe(true)
  })

  it('flips hasMoreOlder to false when the older page is empty and stops re-firing', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
      nextCursor: null,
    })
    // Initial page == PAGE_SIZE so hasMoreOlder is true after refresh; the
    // older fetch then returns empty to simulate end-of-history.
    const initialPage = Array.from({ length: 30 }, (_, idx) => ({
      id: `msg-${idx}`,
      role: 'user' as const,
      text: 'hi',
      attachments: [],
      timestamp: `2026-06-19T00:00:${String(idx).padStart(2, '0')}Z`,
    }))
    api.fetchMessagesUI.mockResolvedValueOnce(initialPage)
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()
    await flushPromises()
    expect(store.hasMoreOlder).toBe(true)

    api.fetchMessagesUI.mockResolvedValueOnce([])
    await store.loadOlderMessages()
    expect(store.hasMoreOlder).toBe(false)

    const callsBefore = api.fetchMessagesUI.mock.calls.length
    await store.loadOlderMessages()
    expect(api.fetchMessagesUI.mock.calls.length).toBe(callsBefore)
  })

  it('emits an explicit deleted-session signal after a session delete succeeds', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' },
        { id: 'session-2', bot_id: 'bot-1', title: 'B', type: 'chat' },
      ],
      nextCursor: null,
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    api.deleteSession.mockResolvedValueOnce(undefined)
    await store.removeSession('session-2')

    expect(api.deleteSession).toHaveBeenCalledWith('bot-1', 'session-2')
    expect(store.deletedSession).toEqual({
      id: 'session-2',
      botId: 'bot-1',
      seq: 1,
    })
    expect(store.sessions.map(session => session.id)).toEqual(['session-1'])
    expect(store.sessionId).toBe('session-1')
  })

  it('aborts a deleted Session stream and ignores its late events in the focused Session', async () => {
    sendEvents = [{ type: 'start' } as UIStreamEvent]
    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' },
        { id: 'session-2', bot_id: 'bot-1', title: 'B', type: 'chat' },
      ],
      nextCursor: null,
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    const sending = store.sendMessage('stream in A')
    await flushPromises()
    const runId = lastRunId
    expect(runId).not.toBe('')

    await store.selectSession('session-2')
    api.deleteSession.mockResolvedValueOnce(undefined)
    await store.removeSession('session-1')
    await expect(sending).resolves.toMatchObject({ ok: false, stage: 'stream' })

    streamHandler?.({
      type: 'message',
      run_id: runId,
      session_id: 'session-1',
      data: { id: 1, type: 'text', content: 'late A output' },
    } as UIStreamEvent)
    streamHandler?.({
      type: 'end',
      run_id: runId,
      session_id: 'session-1',
    } as UIStreamEvent)
    await flushPromises()

    expect(abortedWSRuns).toContain(runId)
    expect(store.sessionId).toBe('session-2')
    expect(store.messages).toEqual([])
    expect(store.sessions.map(session => session.id)).toEqual(['session-2'])
  })

  it('does not fall back to a hidden schedule session after deleting the active recent session', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'schedule-1', bot_id: 'bot-1', title: 'Morning run', type: 'schedule' },
        { id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' },
      ],
      nextCursor: null,
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    expect(store.sessionId).toBe('session-1')

    api.deleteSession.mockResolvedValueOnce(undefined)
    await store.removeSession('session-1')

    expect(store.sessions.map(session => session.id)).toEqual(['schedule-1'])
    expect(store.sessionId).toBeNull()
    expect(store.messages).toEqual([])
  })

  it('falls back within the schedule sidebar mode when deleting an active schedule session', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'schedule-1', bot_id: 'bot-1', title: 'Morning run', type: 'schedule' },
        { id: 'schedule-2', bot_id: 'bot-1', title: 'Evening run', type: 'schedule' },
      ],
      nextCursor: null,
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    expect(store.sessionId).toBe('schedule-1')

    api.deleteSession.mockResolvedValueOnce(undefined)
    await store.removeSession('schedule-1', { fallbackMode: 'schedule' })

    expect(store.sessions.map(session => session.id)).toEqual(['schedule-2'])
    expect(store.sessionId).toBe('schedule-2')
  })

  it('does not mutate the active bot state when a delete resolves after switching bots', async () => {
    api.fetchBots.mockResolvedValue([
      { id: 'bot-1', status: 'active', name: 'Bot A' },
      { id: 'bot-2', status: 'active', name: 'Bot B' },
    ])
    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'shared-session', bot_id: 'bot-1', title: 'A shared id', type: 'chat' },
        { id: 'session-a2', bot_id: 'bot-1', title: 'A2', type: 'chat' },
      ],
      nextCursor: null,
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    let resolveDelete: () => void = () => {}
    api.deleteSession.mockImplementationOnce(() => new Promise<void>((resolve) => {
      resolveDelete = resolve
    }))
    const deletePromise = store.removeSession('shared-session')

    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'shared-session', bot_id: 'bot-2', title: 'B shared id', type: 'chat' },
        { id: 'session-b2', bot_id: 'bot-2', title: 'B2', type: 'chat' },
      ],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockResolvedValueOnce([
      {
        id: 'bot-2-user',
        role: 'user',
        text: 'bot two prompt',
        timestamp: '2026-06-20T00:00:00.000Z',
      },
      {
        id: 'bot-2-assistant',
        role: 'assistant',
        messages: [{ id: 1, type: 'text', content: 'bot two reply' }],
        timestamp: '2026-06-20T00:00:01.000Z',
      },
    ])
    await store.selectBot('bot-2')
    await flushPromises()
    sendEvents = [{ type: 'start' } as UIStreamEvent]
    const botTwoSend = store.sendMessage('keep bot two streaming')
    await flushPromises()
    const botTwoRunId = lastRunId

    expect(store.isChatViewStreaming({
      botId: 'bot-1',
      sessionId: 'shared-session',
      viewId: 'chat:bot-1',
    })).toBe(false)
    expect(store.isChatViewStreaming({
      botId: 'bot-2',
      sessionId: 'shared-session',
      viewId: 'chat:bot-2',
    })).toBe(true)

    resolveDelete()
    await deletePromise

    expect(store.currentBotId).toBe('bot-2')
    expect(store.sessions.map(session => session.id)).toEqual(['shared-session', 'session-b2'])
    expect(store.sessionId).toBe('shared-session')
    expect(store.messages.slice(0, 2).map(message => message.id)).toEqual(['bot-2-user', 'bot-2-assistant'])
    expect(abortedWSRuns).not.toContain(botTwoRunId)
    expect(store.deletedSession).toEqual({
      id: 'shared-session',
      botId: 'bot-1',
      seq: 1,
    })
    streamHandler?.({
      type: 'end',
      run_id: botTwoRunId,
      session_id: 'shared-session',
    } as UIStreamEvent)
    await expect(botTwoSend).resolves.toMatchObject({ ok: true })
  })

  it('does not resurrect a deleted session when an older same-bot list refresh resolves late', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' },
        { id: 'session-2', bot_id: 'bot-1', title: 'B', type: 'chat' },
      ],
      nextCursor: null,
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    let resolveRefresh: (value: { items: Array<{ id: string, bot_id: string, title: string, type: string }>, nextCursor: null }) => void = () => {}
    api.fetchSessions.mockImplementationOnce(() => new Promise((resolve) => {
      resolveRefresh = resolve
    }))
    sessionsActivityHandler?.({
      type: 'session_created',
      session_id: 'session-3',
      session_type: 'chat',
      title: 'C',
    })
    await flushPromises()

    api.deleteSession.mockResolvedValueOnce(undefined)
    await store.removeSession('session-2')

    resolveRefresh({
      items: [
        { id: 'session-2', bot_id: 'bot-1', title: 'Deleted stale copy', type: 'chat' },
        { id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' },
        { id: 'session-3', bot_id: 'bot-1', title: 'C', type: 'chat' },
      ],
      nextCursor: null,
    })
    await flushPromises()

    expect(store.sessions.map(session => session.id)).toEqual(['session-1', 'session-3'])
    expect(store.knownSessionSummary('session-2')).toBeNull()
  })

  it('does not select a tombstoned session from a stale initialize response', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' },
        { id: 'session-2', bot_id: 'bot-1', title: 'B', type: 'chat' },
      ],
      nextCursor: null,
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    api.deleteSession.mockResolvedValueOnce(undefined)
    await store.removeSession('session-2')

    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'session-2', bot_id: 'bot-1', title: 'Deleted stale copy', type: 'chat' },
      ],
      nextCursor: null,
    })
    await store.initialize()

    expect(store.sessions).toEqual([])
    expect(store.sessionId).toBeNull()
    expect(store.knownSessionSummary('session-2')).toBeNull()
  })

  it('refreshes the current transcript when the session message stream reports dropped events', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' }],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockResolvedValueOnce([
      { id: 'message-1', role: 'user', text: 'old', attachments: [], timestamp: '2026-06-20T00:00:00.000Z' },
    ])
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()
    expect(store.messages.map(message => message.id)).toEqual(['message-1'])

    api.fetchMessagesUI.mockResolvedValueOnce([
      { id: 'message-2', role: 'user', text: 'refreshed', attachments: [], timestamp: '2026-06-20T00:00:01.000Z' },
    ])
    _sessionMessageHandler?.({ type: 'dropped', count: 3 })
    await flushPromises()

    expect(api.fetchMessagesUI).toHaveBeenLastCalledWith('bot-1', 'session-1', { limit: 30 })
    expect(store.messages.map(message => message.id)).toEqual(['message-2'])
  })

  it('refreshes the session list when the bot activity stream reports dropped events', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' }],
      nextCursor: null,
    })
    const store = useChatStore()

    await store.selectBot('bot-1')
    await flushPromises()

    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'session-2', bot_id: 'bot-1', title: 'B', type: 'chat' },
        { id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' },
      ],
      nextCursor: null,
    })
    sessionsActivityHandler?.({ type: 'dropped', count: 2 })
    await flushPromises()

    expect(store.sessions.map(session => session.id)).toEqual(['session-2', 'session-1'])
  })

  it('appends sessions emitted by the bot-wide activity stream', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' }],
      nextCursor: null,
    })
    const store = useChatStore()

    await store.selectBot('bot-1')

    // session_created on the activity stream triggers a sessions-list reload
    // (the server payload omits session type/metadata so a client-built stub
    // would be incomplete).
    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'session-2', bot_id: 'bot-1', title: 'New', type: 'discuss' },
        { id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' },
      ],
      nextCursor: null,
    })
    sessionsActivityHandler?.({
      type: 'session_created',
      session_id: 'session-2',
      session_type: 'discuss',
      title: 'New',
    })
    await flushPromises()

    expect(store.sessions.map(s => s.id)).toEqual(['session-2', 'session-1'])
  })

  it('keeps A and B transcripts independent while non-focused B receives Session SSE', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' },
        { id: 'session-b', bot_id: 'bot-1', title: 'B', type: 'chat' },
      ],
      nextCursor: null,
    })
    const latest = new Map([
      ['session-a', [{ id: 'a-1', role: 'user', text: 'from A', attachments: [], timestamp: '2026-07-11T00:00:00Z' }]],
      ['session-b', [{ id: 'b-1', role: 'user', text: 'from B', attachments: [], timestamp: '2026-07-11T00:00:01Z' }]],
    ])
    api.fetchMessagesUI.mockImplementation(async (_botId: string, targetSessionId: string) => latest.get(targetSessionId) ?? [])
    const store = useChatStore()

    await store.selectBot('bot-1')
    const targetA = { botId: 'bot-1', sessionId: 'session-a', viewId: 'chat:a' }
    const targetB = { botId: 'bot-1', sessionId: 'session-b', viewId: 'chat:b' }
    store.bindChatView('chat:a', targetA, true)
    store.bindChatView('chat:b', targetB, true)
    await flushPromises()
    await flushPromises()

    expect(store.chatView(targetA).transcript.messages.map(message => message.id)).toEqual(['a-1'])
    expect(store.chatView(targetB).transcript.messages.map(message => message.id)).toEqual(['b-1'])
    expect(sessionMessageHandlers.has('bot-1:session-a')).toBe(true)
    expect(sessionMessageHandlers.has('bot-1:session-b')).toBe(true)

    store.focusChatView('chat:a')
    await store.selectSession('session-a')
    latest.set('session-b', [
      { id: 'b-1', role: 'user', text: 'from B', attachments: [], timestamp: '2026-07-11T00:00:01Z' },
      { id: 'b-2', role: 'assistant', messages: [{ id: 0, type: 'text', content: 'B updated' }], timestamp: '2026-07-11T00:00:02Z' },
    ])
    sessionMessageHandlers.get('bot-1:session-b')?.({
      type: 'message_created',
      message: { id: 'b-2', session_id: 'session-b', created_at: '2026-07-11T00:00:02Z' },
    } as never)
    await new Promise(resolve => setTimeout(resolve, 150))
    await flushPromises()

    expect(store.sessionId).toBe('session-a')
    expect(store.chatView(targetA).transcript.messages.map(message => message.id)).toEqual(['a-1'])
    expect(store.chatView(targetB).transcript.messages.map(message => message.id)).toEqual(['b-1', 'b-2'])
  })

  it('hydrates a visible non-focused Session summary before it is activated', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' }],
      nextCursor: null,
    })
    api.fetchSession.mockResolvedValueOnce({
      id: 'session-b',
      bot_id: 'bot-1',
      title: 'Hidden subagent',
      type: 'subagent',
      runtime_type: 'acp_agent',
      runtime_metadata: { acp_agent_id: 'codex' },
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()
    const targetA = { botId: 'bot-1', sessionId: 'session-a', viewId: 'chat:a' }
    const targetB = { botId: 'bot-1', sessionId: 'session-b', viewId: 'chat:b' }
    store.bindChatView(targetA.viewId, targetA, true)
    store.focusChatView(targetA.viewId)

    store.bindChatView(targetB.viewId, targetB, true)
    await flushPromises()

    expect(store.sessionId).toBe('session-a')
    expect(store.chatTargetFor(targetB)).toMatchObject({
      session: { id: 'session-b', type: 'subagent' },
      runtimeType: 'acp_agent',
      isACP: true,
    })
    expect(store.chatReadOnlyFor(targetB)).toBe(true)
    expect(api.fetchSession).toHaveBeenCalledWith('bot-1', 'session-b')
  })

  it('keeps an unknown real Session read-only until its summary confirms it is writable', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' }],
      nextCursor: null,
    })
    const summary = deferred<{
      id: string
      bot_id: string
      title: string
      type: string
    }>()
    api.fetchSession.mockReturnValueOnce(summary.promise)
    const store = useChatStore()
    await store.selectBot('bot-1')
    const targetB = { botId: 'bot-1', sessionId: 'session-b', viewId: 'chat:b' }
    store.bindChatView(targetB.viewId, targetB, true)
    await flushPromises()

    expect(store.chatReadOnlyFor(targetB)).toBe(true)
    await expect(store.sendMessage('must not send', undefined, { target: targetB })).resolves.toMatchObject({
      ok: false,
      stage: 'startup',
    })
    expect(sentWSMessages).toEqual([])

    summary.resolve({ id: 'session-b', bot_id: 'bot-1', title: 'B', type: 'chat' })
    await flushPromises()
    expect(store.chatReadOnlyFor(targetB)).toBe(false)
  })

  it('does not remember a visible Session summary that resolves after deletion', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' }],
      nextCursor: null,
    })
    const summary = deferred<{
      id: string
      bot_id: string
      title: string
      type: string
    }>()
    api.fetchSession.mockReturnValueOnce(summary.promise)
    const store = useChatStore()
    await store.selectBot('bot-1')
    const targetB = { botId: 'bot-1', sessionId: 'session-b', viewId: 'chat:b' }
    store.bindChatView(targetB.viewId, targetB, true)
    await flushPromises()

    api.deleteSession.mockResolvedValueOnce(undefined)
    await store.removeSession('session-b')
    summary.resolve({ id: 'session-b', bot_id: 'bot-1', title: 'Deleted B', type: 'chat' })
    await flushPromises()

    expect(store.knownSessionSummary('session-b')).toBeNull()
    expect(store.sessions.some(session => session.id === 'session-b')).toBe(false)
  })

  it('routes an optimistic send and abort only to its explicit pane target', async () => {
    sendEvents = [{ type: 'start' } as UIStreamEvent]
    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' },
        { id: 'session-b', bot_id: 'bot-1', title: 'B', type: 'chat' },
      ],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockResolvedValue([])
    const store = useChatStore()
    await store.selectBot('bot-1')
    const targetA = { botId: 'bot-1', sessionId: 'session-a', viewId: 'chat:a' }
    const targetB = { botId: 'bot-1', sessionId: 'session-b', viewId: 'chat:b' }
    store.bindChatView('chat:a', targetA, true)
    store.bindChatView('chat:b', targetB, true)
    store.focusChatView('chat:b')
    await store.selectSession('session-b')

    const sending = store.sendMessage('send to A', undefined, {
      target: targetA,
      composerScope: 'bot-1:chat:a',
    })
    await flushPromises()

    expect(store.chatView(targetA).transcript.messages.map(message => message.role)).toEqual(['user', 'assistant'])
    expect(store.chatView(targetB).transcript.messages).toEqual([])
    expect(sentWSMessages.at(-1)).toMatchObject({ session_id: 'session-a', composer_scope: 'bot-1:chat:a' })

    store.abort(targetA)
    await expect(sending).resolves.toMatchObject({ ok: false, stage: 'stream' })
    expect(store.sessionId).toBe('session-b')
    expect(store.chatView(targetB).transcript.messages).toEqual([])
  })

  it('shares one Session transcript and one Session SSE across two visible panes', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' }],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockResolvedValue([
      { id: 'a-1', role: 'user', text: 'shared', attachments: [], timestamp: '2026-07-11T00:00:00Z' },
    ])
    const store = useChatStore()
    await store.selectBot('bot-1')
    const first = store.bindChatView('chat:left', {
      botId: 'bot-1', sessionId: 'session-a', viewId: 'chat:left',
    }, true)
    const second = store.bindChatView('chat:right', {
      botId: 'bot-1', sessionId: 'session-a', viewId: 'chat:right',
    }, true)
    await flushPromises()

    expect(second).toBe(first)
    expect(first.transcript.messages.map(message => message.id)).toEqual(['a-1'])
    expect(api.streamSessionMessageEvents.mock.calls.filter(call => call[1] === 'session-a')).toHaveLength(1)
  })

  it('detaches the Session SSE when its last pane hides and reconnects from cache', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' }],
      nextCursor: null,
    })
    api.fetchMessagesUI.mockResolvedValue([
      { id: 'a-1', role: 'user', text: 'cached', attachments: [], timestamp: '2026-07-11T00:00:00Z' },
    ])
    const store = useChatStore()
    await store.selectBot('bot-1')
    const target = { botId: 'bot-1', sessionId: 'session-a', viewId: 'chat:left' }
    const cached = store.bindChatView('chat:left', target, true)
    store.bindChatView('chat:right', { ...target, viewId: 'chat:right' }, true)
    await flushPromises()

    expect(sessionMessageHandlers.has('bot-1:session-a')).toBe(true)
    store.setChatViewVisible('chat:left', false)
    expect(sessionMessageHandlers.has('bot-1:session-a')).toBe(true)

    store.setChatViewVisible('chat:right', false)
    expect(sessionMessageHandlers.has('bot-1:session-a')).toBe(false)
    expect(store.chatView(target)).toBe(cached)
    expect(cached.transcript.messages.map(message => message.id)).toEqual(['a-1'])

    store.setChatViewVisible('chat:left', true)
    await flushPromises()
    expect(sessionMessageHandlers.has('bot-1:session-a')).toBe(true)
    expect(store.chatView(target)).toBe(cached)
  })

  it('keeps Draft ACP Agent state scoped by pane and closes only the removed Draft runtime', async () => {
    const store = useChatStore()
    await store.selectBot('bot-1')
    const targetA = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-a' }
    const targetB = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-b' }
    store.bindChatView(targetA.viewId, targetA, true)
    store.bindChatView(targetB.viewId, targetB, true)

    store.focusChatView(targetA.viewId)
    store.stageACPSession({ agentId: 'codex' }, {}, targetA)
    await store.ensurePendingACPRuntime(targetA)
    store.focusChatView(targetB.viewId)
    store.stageACPSession({ agentId: 'claude' }, {}, targetB)

    expect(store.pendingACPStateFor(targetA)).toMatchObject({
      metadata: { acp_agent_id: 'codex' },
      runtimeId: 'rt_warm',
    })
    expect(store.pendingACPStateFor(targetB)).toMatchObject({
      metadata: { acp_agent_id: 'claude' },
    })
    expect(api.closeACPRuntime).not.toHaveBeenCalled()

    store.focusChatView(targetA.viewId)
    expect(store.pendingACPSessionMetadata).toMatchObject({ acp_agent_id: 'codex' })
    store.unbindChatView(targetA.viewId)

    expect(api.closeACPRuntime).toHaveBeenCalledWith('bot-1', 'rt_warm')
    expect(store.pendingACPStateFor(targetB)).toMatchObject({ metadata: { acp_agent_id: 'claude' } })
  })

  it('does not let a late native Draft creation steal focus from another Draft', async () => {
    const creation = deferred<{
      id: string
      bot_id: string
      title: string
      type: string
    }>()
    api.createSession.mockReturnValueOnce(creation.promise)
    const store = useChatStore()
    await store.selectBot('bot-1')
    const targetA = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-a' }
    const targetB = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-b' }
    store.bindChatView(targetA.viewId, targetA, true)
    store.bindChatView(targetB.viewId, targetB, true)
    store.focusChatView(targetA.viewId)

    const sending = store.sendMessage('from A', undefined, { target: targetA })
    await flushPromises()
    store.focusChatView(targetB.viewId)
    store.selectDraft({ explicitSelection: true })
    creation.resolve({ id: 'session-a', bot_id: 'bot-1', title: '', type: 'chat' })
    await sending

    expect(store.sessionId).toBeNull()
    expect(store.chatView({ ...targetA, sessionId: 'session-a' }).sessionId).toBe('session-a')
    expect(store.chatView(targetB).kind).toBe('draft')
    expect(store.userSentInSession).toMatchObject({ id: 'session-a', viewId: targetA.viewId })
  })

  it('drops a late Draft creation result after the authenticated scope resets', async () => {
    const windowTarget = new EventTarget()
    vi.stubGlobal('window', windowTarget)
    const creation = deferred<{
      id: string
      bot_id: string
      title: string
      type: string
    }>()
    api.createSession.mockReturnValueOnce(creation.promise)
    const store = useChatStore()
    await store.selectBot('bot-1')
    const target = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-a' }
    store.bindChatView(target.viewId, target, true)
    store.focusChatView(target.viewId)

    const sending = store.sendMessage('old user send', undefined, { target })
    await flushPromises()
    windowTarget.dispatchEvent(new CustomEvent(AUTH_SESSION_CLEARED_EVENT, {
      detail: { reason: 'logout' },
    }))
    creation.resolve({ id: 'old-session', bot_id: 'bot-1', title: '', type: 'chat' })

    await expect(sending).resolves.toMatchObject({ ok: false })
    expect(store.sessions).toEqual([])
    expect(store.knownSessionSummary('old-session')).toBeNull()
    expect(store.currentBotId).toBeNull()
    expect(store.isChatViewCreatingSession(target)).toBe(false)
  })

  it('restores a failed ACP creation to its owning Draft after focus moves', async () => {
    const creation = deferred<{
      id: string
      bot_id: string
      title: string
      type: string
    }>()
    api.createSession.mockReturnValueOnce(creation.promise)
    const store = useChatStore()
    await store.selectBot('bot-1')
    const targetA = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-a' }
    const targetB = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-b' }
    store.bindChatView(targetA.viewId, targetA, true)
    store.bindChatView(targetB.viewId, targetB, true)
    store.focusChatView(targetA.viewId)
    store.stageACPSession({ agentId: 'codex' }, {}, targetA)

    const sending = store.sendMessage('from ACP A', undefined, { target: targetA })
    await flushPromises()
    store.focusChatView(targetB.viewId)
    store.selectDraft({ explicitSelection: true })
    store.stageACPSession({ agentId: 'claude' }, {}, targetB)
    creation.reject(new Error('create failed'))
    await expect(sending).resolves.toMatchObject({ ok: false, stage: 'startup' })

    expect(store.pendingACPStateFor(targetA)).toMatchObject({ metadata: { acp_agent_id: 'codex' } })
    expect(store.pendingACPStateFor(targetB)).toMatchObject({ metadata: { acp_agent_id: 'claude' } })
    expect(store.pendingACPSessionMetadata).toMatchObject({ acp_agent_id: 'claude' })
    expect(store.sessionId).toBeNull()
  })

  it('creates an explicit non-focused ACP Draft with its saved Agent and warm runtime', async () => {
    sendEvents = [{ type: 'start' } as UIStreamEvent]
    const store = useChatStore()
    await store.selectBot('bot-1')
    const targetA = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-a' }
    const targetB = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-b' }
    store.bindChatView(targetA.viewId, targetA, true)
    store.bindChatView(targetB.viewId, targetB, true)
    store.focusChatView(targetA.viewId)
    store.stageACPSession({ agentId: 'codex' }, {}, targetA)
    await store.ensurePendingACPRuntime(targetA)
    store.focusChatView(targetB.viewId)
    store.selectDraft({ explicitSelection: true })

    const sending = store.sendMessage('run in A', undefined, { target: targetA })
    await flushPromises()
    await flushPromises()

    expect(api.createSession).toHaveBeenLastCalledWith('bot-1', expect.objectContaining({
      runtimeType: 'acp_agent',
      acpRuntimeId: 'rt_warm',
      runtimeMetadata: expect.objectContaining({ acp_agent_id: 'codex' }),
    }))
    expect(store.sessionId).toBeNull()
    expect(store.pendingACPStateFor(targetA)).toBeNull()
    expect(api.closeACPRuntime).not.toHaveBeenCalledWith('bot-1', 'rt_warm')

    store.abort({ ...targetA, sessionId: 'session-1' })
    await expect(sending).resolves.toMatchObject({ ok: false, stage: 'stream' })
  })

  it('does not let a late session_created event steal focus from another Draft', async () => {
    sendEvents = []
    const store = useChatStore()
    await store.selectBot('bot-1')
    const targetA = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-a' }
    const targetB = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-b' }
    store.bindChatView(targetA.viewId, targetA, true)
    store.bindChatView(targetB.viewId, targetB, true)
    store.focusChatView(targetA.viewId)
    const sending = store.sendMessage('activate A', undefined, {
      target: targetA,
      requestedSkills: [{ name: 'alpha' }],
    })
    await flushPromises()
    const invocationId = wsInvocationId(0)
    const runId = wsRunId(0)

    store.focusChatView(targetB.viewId)
    store.selectDraft({ explicitSelection: true })
    streamHandler?.({
      type: 'session_created',
      invocation_id: invocationId,
      session_id: 'session-a',
    } as UIStreamEvent)

    expect(store.sessionId).toBeNull()
    expect(store.chatView({ ...targetA, sessionId: 'session-a' }).sessionId).toBe('session-a')
    expect(store.chatView(targetB).kind).toBe('draft')

    streamHandler?.({ type: 'end', run_id: runId, session_id: 'session-a' } as UIStreamEvent)
    await expect(sending).resolves.toMatchObject({ ok: true })
  })

  it('cleans a non-focused deferred Session view and SSE after startup failure', async () => {
    sendEvents = []
    const store = useChatStore()
    await store.selectBot('bot-1')
    const targetA = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-a' }
    const targetB = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-b' }
    store.bindChatView(targetA.viewId, targetA, true)
    store.bindChatView(targetB.viewId, targetB, true)
    store.focusChatView(targetA.viewId)

    const sending = store.sendMessage('activate A', [{
      type: 'file',
      base64: 'data:text/plain;base64,aGVsbG8=',
      mime: 'text/plain',
      name: 'note.txt',
    }], {
      target: targetA,
      requestedSkills: [{ name: 'alpha' }],
      composerScope: 'bot-1:chat:draft-a',
    })
    await flushPromises()
    const invocationId = wsInvocationId(0)
    store.focusChatView(targetB.viewId)
    store.selectDraft({ explicitSelection: true })

    streamHandler?.({
      type: 'session_created',
      invocation_id: invocationId,
      session_id: 'created-a',
    } as UIStreamEvent)
    await flushPromises()
    expect(sessionMessageHandlers.has('bot-1:created-a')).toBe(true)

    streamHandler?.({
      type: 'command_error',
      invocation_id: invocationId,
      session_id: 'created-a',
      composer_scope: 'bot-1:chat:draft-a',
      terminal: true,
      error: { code: 'unsupported', message: 'preflight failed' },
    } as UIStreamEvent)

    await expect(sending).resolves.toMatchObject({ ok: false, stage: 'startup' })
    expect(store.sessionId).toBeNull()
    expect(store.deletedSession).toMatchObject({ id: 'created-a', botId: 'bot-1' })
    expect(sessionMessageHandlers.has('bot-1:created-a')).toBe(false)
  })

  it('does not clear Draft B staging when Session A Agent update resolves late', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' }],
      nextCursor: null,
    })
    const update = deferred<{
      id: string
      bot_id: string
      title: string
      type: string
      runtime_type: string
      metadata: Record<string, unknown>
    }>()
    api.updateSessionAgent.mockReturnValueOnce(update.promise)
    const store = useChatStore()
    await store.selectBot('bot-1')
    const targetA = { botId: 'bot-1', sessionId: 'session-a', viewId: 'chat:session-a' }
    const targetB = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-b' }
    store.bindChatView(targetA.viewId, targetA, true)
    store.bindChatView(targetB.viewId, targetB, true)
    store.focusChatView(targetA.viewId)
    await store.selectSession('session-a')

    const updating = store.updateCurrentSessionAgent({ agentId: 'codex' }, targetA)
    store.focusChatView(targetB.viewId)
    store.selectDraft({ explicitSelection: true })
    store.stageACPSession({ agentId: 'claude' }, {}, targetB)
    await store.ensurePendingACPRuntime(targetB)
    update.resolve({
      id: 'session-a',
      bot_id: 'bot-1',
      title: 'A',
      type: 'acp_agent',
      runtime_type: 'acp_agent',
      metadata: { acp_agent_id: 'codex' },
    })
    await updating

    expect(store.sessionId).toBeNull()
    expect(store.pendingACPStateFor(targetB)).toMatchObject({
      metadata: { acp_agent_id: 'claude' },
      runtimeId: 'rt_warm',
    })
    expect(api.closeACPRuntime).not.toHaveBeenCalledWith('bot-1', 'rt_warm')
  })

  it('routes a late /new Agent result back to its origin Draft request', async () => {
    const store = useChatStore()
    await store.selectBot('bot-1')
    const targetA = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-a' }
    const targetB = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-b' }
    store.bindChatView(targetA.viewId, targetA, true)
    store.bindChatView(targetB.viewId, targetB, true)
    store.focusChatView(targetA.viewId)
    const settings = deferred<{ data: {
      chat_runtime: string
      chat_acp_agent_id: string
      chat_acp_project_path: string
      chat_acp_project_mode: string
    } }>()
    sdk.getBotsByBotIdSettings.mockReturnValueOnce(settings.promise)

    const command = store.sendMessage('/new codex', undefined, { target: targetA })
    await flushPromises()
    store.focusChatView(targetB.viewId)
    store.selectDraft({ explicitSelection: true })
    store.stageACPSession({ agentId: 'claude' }, {}, targetB)
    settings.resolve({ data: {
      chat_runtime: 'acp_agent',
      chat_acp_agent_id: 'codex',
      chat_acp_project_path: '/data/a',
      chat_acp_project_mode: 'project',
    } })

    await expect(command).resolves.toMatchObject({ ok: true })
    expect(store.sessionId).toBeNull()
    expect(store.pendingACPStateFor(targetB)).toMatchObject({ metadata: { acp_agent_id: 'claude' } })
    expect(store.draftViewRequested).toMatchObject({
      botId: 'bot-1',
      viewId: targetA.viewId,
      expectedSessionId: null,
      input: { agentId: 'codex', projectPath: '/data/a', projectMode: 'project' },
      activate: true,
    })
  })

  it('keeps the newest /new Agent choice when an older settings request resolves last', async () => {
    const codexSettings = deferred<{ data: {
      chat_runtime: string
      chat_acp_agent_id: string
      chat_acp_project_path: string
      chat_acp_project_mode: string
    } }>()
    const claudeSettings = deferred<{ data: {
      chat_runtime: string
      chat_acp_agent_id: string
      chat_acp_project_path: string
      chat_acp_project_mode: string
    } }>()
    const store = useChatStore()
    await store.selectBot('bot-1')
    sdk.getBotsByBotIdSettings
      .mockReturnValueOnce(codexSettings.promise)
      .mockReturnValueOnce(claudeSettings.promise)
    const target = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-a' }
    store.bindChatView(target.viewId, target, true)
    store.focusChatView(target.viewId)

    const older = store.sendMessage('/new codex', undefined, { target })
    await flushPromises()
    const newer = store.sendMessage('/new claude-code', undefined, { target })
    await flushPromises()
    claudeSettings.resolve({ data: {
      chat_runtime: 'acp_agent',
      chat_acp_agent_id: 'claude-code',
      chat_acp_project_path: '/data/claude',
      chat_acp_project_mode: 'project',
    } })
    await newer
    const latestRequest = store.draftViewRequested
    expect(latestRequest).toMatchObject({
      viewId: target.viewId,
      input: { agentId: 'claude-code', projectPath: '/data/claude' },
    })

    codexSettings.resolve({ data: {
      chat_runtime: 'acp_agent',
      chat_acp_agent_id: 'codex',
      chat_acp_project_path: '/data/codex',
      chat_acp_project_mode: 'project',
    } })
    await older

    expect(store.draftViewRequested).toBe(latestRequest)
    expect(store.draftViewRequested?.input?.agentId).toBe('claude-code')
  })

  it('drops a late /new Agent result after the authenticated scope resets', async () => {
    const windowTarget = new EventTarget()
    vi.stubGlobal('window', windowTarget)
    const settings = deferred<{ data: {
      chat_runtime: string
      chat_acp_agent_id: string
      chat_acp_project_path: string
      chat_acp_project_mode: string
    } }>()
    const store = useChatStore()
    await store.selectBot('bot-1')
    sdk.getBotsByBotIdSettings.mockReturnValueOnce(settings.promise)
    const target = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-a' }
    store.bindChatView(target.viewId, target, true)
    store.focusChatView(target.viewId)

    const command = store.sendMessage('/new codex', undefined, { target })
    await flushPromises()
    windowTarget.dispatchEvent(new CustomEvent(AUTH_SESSION_CLEARED_EVENT, {
      detail: { reason: 'logout' },
    }))
    settings.resolve({ data: {
      chat_runtime: 'acp_agent',
      chat_acp_agent_id: 'codex',
      chat_acp_project_path: '/data/a',
      chat_acp_project_mode: 'project',
    } })

    await expect(command).resolves.toMatchObject({ ok: true })
    expect(store.draftViewRequested).toBeNull()
    expect(store.currentBotId).toBeNull()
  })

  it('keeps a manual Draft Agent choice made after a deferred /new command', async () => {
    const settings = deferred<{ data: {
      chat_runtime: string
      chat_acp_agent_id: string
      chat_acp_project_path: string
      chat_acp_project_mode: string
    } }>()
    const store = useChatStore()
    await store.selectBot('bot-1')
    sdk.getBotsByBotIdSettings.mockReturnValueOnce(settings.promise)
    const target = { botId: 'bot-1', sessionId: null, viewId: 'chat:draft-a' }
    store.bindChatView(target.viewId, target, true)
    store.focusChatView(target.viewId)

    const command = store.sendMessage('/new codex', undefined, { target })
    await flushPromises()
    store.stageACPSession({ agentId: 'claude' }, {}, target)
    await store.ensurePendingACPRuntime(target)

    settings.resolve({ data: {
      chat_runtime: 'acp_agent',
      chat_acp_agent_id: 'codex',
      chat_acp_project_path: '/data/codex',
      chat_acp_project_mode: 'project',
    } })
    await expect(command).resolves.toMatchObject({ ok: true })

    expect(store.draftViewRequested).toBeNull()
    expect(store.pendingACPStateFor(target)).toMatchObject({
      metadata: { acp_agent_id: 'claude' },
      runtimeId: 'rt_warm',
    })
    expect(api.closeACPRuntime).not.toHaveBeenCalledWith('bot-1', 'rt_warm')
  })
  it('applies the rich active-run contract script to the current assistant turn', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' }],
      nextCursor: null,
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    sendEvents = richActiveRunStoreScript()
    const sendPromise = store.sendMessage('please inspect')
    await flushPromises()
    await flushPromises()

    expect(sentWSMessages[0]).toMatchObject({
      type: 'message',
      text: 'please inspect',
      session_id: 'session-1',
    })

    const assistant = store.messages.find(turn => turn.role === 'assistant')
    expect(assistant?.role).toBe('assistant')
    if (assistant?.role !== 'assistant') throw new Error('missing assistant turn')

    expect(assistant.messages.find(block => block.type === 'reasoning')).toMatchObject({
      content: 'I need to inspect the workspace.',
    })
    expect(assistant.messages.find(block => block.type === 'text')).toMatchObject({
      content: 'I will check the current state.',
    })

    const execTool = assistant.messages.find(block => block.type === 'tool' && block.toolCallId === 'call-exec')
    expect(execTool).toMatchObject({
      type: 'tool',
      toolName: 'exec',
      done: true,
      running: false,
      progress: ['queued', { stdout: '/workspace\n' }],
    })

    const approvalTool = assistant.messages.find(block => block.type === 'tool' && block.toolCallId === 'call-approval')
    expect(approvalTool).toMatchObject({
      approval: {
        approval_id: 'approval-1',
        status: 'pending',
        can_approve: true,
      },
    })

    const askUserTool = assistant.messages.find(block => block.type === 'tool' && block.toolCallId === 'call-ask')
    expect(askUserTool).toMatchObject({
      userInput: {
        user_input_id: 'input-1',
        status: 'pending',
        can_respond: true,
        questions: [expect.objectContaining({ text: 'Continue?' })],
      },
    })

    streamHandler?.({ type: 'end', run_id: lastRunId, session_id: lastSessionId } as UIStreamEvent)
    await sendPromise
  })

  it('records interrupted runtime streams as stream-stage failures after visible output', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [{ id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' }],
      nextCursor: null,
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    sendEvents = interruptedRunStoreScript()
    const result = await store.sendMessage('please run')

    expect(result).toMatchObject({
      ok: false,
      stage: 'stream',
      error: 'runtime interrupted',
    })
    const assistant = store.messages.find(turn => turn.role === 'assistant')
    expect(assistant?.role).toBe('assistant')
    if (assistant?.role !== 'assistant') throw new Error('missing assistant turn')
    expect(assistant.messages.some(block => block.type === 'text' && block.content === 'partial output')).toBe(true)
    expect(assistant.messages.some(block => block.type === 'error' && block.content === 'runtime interrupted')).toBe(true)
  })

  it('does not let stale active-run events for another session pollute the visible transcript', async () => {
    api.fetchSessions.mockResolvedValueOnce({
      items: [
        { id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' },
        { id: 'session-2', bot_id: 'bot-1', title: 'B', type: 'chat' },
      ],
      nextCursor: null,
    })
    const store = useChatStore()
    await store.selectBot('bot-1')
    await flushPromises()

    api.fetchMessagesUI.mockResolvedValueOnce([])
    store.selectSession('session-2')
    await flushPromises()
    expect(store.sessionId).toBe('session-2')
    expect(store.messages).toEqual([])

    streamHandler?.({ type: 'start', run_id: 'run-old', session_id: 'session-1' } as UIStreamEvent)
    streamHandler?.({
      type: 'message',
      run_id: 'run-old',
      session_id: 'session-1',
      data: { id: 0, type: 'text', content: 'old session output' },
    } as UIStreamEvent)

    expect(store.messages).toEqual([])

    streamHandler?.({ type: 'end', run_id: 'run-old', session_id: 'session-1' } as UIStreamEvent)
  })

})
