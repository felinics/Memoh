import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, disposePinia, setActivePinia, type Pinia } from 'pinia'
import type {
  BotSessionActivityEvent,
  RuntimeCurrentRunView,
  RuntimeDelta,
  RuntimeRunStatus,
  UIMessage,
  UIStreamEvent,
  UIStreamEventHandler,
  UITurn,
  UIUserTurn,
} from '@/composables/api/useChat'
import { REASONING_EFFORT_DISABLE } from '@/pages/bots/components/reasoning-effort'
import { AUTH_SESSION_CLEARED_EVENT } from '@/lib/auth-session'
import type { BotWorkdir } from '@/composables/api/useWorkdirs'
import { useChatSelectionStore } from './chat-selection'
import { useChatStore } from './chat-list'
import { useWorkdirsStore } from './workdirs'

const api = vi.hoisted(() => ({
  createSession: vi.fn(),
  deleteSession: vi.fn(),
  forkSessionFromTurn: vi.fn(),
  fetchSession: vi.fn(),
  fetchSessions: vi.fn(),
  fetchBots: vi.fn(),
  fetchMessagesUI: vi.fn(),
  executeQuickAction: vi.fn(),
  fetchSafeSkillCatalog: vi.fn(),
  updateSessionAgent: vi.fn(),
  ensureACPRuntime: vi.fn(),
  createACPRuntime: vi.fn(),
  fetchACPRuntimeByID: vi.fn(),
  setACPRuntimeMode: vi.fn(),
  setACPRuntimeModel: vi.fn(),
  setACPRuntimeModelByID: vi.fn(),
  setACPRuntimeReasoning: vi.fn(),
  setACPRuntimeReasoningByID: vi.fn(),
  closeACPRuntime: vi.fn(),
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

const workdirsApi = vi.hoisted(() => ({
  fetchWorkdirs: vi.fn(),
}))

vi.hoisted(() => {
  for (const name of ['localStorage', 'sessionStorage']) {
    Object.defineProperty(globalThis, name, {
      configurable: true,
      value: {
        getItem: () => null,
        setItem: () => {},
        removeItem: () => {},
        clear: () => {},
      },
    })
  }
})

vi.mock('@/composables/api/useChat', () => api)
vi.mock('@/composables/api/useWorkdirs', async (importOriginal) => {
  const original = await importOriginal<typeof import('@/composables/api/useWorkdirs')>()
  return { ...original, fetchWorkdirs: workdirsApi.fetchWorkdirs }
})
vi.mock('@memohai/sdk', () => ({ getBotsByBotIdSettings: sdk.getBotsByBotIdSettings }))
vi.mock('vue-sonner', () => ({ toast }))
vi.mock('@felinic/ui', async (importOriginal) => {
  const original = await importOriginal<typeof import('@felinic/ui')>()
  return { ...original, toast }
})

function flushPromises() {
  return new Promise(resolve => setTimeout(resolve, 0))
}

type RuntimeTestUpdate =
  | { kind: 'run', status: RuntimeRunStatus, error?: string }
  | { kind: 'message', message: UIMessage }
  | { kind: 'user_turn', turn: UIUserTurn }

const runtime = {
  started: { kind: 'run', status: 'running' } as RuntimeTestUpdate,
  completed: { kind: 'run', status: 'completed' } as RuntimeTestUpdate,
  failed: (error: string): RuntimeTestUpdate => ({ kind: 'run', status: 'errored', error }),
  message: (message: UIMessage): RuntimeTestUpdate => ({ kind: 'message', message }),
  userTurn: (turn: UIUserTurn): RuntimeTestUpdate => ({ kind: 'user_turn', turn }),
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

const h = {
  streamHandler: null as UIStreamEventHandler | null,
  sessionsActivityHandler: null as ((event: BotSessionActivityEvent) => void) | null,
  sendUpdates: [] as RuntimeTestUpdate[],
  sentWSMessages: [] as Array<Record<string, unknown>>,
  runtimeUnsubscribes: [] as string[],
  wsRunIds: [] as string[],
  abortedWSRuns: [] as string[],
  lastRunId: '',
  lastSessionId: '',
  wsRunSeq: 0,
  runtimeBySession: new Map<string, {
    epoch: string
    seq: number
    run: RuntimeCurrentRunView | null
    publishedRunId: string
  }>(),
}

let pinia: Pinia

function testRuntime(sessionId: string) {
  let runtime = h.runtimeBySession.get(sessionId)
  if (!runtime) {
    runtime = { epoch: `epoch-${sessionId}`, seq: 0, run: null, publishedRunId: '' }
    h.runtimeBySession.set(sessionId, runtime)
  }
  return runtime
}

function emitRuntimeSnapshot(onEvent: UIStreamEventHandler, sessionId: string) {
  const runtime = testRuntime(sessionId)
  onEvent({
    type: 'runtime_snapshot',
    session_id: sessionId,
    epoch: runtime.epoch,
    seq: runtime.seq,
    snapshot: {
      bot_id: 'bot-1',
      session_id: sessionId,
      epoch: runtime.epoch,
      seq: runtime.seq,
      current_run_view: runtime.run ?? undefined,
      updated_at: new Date().toISOString(),
    },
  })
}

function publishRuntimeUpdate(
  onEvent: UIStreamEventHandler,
  sessionId: string,
  runId: string,
  update: RuntimeTestUpdate,
) {
  if (!sessionId || !runId) return
  const runtime = testRuntime(sessionId)
  const now = new Date().toISOString()
  const knownRun = runtime.run?.run_id === runId
  const publishedRun = runtime.publishedRunId === runId
  if (!knownRun) {
    runtime.run = {
      run_id: runId,
      turn_id: `turn-${runId}`,
      generation: `generation-${runId}`,
      status: 'running',
      started_at: now,
      updated_at: now,
      messages: [],
    }
  }
  const run = runtime.run!
  let delta: RuntimeDelta
  if (update.kind === 'run') {
    run.status = update.status
    run.error = update.error
    run.updated_at = now
    delta = publishedRun
      ? {
          run: {
            run_id: runId,
            status: update.status,
            error: update.error,
            updated_at: now,
          },
        }
      : { current_run_view: run }
  }
  else if (update.kind === 'message') {
    const messages = run.messages
    const incoming = { ...update.message }
    const index = messages.findIndex(message => message.id === incoming.id)
    if (index < 0) messages.push(incoming)
    else messages[index] = incoming
    run.updated_at = now
    delta = publishedRun
      ? { message_upserts: [incoming] }
      : { current_run_view: run }
  }
  else {
    run.request_user_turn = update.turn
    run.updated_at = now
    delta = { current_run_view: run }
  }
  runtime.seq += 1
  runtime.publishedRunId = runId
  onEvent({
    type: 'runtime_delta',
    session_id: sessionId,
    epoch: runtime.epoch,
    seq: runtime.seq,
    delta,
  })
}

function emitRuntime(
  update: RuntimeTestUpdate,
  sessionId = h.lastSessionId,
  runId = h.lastRunId,
) {
  if (h.streamHandler) publishRuntimeUpdate(h.streamHandler, sessionId, runId, update)
}

function emitRuntimeTo(
  handler: UIStreamEventHandler | undefined,
  update: RuntimeTestUpdate,
  sessionId: string,
  runId: string,
) {
  if (handler) publishRuntimeUpdate(handler, sessionId, runId, update)
}

// The invocation the client minted for an outbound message, and the run the
// fake server named in reply. Negative indexes count from the newest send.
function wsInvocationId(index = 0): string {
  const message = index < 0 ? h.sentWSMessages.at(index) : h.sentWSMessages[index]
  return (message?.invocation_id as string | undefined) ?? ''
}

function wsRunId(index = 0): string {
  return (index < 0 ? h.wsRunIds.at(index) : h.wsRunIds[index]) ?? ''
}

beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    // The runtime client coalesces delta projections to one per animation
    // frame; run frames synchronously so existing per-event assertions hold.
    vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
      callback(0)
      return 0
    })
    h.streamHandler = null
    h.sessionsActivityHandler = null
    h.lastRunId = ''
    h.lastSessionId = ''
    h.wsRunSeq = 0
    h.runtimeBySession = new Map()
    h.sentWSMessages = []
    h.runtimeUnsubscribes = []
    h.wsRunIds = []
    h.abortedWSRuns = []
    h.sendUpdates = [runtime.started, runtime.failed('model failed')]
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
    workdirsApi.fetchWorkdirs.mockResolvedValue([])
    sdk.getBotsByBotIdSettings.mockResolvedValue({ data: { chat_runtime: 'model' } })
    api.streamBotSessionsActivityEvents.mockImplementation((_botId: string, signal: AbortSignal, onEvent: (event: BotSessionActivityEvent) => void) => new Promise<void>((resolve) => {
      h.sessionsActivityHandler = onEvent
      signal.addEventListener('abort', () => resolve(), { once: true })
    }))
    api.connectWebSocket.mockImplementation((_botId: string, onStreamEvent: UIStreamEventHandler) => {
      h.streamHandler = onStreamEvent
      return {
        get connected() {
          return true
        },
        send: vi.fn((message: {
          type?: string
          invocation_id?: string
          session_id?: string
          message_id?: string
          text?: string
        }) => {
          if (message.type === 'runtime_subscribe') {
            if (message.session_id) emitRuntimeSnapshot(onStreamEvent, message.session_id)
            return
          }
          if (message.type === 'runtime_unsubscribe') {
            if (message.session_id) h.runtimeUnsubscribes.push(message.session_id)
            return
          }
          h.sentWSMessages.push(message as Record<string, unknown>)
          h.lastSessionId = message.session_id ?? ''
          if (
            message.type === 'tool_approval_response'
            || message.type === 'user_input_response'
          ) {
            const control = message as typeof message & {
              control_id?: string
              decision_id?: string
              run_id?: string
              decision?: 'approve' | 'reject'
              canceled?: boolean
            }
            const runtime = testRuntime(h.lastSessionId)
            onStreamEvent({
              type: 'control_ack',
              session_id: h.lastSessionId,
              run_id: control.run_id ?? runtime.run?.run_id ?? '',
              control: message.type,
              control_id: control.control_id ?? '',
              applied: true,
            })
            if (runtime.run) {
              const decisionId = control.decision_id ?? ''
              const updated = runtime.run.messages.flatMap((candidate): UIMessage[] => {
                if (candidate.type !== 'tool') return []
                if (
                  message.type === 'tool_approval_response'
                  && candidate.approval?.approval_id === decisionId
                ) {
                  return [{
                    ...candidate,
                    approval: {
                      ...candidate.approval,
                      status: control.decision === 'reject' ? 'rejected' : 'approved',
                      can_approve: false,
                    },
                  }]
                }
                if (
                  message.type === 'user_input_response'
                  && candidate.user_input?.user_input_id === decisionId
                ) {
                  return [{
                    ...candidate,
                    user_input: {
                      ...candidate.user_input,
                      status: control.canceled ? 'canceled' : 'submitted',
                      can_respond: false,
                    },
                  }]
                }
                return []
              })
              for (const candidate of updated) {
                const index = runtime.run.messages.findIndex(item => item.id === candidate.id)
                if (index >= 0) runtime.run.messages[index] = candidate
              }
              if (updated.length) {
                runtime.seq += 1
                onStreamEvent({
                  type: 'runtime_delta',
                  session_id: h.lastSessionId,
                  epoch: runtime.epoch,
                  seq: runtime.seq,
                  delta: { message_upserts: updated },
                })
              }
            }
            return
          }
          // The server names the run and announces it before any turn output,
          // so every later event is addressed by run_id.
          const invocationId = message.invocation_id ?? ''
          h.lastRunId = invocationId ? `run-${++h.wsRunSeq}` : ''
          h.wsRunIds.push(h.lastRunId)
          if (h.lastRunId) {
            const runtime = testRuntime(h.lastSessionId)
            const now = new Date().toISOString()
            runtime.run = {
              run_id: h.lastRunId,
              turn_id: `turn-${h.lastRunId}`,
              invocation_id: invocationId,
              generation: `generation-${h.lastRunId}`,
              status: 'admitting',
              started_at: now,
              updated_at: now,
              messages: [],
              request_user_turn: message.type === 'message'
                ? {
                    turn_id: `turn-${h.lastRunId}`,
                    role: 'user',
                    text: message.text ?? '',
                    timestamp: now,
                  }
                : undefined,
              operation: message.type === 'retry_message'
                ? {
                    kind: 'retry',
                    replace_from_message_id: message.message_id ?? '',
                  }
                : message.type === 'edit_message'
                  ? {
                      kind: 'edit',
                      replace_from_message_id: message.message_id ?? '',
                      replacement_user_turn: {
                        turn_id: `turn-${h.lastRunId}`,
                        role: 'user',
                        text: message.text ?? '',
                        timestamp: now,
                      },
                    }
                  : undefined,
            }
            onStreamEvent({
              type: 'run_accepted',
              run_id: h.lastRunId,
              invocation_id: invocationId,
              session_id: h.lastSessionId,
              turn_id: runtime.run.turn_id,
              epoch: runtime.epoch,
              seq: runtime.seq + 1,
            })
          }
          for (const update of h.sendUpdates) {
            if (!h.lastSessionId && update.kind === 'run' && update.status === 'errored') {
              onStreamEvent({
                type: 'error',
                run_id: h.lastRunId,
                invocation_id: invocationId,
                message: update.error ?? 'model failed',
              })
              continue
            }
            publishRuntimeUpdate(onStreamEvent, h.lastSessionId, h.lastRunId, update)
          }
        }),
        abort: vi.fn((runId: string) => {
          h.abortedWSRuns.push(runId)
        }),
        close: vi.fn(),
        onOpen: null,
        onClose: null,
      }
    })
})

afterEach(() => {
  disposePinia(pinia)
  vi.unstubAllGlobals()
})

function richActiveRunStoreScript(): RuntimeTestUpdate[] {
  return [
    runtime.started,
    runtime.message({ id: 0, type: 'reasoning', content: 'I need to inspect the workspace.' }),
    runtime.message({ id: 1, type: 'text', content: 'I will check the current state.' }),
    runtime.message({
        id: 2,
        type: 'tool',
        name: 'exec',
        tool_call_id: 'call-exec',
        input: { command: 'pwd' },
        running: true,
        progress: ['queued'],
    }),
    runtime.message({
        id: 2,
        type: 'tool',
        name: 'exec',
        tool_call_id: 'call-exec',
        input: { command: 'pwd' },
        output: { structuredContent: { stdout: '/workspace\n' } },
        running: false,
        progress: ['queued', { stdout: '/workspace\n' }],
    }),
    runtime.message({
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
    }),
    runtime.message({
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
    }),
  ]
}

function interruptedRunStoreScript(): RuntimeTestUpdate[] {
  return [
    runtime.started,
    runtime.message({ id: 0, type: 'text', content: 'partial output' }),
    runtime.failed('runtime interrupted'),
  ]
}

describe('chat-list store', () => {

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
      h.sendUpdates = [
        runtime.started,
        runtime.message({
            id: 1,
            type: 'tool',
            name: 'browser_action',
            input: { action: 'click' },
            tool_call_id: 'call-browser',
            running: true,
        }),
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

      emitRuntime(runtime.message({
          id: 1,
          type: 'tool',
          name: 'browser_action',
          input: { action: 'click', coordinate: [10, 20] },
          tool_call_id: 'call-browser',
          running: true,
      }), 'session-1', h.lastRunId)
      expect(store.guiToolUseRequested?.seq).toBe(1)

      emitRuntime(runtime.message({
          id: 2,
          type: 'tool',
          name: 'computer_observe',
          input: { observe: 'snapshot' },
          tool_call_id: 'call-computer',
          running: true,
      }), 'session-1', h.lastRunId)
      expect(store.guiToolUseRequested).toMatchObject({
        toolCallId: 'call-computer',
        toolName: 'computer_observe',
        seq: 2,
      })

      emitRuntime(runtime.completed, 'session-1', h.lastRunId)
      await expect(sending).resolves.toMatchObject({ ok: true })
    })

  it('projects startup failures identically while returning them to the composer', async () => {
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
      expect(store.messages.map(turn => turn.role)).toEqual(['user', 'assistant'])
      expect(store.messages[1]).toMatchObject({
        role: 'assistant',
        streaming: false,
        messages: [{ type: 'error', content: 'model failed' }],
      })
      expect(store.startupSendFailure).toMatchObject({
        botId: 'bot-1',
        sessionId: 'session-1',
        error: 'model failed',
        restoreInput: 'hello',
      })
      expect(onBeforeTurnAppend).toHaveBeenCalledOnce()
      expect(onTurnAppendAborted).toHaveBeenCalledOnce()
      expect(h.sentWSMessages.at(-1)).toMatchObject({
        type: 'message',
        workspace_target_id: 'computer-b',
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

  it.each(['/new codex', '/new chat codex'])(
    'handles %s as a fresh ACP chat composer',
    async (command) => {
      const store = useChatStore()

      await store.selectBot('bot-1')
      const result = await store.sendMessage(command)
      applyLatestDraftRequest(store)

      expect(result.ok).toBe(true)
      expect(api.createSession).not.toHaveBeenCalled()
      expect(h.sentWSMessages).toHaveLength(0)
      expect(store.sessionId).toBeNull()
      expect(store.pendingACPSessionMetadata).toEqual({
        acp_agent_id: 'codex',
        project_path: '/data',
        acp_project_mode: 'project',
      })
      expect(store.activeChatTarget).toMatchObject({
        kind: 'draft-acp',
        runtimeType: 'acp_agent',
        isACP: true,
        isPendingACP: true,
      })
    },
  )

  it('handles /new codex from an existing session as a fresh ACP composer', async () => {
      h.sendUpdates = [runtime.completed]
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
      expect(h.sentWSMessages.at(-1)).toMatchObject({
        session_id: 'acp-session-1',
        text: 'hello codex',
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

  it('does not match a staged default when only the BotAgent row changes', async () => {
      const store = useChatStore()

      await store.selectBot('bot-1')
      store.stageDefaultACPSession({
        botAgentId: 'agent-1',
        agentId: 'codex',
        projectPath: '/data',
        projectMode: 'project',
      })

      expect(store.pendingACPMatchesInput({
        botAgentId: 'agent-2',
        agentId: 'codex',
        projectPath: '/data',
        projectMode: 'project',
      })).toBe(false)
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
      expect(h.sentWSMessages).toHaveLength(0)
      expect(store.sessionId).toBeNull()
      expect(store.pendingACPSessionMetadata?.acp_agent_id).toBe('codex')

      h.sendUpdates = [runtime.completed]
      const sendResult = await store.sendMessage('start discuss')

      expect(sendResult.ok).toBe(true)
      expect(api.createSession).toHaveBeenCalledWith('bot-1', expect.objectContaining({
        type: 'discuss',
        sessionMode: 'discuss',
        runtimeType: 'acp_agent',
      }))
    })

  it('merges ACP approval tool messages into the existing tool block by call id', async () => {
      h.sendUpdates = [
        runtime.started,
        runtime.message({
            id: 1,
            type: 'tool',
            name: 'exec',
            input: { command: 'make test' },
            tool_call_id: 'mcp-http-call-1',
            running: true,
        }),
        runtime.message({
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
        }),
        runtime.failed('stop after visible output'),
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

  it.each([
    ['uses an explicit project without a placeholder title', '/data/app', true],
    ['defaults to the workspace root without a placeholder title', '/data', false],
  ])('%s', async (_name, projectPath, explicitProject) => {
      api.createSession.mockResolvedValueOnce({
        id: 'acp-session-1',
        bot_id: 'bot-1',
        title: '',
        type: 'acp_agent',
        metadata: {
          acp_agent_id: 'codex',
          project_path: projectPath,
          acp_project_mode: 'project',
        },
      })
      const store = useChatStore()

      await store.selectBot('bot-1')
      await store.createACPSession({
        agentId: 'codex',
        ...(explicitProject ? { projectPath, projectMode: 'project' as const } : {}),
      })

      expect(api.createSession).toHaveBeenLastCalledWith('bot-1', expect.objectContaining({
        title: '',
        type: 'chat',
        sessionMode: 'chat',
        runtimeType: 'acp_agent',
        metadata: {},
        runtimeMetadata: {
          acp_agent_id: 'codex',
          project_path: projectPath,
          acp_project_mode: 'project',
        },
      }))
    })

  it('defers ACP session creation until the first message is sent', async () => {
      h.sendUpdates = [runtime.completed]
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
      expect(h.sentWSMessages[0]).toMatchObject({
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

  it('unsubscribes the live session runtime when resetToEmptyComposer clears a selected session', async () => {
      api.fetchSessions.mockResolvedValueOnce({
        items: [{
          id: 'session-live',
          bot_id: 'bot-1',
          title: 'Live',
          type: 'chat',
        }],
        nextCursor: null,
      })
      const store = useChatStore()

      await store.selectBot('bot-1')
      await store.selectSession('session-live')
      await flushPromises()
      expect(store.sessionId).toBe('session-live')

      h.runtimeUnsubscribes = []
      store.resetToEmptyComposer({
        clearPendingACP: false,
        explicitSelection: false,
        draftIntent: false,
      })

      expect(store.sessionId).toBeNull()
      expect(h.runtimeUnsubscribes).toEqual(['session-live'])
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
      h.sendUpdates = [runtime.completed]
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
      expect(h.sentWSMessages[0]).toMatchObject({
        session_id: 'acp-session-1',
        reasoning_effort: 'high',
        text: 'hello codex',
        model_id: 'gpt-5.1-codex-high',
      })
    })

  it.each([
    {
      targetKind: 'native' as const,
      workdirId: 'native-workdir',
      name: 'Native project',
      path: '/data/project',
      workspaceTargetId: 'native',
    },
    {
      targetKind: 'remote' as const,
      workdirId: 'remote-workdir',
      name: 'Laptop project',
      path: '/Users/example/project',
      workspaceTargetId: 'runtime-1',
    },
  ])('cold-starts a staged ACP agent after binding a $targetKind workdir', async ({
    targetKind,
    workdirId,
    name,
    path,
    workspaceTargetId,
  }) => {
      h.sendUpdates = [runtime.completed]
      const selectedWorkdir: BotWorkdir = {
        id: workdirId,
        bot_id: 'bot-1',
        name,
        path,
        target_kind: targetKind,
        workspace_target_id: workspaceTargetId,
      }
      const workdirsLoad = deferred<BotWorkdir[]>()
      workdirsApi.fetchWorkdirs.mockReturnValueOnce(workdirsLoad.promise)
      api.createSession.mockResolvedValueOnce({
        id: 'bound-acp-session',
        bot_id: 'bot-1',
        title: '',
        type: 'acp_agent',
        workdir_id: workdirId,
        metadata: {
          acp_agent_id: 'codex',
          project_path: path,
          acp_project_mode: 'project',
        },
      })
      const store = useChatStore()

      await store.selectBot('bot-1')
      const workdirs = useWorkdirsStore()
      // This mirrors a Folder ID restored from localStorage before the list
      // request has completed.
      workdirs.setWorkingWorkdir('bot-1', workdirId)
      store.stageACPSession({ agentId: 'codex' })

      await expect(store.ensurePendingACPRuntime()).resolves.toBeUndefined()
      expect(api.createACPRuntime).not.toHaveBeenCalled()
      expect(store.pendingACPRuntimeId).toBe('')

      const sending = store.sendMessage('run in the selected folder')
      await flushPromises()

      expect(workdirsApi.fetchWorkdirs).toHaveBeenCalledWith('bot-1')
      expect(api.createSession).not.toHaveBeenCalled()

      workdirsLoad.resolve([selectedWorkdir])
      const result = await sending

      expect(result.ok).toBe(true)
      const createInput = api.createSession.mock.calls.at(-1)?.[1]
      expect(createInput).toMatchObject({
        type: 'chat',
        sessionMode: 'chat',
        runtimeType: 'acp_agent',
        workdirId,
      })
      expect(createInput?.acpRuntimeId).toBeUndefined()
      expect(api.createACPRuntime).not.toHaveBeenCalled()
      expect(api.ensureACPRuntime).not.toHaveBeenCalled()
      expect(h.sentWSMessages[0]).toMatchObject({
        session_id: 'bound-acp-session',
        text: 'run in the selected folder',
      })
    })

  it('discards a Primary warm runtime before a native-folder-bound first send', async () => {
      h.sendUpdates = [runtime.completed]
      const store = useChatStore()

      await store.selectBot('bot-1')
      store.stageACPSession({ agentId: 'codex' })
      await store.ensurePendingACPRuntime()
      expect(store.pendingACPRuntimeId).toBe('rt_warm')

      const workdirs = useWorkdirsStore()
      workdirsApi.fetchWorkdirs.mockResolvedValueOnce([{
        id: 'native-workdir',
        bot_id: 'bot-1',
        name: 'Native project',
        path: '/data/project',
        target_kind: 'native',
        workspace_target_id: 'native',
      }])
      workdirs.setWorkingWorkdir('bot-1', 'native-workdir')

      const result = await store.sendMessage('use the selected folder')

      expect(result.ok).toBe(true)
      expect(api.closeACPRuntime).toHaveBeenCalledWith('bot-1', 'rt_warm')
      expect(api.createACPRuntime).toHaveBeenCalledTimes(1)
      const createInput = api.createSession.mock.calls.at(-1)?.[1]
      expect(createInput?.workdirId).toBe('native-workdir')
      expect(createInput?.acpRuntimeId).toBeUndefined()
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

  it('stamps session updated_at from the server message time, not the client clock or a reorder', async () => {
      api.fetchSessions.mockResolvedValueOnce({ items: [
        { id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat', updated_at: '2026-01-01T00:00:00Z' },
        { id: 'session-2', bot_id: 'bot-1', title: 'B', type: 'chat', updated_at: '2026-01-02T00:00:00Z' },
      ], nextCursor: null })
      const store = useChatStore()
      await store.selectBot('bot-1')
      await flushPromises()

      h.sessionsActivityHandler?.({
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

      h.sessionsActivityHandler?.({
        type: 'session_title_changed',
        session_id: 'session-subagent',
        title: 'Updated subagent title',
      })
      await flushPromises()

      expect(store.knownSessionSummary('session-subagent')?.title).toBe('Updated subagent title')
      expect(store.knownSessions.find(session => session.id === 'session-subagent')?.title).toBe('Updated subagent title')
    })

  it('does not let an older REST snapshot overwrite a newer SSE title', async () => {
      api.fetchSessions.mockResolvedValueOnce({ items: [
        { id: 'session-1', bot_id: 'bot-1', title: 'Original', type: 'chat' },
      ], nextCursor: null })
      const staleRefresh = deferred<{
        items: Array<{ id: string, bot_id: string, title: string, type: string }>
        nextCursor: null
      }>()
      const store = useChatStore()
      await store.selectBot('bot-1')
      api.fetchSessions
        .mockReturnValueOnce(staleRefresh.promise)
        .mockResolvedValueOnce({ items: [
          { id: 'session-1', bot_id: 'bot-1', title: 'New title', type: 'chat' },
        ], nextCursor: null })

      h.sessionsActivityHandler?.({
        type: 'session_created',
        session_id: 'unknown-session',
        session_type: 'chat',
      })
      await flushPromises()
      h.sessionsActivityHandler?.({
        type: 'session_title_changed',
        session_id: 'session-1',
        title: 'New title',
      })
      staleRefresh.resolve({ items: [
        { id: 'session-1', bot_id: 'bot-1', title: 'Original', type: 'chat' },
      ], nextCursor: null })
      await flushPromises()
      await flushPromises()

      expect(api.fetchSessions).toHaveBeenCalledTimes(3)
      expect(store.sessions[0]?.title).toBe('New title')
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

      h.sessionsActivityHandler?.({
        type: 'session_title_changed',
        session_id: 'session-subagent',
        title: 'Updated subagent title',
      })
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
      h.sessionsActivityHandler?.({
        type: 'session_touched',
        session_id: 'session-hidden',
        updated_at: '2026-06-23T10:00:00.000Z',
      })
      await flushPromises()

      expect(api.fetchSessions).toHaveBeenCalledTimes(2)
      expect(store.sessions.map(session => session.id)).toEqual(['session-hidden', 'session-visible'])
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

      h.sessionsActivityHandler?.({
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
      h.sendUpdates = [
        runtime.started,
        runtime.message({ id: 0, type: 'text', content: 'partial response' }),
        runtime.failed('model failed'),
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

  it('replaces the latest assistant immediately when retry starts', async () => {
      h.sendUpdates = [runtime.started]
      api.fetchSessions.mockResolvedValueOnce({
        items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
        nextCursor: null,
      })
      api.fetchMessagesUI.mockResolvedValueOnce([
        {
          id: 'user-1',
          turn_id: 'turn-fx-0-1',
          role: 'user',
          text: 'hello',
          attachments: [],
          timestamp: '2026-05-17T08:00:00.000Z',
        },
        {
          id: 'assistant-old',
          turn_id: 'turn-fx-0-1',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'old answer' }],
          timestamp: '2026-05-17T08:00:01.000Z',
          streaming: false,
        },
      ])
      const store = useChatStore()

      await store.selectBot('bot-1')
      await flushPromises()
      const retry = store.retryLatestAssistant('turn-fx-0-1', { workspaceTargetId: 'computer-b' })
      await flushPromises()

      expect(h.sentWSMessages.at(-1)).toMatchObject({
        type: 'retry_message',
        session_id: 'session-1',
        turn_id: 'turn-fx-0-1',
        workspace_target_id: 'computer-b',
      })
      expect(store.messages.map(message => message.id)).not.toContain('assistant-old')
      expect(store.messages.map(message => message.role)).toEqual(['user', 'assistant'])
      expect(store.messages[1]).toMatchObject({
        role: 'assistant',
        streaming: true,
        __optimistic: false,
      })

      api.fetchMessagesUI.mockResolvedValueOnce([
        {
          id: 'user-1',
          turn_id: 'turn-fx-0-2',
          role: 'user',
          text: 'hello',
          attachments: [],
          timestamp: '2026-05-17T08:00:00.000Z',
        },
        {
          id: 'assistant-new',
          turn_id: 'turn-fx-0-2',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'new answer' }],
          timestamp: '2026-05-17T08:00:02.000Z',
          streaming: false,
        },
      ])
      emitRuntime(runtime.completed, h.lastSessionId, h.lastRunId)
      await retry
      await flushPromises()

      expect(store.messages.map(message => message.id)).toEqual(['user-1', 'assistant-new'])
    })

  it('moves fork divider anchor to the previous inherited assistant when retry replaces the fork anchor', async () => {
      h.sendUpdates = [runtime.started]
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
          turn_id: 'turn-fx-1-1',
          role: 'user',
          text: 'first',
          attachments: [],
          timestamp: '2026-05-17T08:00:00.000Z',
        },
        {
          id: 'assistant-prev',
          turn_id: 'turn-fx-1-1',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'previous answer' }],
          timestamp: '2026-05-17T08:00:01.000Z',
          streaming: false,
        },
        {
          id: 'user-2',
          turn_id: 'turn-fx-1-2',
          role: 'user',
          text: 'second',
          attachments: [],
          timestamp: '2026-05-17T08:00:02.000Z',
        },
        {
          id: 'assistant-old',
          turn_id: 'turn-fx-1-2',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'old answer' }],
          timestamp: '2026-05-17T08:00:03.000Z',
          streaming: false,
        },
      ])
      const store = useChatStore()

      await store.selectBot('bot-1')
      await flushPromises()
      const retry = store.retryLatestAssistant('turn-fx-1-2')
      await flushPromises()

      expect(store.activeChatTarget.metadata.forked_from).toMatchObject({
        fork_message_id: 'assistant-prev',
      })

      api.fetchMessagesUI.mockResolvedValueOnce([
        {
          id: 'user-1',
          turn_id: 'turn-fx-1-3',
          role: 'user',
          text: 'first',
          attachments: [],
          timestamp: '2026-05-17T08:00:00.000Z',
        },
        {
          id: 'assistant-prev',
          turn_id: 'turn-fx-1-3',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'previous answer' }],
          timestamp: '2026-05-17T08:00:01.000Z',
          streaming: false,
        },
        {
          id: 'user-2',
          turn_id: 'turn-fx-1-4',
          role: 'user',
          text: 'second',
          attachments: [],
          timestamp: '2026-05-17T08:00:02.000Z',
        },
        {
          id: 'assistant-new',
          turn_id: 'turn-fx-1-4',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'new answer' }],
          timestamp: '2026-05-17T08:00:06.000Z',
          streaming: false,
        },
      ])
      emitRuntime(runtime.completed, h.lastSessionId, h.lastRunId)
      await retry
      await flushPromises()

      expect(store.activeChatTarget.metadata.forked_from).toMatchObject({
        fork_message_id: 'assistant-prev',
      })
    })

  it('clears fork divider anchor when retry replaces the only inherited assistant', async () => {
      h.sendUpdates = [runtime.started]
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
          turn_id: 'turn-fx-2-1',
          role: 'user',
          text: 'hello',
          attachments: [],
          timestamp: '2026-05-17T08:00:00.000Z',
        },
        {
          id: 'assistant-old',
          turn_id: 'turn-fx-2-1',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'old answer' }],
          timestamp: '2026-05-17T08:00:01.000Z',
          streaming: false,
        },
      ])
      const store = useChatStore()

      await store.selectBot('bot-1')
      await flushPromises()
      const retry = store.retryLatestAssistant('turn-fx-2-1')
      await flushPromises()

      expect(store.activeChatTarget.metadata.forked_from).toMatchObject({
        session_id: 'source-session',
        message_id: 'source-assistant',
      })
      expect((store.activeChatTarget.metadata.forked_from as Record<string, unknown>).fork_message_id).toBeUndefined()

      api.fetchMessagesUI.mockResolvedValueOnce([
        {
          id: 'user-1',
          turn_id: 'turn-fx-2-2',
          role: 'user',
          text: 'hello',
          attachments: [],
          timestamp: '2026-05-17T08:00:00.000Z',
        },
        {
          id: 'assistant-new',
          turn_id: 'turn-fx-2-2',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'new answer' }],
          timestamp: '2026-05-17T08:00:06.000Z',
          streaming: false,
        },
      ])
      emitRuntime(runtime.completed, h.lastSessionId, h.lastRunId)
      await retry
      await flushPromises()

      expect((store.activeChatTarget.metadata.forked_from as Record<string, unknown>).fork_message_id).toBeUndefined()
    })

  it('moves fork divider anchor when edit replaces the fork anchor tail', async () => {
      h.sendUpdates = [runtime.started]
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
          turn_id: 'turn-fx-3-1',
          role: 'user',
          text: 'first',
          attachments: [],
          timestamp: '2026-05-17T08:00:00.000Z',
        },
        {
          id: 'assistant-prev',
          turn_id: 'turn-fx-3-1',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'previous answer' }],
          timestamp: '2026-05-17T08:00:01.000Z',
          streaming: false,
        },
        {
          id: 'user-2',
          turn_id: 'turn-fx-3-2',
          role: 'user',
          text: 'second',
          attachments: [],
          timestamp: '2026-05-17T08:00:02.000Z',
        },
        {
          id: 'assistant-old',
          turn_id: 'turn-fx-3-2',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'old answer' }],
          timestamp: '2026-05-17T08:00:03.000Z',
          streaming: false,
        },
      ])
      const store = useChatStore()

      await store.selectBot('bot-1')
      await flushPromises()
      const edit = store.editLatestUser('turn-fx-3-2', 'edited second')
      await flushPromises()

      expect(store.activeChatTarget.metadata.forked_from).toMatchObject({
        fork_message_id: 'assistant-prev',
      })

      api.fetchMessagesUI.mockResolvedValueOnce([
        {
          id: 'user-1',
          turn_id: 'turn-fx-3-3',
          role: 'user',
          text: 'first',
          attachments: [],
          timestamp: '2026-05-17T08:00:00.000Z',
        },
        {
          id: 'assistant-prev',
          turn_id: 'turn-fx-3-3',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'previous answer' }],
          timestamp: '2026-05-17T08:00:01.000Z',
          streaming: false,
        },
        {
          id: 'user-new',
          turn_id: 'turn-fx-3-4',
          role: 'user',
          text: 'edited second',
          attachments: [],
          timestamp: '2026-05-17T08:00:06.000Z',
        },
        {
          id: 'assistant-new',
          turn_id: 'turn-fx-3-4',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'new answer' }],
          timestamp: '2026-05-17T08:00:07.000Z',
          streaming: false,
        },
      ])
      emitRuntime(runtime.completed, h.lastSessionId, h.lastRunId)
      await edit
      await flushPromises()

      expect(store.activeChatTarget.metadata.forked_from).toMatchObject({
        fork_message_id: 'assistant-prev',
      })
    })

  it('restores the old assistant when retry fails before streaming starts', async () => {
      h.sendUpdates = [runtime.failed('model failed')]
      api.fetchSessions.mockResolvedValueOnce({
        items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
        nextCursor: null,
      })
      api.fetchMessagesUI.mockResolvedValueOnce([
        {
          id: 'user-1',
          turn_id: 'turn-fx-4-1',
          role: 'user',
          text: 'hello',
          attachments: [],
          timestamp: '2026-05-17T08:00:00.000Z',
        },
        {
          id: 'assistant-old',
          turn_id: 'turn-fx-4-1',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'old answer' }],
          timestamp: '2026-05-17T08:00:01.000Z',
          streaming: false,
        },
      ])
      const store = useChatStore()

      await store.selectBot('bot-1')
      await flushPromises()
      const result = await store.retryLatestAssistant('turn-fx-4-1')

      expect(result).toMatchObject({ ok: false, stage: 'startup', error: 'model failed' })
      expect(store.messages.map(message => message.id)).toEqual(['user-1', 'assistant-old'])
    })

  it('does not restore a failed retry tail into a different active session', async () => {
      h.sendUpdates = []
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
              turn_id: 'turn-fx-5-1',
              role: 'user',
              text: 'hello',
              attachments: [],
              timestamp: '2026-05-17T08:00:00.000Z',
            },
            {
              id: 'assistant-old',
              turn_id: 'turn-fx-5-1',
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
              turn_id: 'turn-fx-5-2',
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
      const retry = store.retryLatestAssistant('turn-fx-5-1')
      await flushPromises()
      expect(store.messages.map(message => message.id)).toEqual(['user-a', expect.any(String)])
      const retryRunId = h.lastRunId

      await store.selectSession('session-b')
      await flushPromises()
      expect(store.messages.map(message => message.id)).toEqual(['user-b'])

      h.streamHandler?.({
        type: 'error',
        run_id: retryRunId,
        session_id: 'session-a',
        message: 'model failed',
      })
      const result = await retry
      await flushPromises()

      expect(result).toMatchObject({ ok: false, stage: 'startup', error: 'model failed' })
      expect(store.sessionId).toBe('session-b')
      expect(store.messages.map(message => message.id)).toEqual(['user-b'])
    })

  it('replaces the latest user turn tail immediately when edit starts', async () => {
      h.sendUpdates = [runtime.started]
      api.fetchSessions.mockResolvedValueOnce({
        items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
        nextCursor: null,
      })
      api.fetchMessagesUI.mockResolvedValueOnce([
        {
          id: 'user-1',
          turn_id: 'turn-fx-6-1',
          role: 'user',
          text: 'old prompt',
          attachments: [],
          timestamp: '2026-05-17T08:00:00.000Z',
        },
        {
          id: 'assistant-old',
          turn_id: 'turn-fx-6-1',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'old answer' }],
          timestamp: '2026-05-17T08:00:01.000Z',
          streaming: false,
        },
      ])
      const store = useChatStore()

      await store.selectBot('bot-1')
      await flushPromises()
      const edit = store.editLatestUser('turn-fx-6-1', 'new prompt', { workspaceTargetId: 'computer-a' })
      await flushPromises()

      expect(h.sentWSMessages.at(-1)).toMatchObject({
        type: 'edit_message',
        session_id: 'session-1',
        turn_id: 'turn-fx-6-1',
        text: 'new prompt',
        workspace_target_id: 'computer-a',
      })
      expect(store.messages.map(message => message.id)).not.toContain('user-1')
      expect(store.messages.map(message => message.id)).not.toContain('assistant-old')
      expect(store.messages.map(message => message.role)).toEqual(['user', 'assistant'])
      expect(store.messages[0]).toMatchObject({
        role: 'user',
        text: 'new prompt',
        __optimistic: false,
      })
      expect(store.messages[1]).toMatchObject({
        role: 'assistant',
        streaming: true,
        __optimistic: false,
      })

      api.fetchMessagesUI.mockResolvedValueOnce([
        {
          id: 'user-new',
          turn_id: 'turn-fx-6-2',
          role: 'user',
          text: 'new prompt',
          attachments: [],
          timestamp: '2026-05-17T08:00:02.000Z',
        },
        {
          id: 'assistant-new',
          turn_id: 'turn-fx-6-2',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'new answer' }],
          timestamp: '2026-05-17T08:00:03.000Z',
          streaming: false,
        },
      ])
      emitRuntime(runtime.completed, h.lastSessionId, h.lastRunId)
      await edit
      await flushPromises()

      expect(store.messages.map(message => message.id)).toEqual(['user-new', 'assistant-new'])
    })

  it('restores the old latest turn tail when edit fails before streaming starts', async () => {
      h.sendUpdates = [runtime.failed('model failed')]
      api.fetchSessions.mockResolvedValueOnce({
        items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
        nextCursor: null,
      })
      api.fetchMessagesUI.mockResolvedValueOnce([
        {
          id: 'user-1',
          turn_id: 'turn-fx-7-1',
          role: 'user',
          text: 'old prompt',
          attachments: [],
          timestamp: '2026-05-17T08:00:00.000Z',
        },
        {
          id: 'assistant-old',
          turn_id: 'turn-fx-7-1',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'old answer' }],
          timestamp: '2026-05-17T08:00:01.000Z',
          streaming: false,
        },
      ])
      const store = useChatStore()

      await store.selectBot('bot-1')
      await flushPromises()
      const result = await store.editLatestUser('turn-fx-7-1', 'new prompt')

      expect(result).toMatchObject({
        ok: false,
        stage: 'startup',
        error: 'model failed',
        restoreInput: 'new prompt',
      })
      expect(store.messages.map(message => message.id)).toEqual(['user-1', 'assistant-old'])
    })

  it('does not restore a failed edit tail into a different active session', async () => {
      h.sendUpdates = []
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
              turn_id: 'turn-fx-8-1',
              role: 'user',
              text: 'old prompt',
              attachments: [],
              timestamp: '2026-05-17T08:00:00.000Z',
            },
            {
              id: 'assistant-a',
              turn_id: 'turn-fx-8-1',
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
              turn_id: 'turn-fx-8-2',
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
      const edit = store.editLatestUser('turn-fx-8-1', 'new prompt')
      await flushPromises()
      expect(store.messages.map(message => message.role)).toEqual(['user', 'assistant'])
      expect(store.messages[0]).toMatchObject({ role: 'user', text: 'new prompt' })
      const editRunId = h.lastRunId

      await store.selectSession('session-b')
      await flushPromises()
      expect(store.messages.map(message => message.id)).toEqual(['user-b'])

      h.streamHandler?.({
        type: 'error',
        run_id: editRunId,
        session_id: 'session-a',
        message: 'model failed',
      })
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
      h.sendUpdates = []
      api.fetchSessions.mockResolvedValueOnce({
        items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
        nextCursor: null,
      })
      api.fetchMessagesUI.mockResolvedValueOnce([
        {
          id: 'user-1',
          turn_id: 'turn-fx-8-3',
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
          turn_id: 'turn-fx-8-3',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'old answer' }],
          timestamp: '2026-05-17T08:00:01.000Z',
          streaming: false,
        },
      ])
      const store = useChatStore()

      await store.selectBot('bot-1')
      await flushPromises()
      const result = await store.editLatestUser('turn-fx-8-3', 'new prompt')

      expect(result).toMatchObject({ ok: false, stage: 'startup' })
      expect(h.sentWSMessages).toHaveLength(0)
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
          turn_id: 'turn-fx-9-1',
          role: 'user' as const,
          text: 'hello',
          attachments: [],
          timestamp: '2026-05-17T08:00:00.000Z',
        },
        {
          id: 'source-assistant',
          turn_id: 'turn-fx-9-1',
          role: 'assistant' as const,
          messages: [{ id: 1, type: 'text' as const, content: 'answer' }],
          timestamp: '2026-05-17T08:00:01.000Z',
          streaming: false,
        },
      ]
      const forkTurns = [
        {
          id: 'fork-user',
          turn_id: 'turn-fx-9-2',
          role: 'user' as const,
          text: 'hello',
          attachments: [],
          timestamp: '2026-05-17T08:00:00.000Z',
        },
        {
          id: 'fork-assistant',
          turn_id: 'turn-fx-9-2',
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
      api.forkSessionFromTurn.mockResolvedValueOnce({
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
              fork_message_id: 'fork-final-raw-message',
            },
          },
        }],
        nextCursor: null,
      })
      const store = useChatStore()

      await store.selectBot('bot-1')
      await flushPromises()
      const ok = await store.forkTurn('turn-fx-9-1', { title: 'Custom fork name' })
      await applyLatestForkRequest(store)
      await flushPromises()

      expect(ok).toBe(true)
      expect(api.forkSessionFromTurn).toHaveBeenCalledWith('bot-1', 'source-session', 'turn-fx-9-1', { title: 'Custom fork name' })
      expect(store.sessionId).toBe('fork-session')
      expect(store.messages.map(message => message.id)).toEqual(['fork-user', 'fork-assistant'])
      expect(store.activeChatTarget.metadata.forked_from).toMatchObject({
        session_id: 'source-session',
        message_id: 'source-assistant',
        fork_message_id: 'fork-final-raw-message',
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
          turn_id: 'turn-fx-10-1',
          role: 'user' as const,
          text: 'hello',
          attachments: [],
          timestamp: '2026-05-17T08:00:00.000Z',
        },
        {
          id: 'source-assistant',
          turn_id: 'turn-fx-10-1',
          role: 'assistant' as const,
          messages: [{ id: 1, type: 'text' as const, content: 'answer' }],
          timestamp: '2026-05-17T08:00:01.000Z',
          streaming: false,
        },
      ]
      const forkTurns = [
        {
          id: 'fork-user',
          turn_id: 'turn-fx-10-2',
          role: 'user' as const,
          text: 'hello',
          attachments: [],
          timestamp: '2026-05-17T08:00:00.000Z',
        },
        {
          id: 'fork-assistant',
          turn_id: 'turn-fx-10-2',
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
      api.forkSessionFromTurn.mockResolvedValueOnce({
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
      const ok = await store.forkTurn('turn-fx-10-1')
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
              turn_id: 'turn-fx-11-1',
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
              turn_id: 'turn-fx-11-2',
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
      api.forkSessionFromTurn.mockReturnValueOnce(new Promise(resolve => {
        resolveFork = resolve
      }))
      const store = useChatStore()

      await store.selectBot('bot-1')
      await flushPromises()
      const targetA = { botId: 'bot-1', sessionId: 'source-session', viewId: 'chat:a' }
      const targetB = { botId: 'bot-1', sessionId: 'other-session', viewId: 'chat:b' }
      store.bindChatView(targetA.viewId, targetA, true)
      store.focusChatView(targetA.viewId)
      const fork = store.forkTurn('turn-fx-11-1', { target: targetA })
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
        turn_id: 'turn-fx-12-1',
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
      api.forkSessionFromTurn.mockReturnValueOnce(response.promise)
      const store = useChatStore()
      await store.selectBot('bot-1')
      await flushPromises()

      const fork = store.forkTurn('turn-fx-12-1')
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
          turn_id: 'turn-fx-13-1',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'answer' }],
          timestamp: '2026-05-17T08:00:01.000Z',
          streaming: false,
        },
      ])
      const store = useChatStore()

      await store.selectBot('bot-1')
      await flushPromises()
      const ok = await store.forkTurn('turn-fx-13-1')

      expect(ok).toBe(false)
      expect(api.forkSessionFromTurn).not.toHaveBeenCalled()
    })

  it('sends disable as an explicit reasoning effort override', async () => {
      h.sendUpdates = [
        runtime.started,
        runtime.completed,
      ]
      const store = useChatStore()

      await store.selectBot('bot-1')
      store.overrideReasoningEffort = REASONING_EFFORT_DISABLE
      const result = await store.sendMessage('hello')

      expect(result).toMatchObject({ ok: true })
      expect(h.sentWSMessages).toHaveLength(1)
      expect(h.sentWSMessages[0]!.reasoning_effort).toBe(REASONING_EFFORT_DISABLE)
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
      })
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
      })
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
      h.sendUpdates = [
        runtime.started,
        runtime.failed('model failed'),
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
      expect(h.sentWSMessages[0]).toMatchObject({
        type: 'message',
        text: '/wat',
        composer_scope: 'bot-1:panel-a',
      })
      expect(h.sentWSMessages[0]?.requested_skills).toBeUndefined()
    })

  it('forwards a non-Memoh slash in a pending ACP draft to backend command authority', async () => {
      h.sendUpdates = []
      const store = useChatStore()
      const attachment = {
        type: 'file',
        base64: 'data:text/plain;base64,aGVsbG8=',
        mime: 'text/plain',
        name: 'note.txt',
      }

      await store.selectBot('bot-1')
      store.stageACPSession({ agentId: 'codex' })
      const sending = store.sendMessage('/flutter-adding-home-screen-widgets', [attachment], {
        composerScope: 'bot-1:draft-a',
      })
      await flushPromises()

      expect(h.sentWSMessages[0]).toMatchObject({
        type: 'message',
        session_id: 'session-1',
        text: '/flutter-adding-home-screen-widgets',
        composer_scope: 'bot-1:draft-a',
        attachments: [attachment],
      })
      expect(h.sentWSMessages[0]?.requested_skills).toBeUndefined()
      const invocationId = wsInvocationId(0)
      h.streamHandler?.({
        type: 'command_error',
        invocation_id: invocationId,
        session_id: 'session-1',
        composer_scope: 'bot-1:draft-a',
        terminal: true,
        error: { code: 'unknown_slash', message: 'Unknown slash command.' },
      })
      const result = await sending

      expect(result).toMatchObject({
        ok: false,
        stage: 'startup',
        restoreInput: '/flutter-adding-home-screen-widgets',
      })
      expect(store.streaming).toBe(false)
      const commandEvent = store.commandEventForScope({
        botId: 'bot-1',
        sessionId: 'session-1',
        composerScope: 'bot-1:draft-a',
      })
      expect(commandEvent).toMatchObject({
        type: 'command_error',
        error: { code: 'unknown_slash' },
      })
    })

  it('rejects attachments on Memoh quick actions in pending ACP drafts', async () => {
      const store = useChatStore()
      const attachment = {
        type: 'file',
        base64: 'data:text/plain;base64,aGVsbG8=',
        mime: 'text/plain',
        name: 'note.txt',
      }

      await store.selectBot('bot-1')
      store.stageACPSession({ agentId: 'codex' })
      const result = await store.sendMessage('/skill list', [attachment], {
        composerScope: 'bot-1:draft-a',
      })

      expect(result).toMatchObject({
        ok: false,
        stage: 'startup',
        restoreInput: '/skill list',
        restoreAttachments: [attachment],
      })
      expect(api.executeQuickAction).not.toHaveBeenCalled()
      expect(h.sentWSMessages).toHaveLength(0)
      expect(store.streaming).toBe(false)
      const commandEvent = store.commandEventForScope({ botId: 'bot-1', composerScope: 'bot-1:draft-a' })
      expect(commandEvent).toMatchObject({
        type: 'command_error',
        error: { code: 'slash_attachments_unsupported' },
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
      })
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
      expect(h.sentWSMessages).toHaveLength(0)
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
      expect(store.streaming).toBe(false)
    })

  it('sends only skill name in requested skill websocket payloads', async () => {
      h.sendUpdates = [runtime.started, runtime.completed]
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
      expect(h.sentWSMessages[0]?.requested_skills).toEqual([{
        name: 'alpha',
      }])
      expect(JSON.stringify(h.sentWSMessages[0]?.requested_skills)).not.toContain('Display-only description')
      expect(JSON.stringify(h.sentWSMessages[0]?.requested_skills)).not.toContain('managed')
    })

  it('inserts direct skill activation from the authoritative runtime user turn', async () => {
      h.sendUpdates = []
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
      expect(h.sentWSMessages[0]).toMatchObject({
        type: 'message',
        text: '/flutter-adding-home-screen-widgets',
      })
      expect(h.sentWSMessages[0]?.requested_skills).toBeUndefined()

      emitRuntime(runtime.userTurn({
          id: 'msg-skill',
          turn_id: `turn-${runId}`,
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
      }), 'session-a', runId)
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

      emitRuntime(runtime.message({ id: 1, type: 'text', content: 'Done' }), 'session-a', runId)
      expect(store.messages[1]).toMatchObject({
        role: 'assistant',
        messages: [{ type: 'text', content: 'Done' }],
        streaming: true,
      })

      emitRuntime(runtime.completed, 'session-a', runId)
      await expect(sendPromise).resolves.toMatchObject({ ok: true })
    })

  it('blocks a second deferred draft send while the first stream is still unbound', async () => {
      h.sendUpdates = []
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
      expect(h.sentWSMessages).toHaveLength(1)

      const invocationId = wsInvocationId(0)
      h.streamHandler?.({
        type: 'session_created',
        invocation_id: invocationId,
        session_id: 'session-created',
      })
      emitRuntime(runtime.completed, 'session-created', runId)
      await expect(firstSend).resolves.toMatchObject({ ok: true })
      expect(store.streaming).toBe(false)
    })

  it('aborts a deferred draft stream before session_created binds it', async () => {
      h.sendUpdates = []
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
      expect(h.abortedWSRuns).toEqual([])
      h.streamHandler?.({
        type: 'session_created',
        invocation_id: wsInvocationId(0),
        session_id: 'session-created',
      })
      expect(h.abortedWSRuns).toContain(runId)
      expect(store.streaming).toBe(false)
    })

  it('names an outbound turn by invocation and never sends a stream id', async () => {
      h.sendUpdates = []
      const store = useChatStore()

      await store.selectBot('bot-1')
      const sending = store.sendMessage('hello')
      await flushPromises()

      const sent = h.sentWSMessages[0]!
      expect(sent).toMatchObject({ type: 'message', invocation_id: expect.any(String) })
      expect(sent).not.toHaveProperty('stream_id')
      // The client mints the invocation; only the server names the run, and it is
      // that name a stop has to address.
      expect(sent.invocation_id).not.toBe(wsRunId(0))

      store.abort()
      await expect(sending).resolves.toMatchObject({ ok: false })
      expect(h.abortedWSRuns).toEqual([wsRunId(0)])
    })

  it('replays a stop pressed before the run was accepted', async () => {
      h.sendUpdates = []
      // Withhold acceptance so the stop lands while the turn has no run id.
      api.connectWebSocket.mockImplementation((_botId: string, onStreamEvent: UIStreamEventHandler) => {
        h.streamHandler = onStreamEvent
        return {
          get connected() {
            return true
          },
          send: vi.fn((message: Record<string, unknown>) => {
            if (
              message.type === 'runtime_subscribe'
              || message.type === 'runtime_unsubscribe'
            ) return
            h.sentWSMessages.push(message)
          }),
          abort: vi.fn((runId: string) => {
            h.abortedWSRuns.push(runId)
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
      const invocationId = h.sentWSMessages.find(
        message => message.type === 'message',
      )!.invocation_id as string

      store.abort()
      expect(h.abortedWSRuns).toEqual([])

      h.streamHandler?.({
        type: 'run_accepted',
        run_id: 'run-late',
        invocation_id: invocationId,
        session_id: 'session-1',
        turn_id: 'turn-run-late',
        epoch: 'epoch-session-1',
        seq: 1,
      })

      await expect(sending).resolves.toMatchObject({ ok: false })
      expect(h.abortedWSRuns).toEqual(['run-late'])
    })

  it('fails a rejected submission with the code the server refused it by', async () => {
      h.sendUpdates = []
      const store = useChatStore()

      await store.selectBot('bot-1')
      const sending = store.sendMessage('hello')
      await flushPromises()
      const invocationId = wsInvocationId(0)

      h.streamHandler?.({
        type: 'run_rejected',
        invocation_id: invocationId,
        session_id: 'session-1',
        code: 'session_runtime.session_busy',
        message: 'This conversation is still working on the previous message.',
      })

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
      h.sendUpdates = []
      const store = useChatStore()

      await store.selectBot('bot-1')
      const sending = store.sendMessage('activate', undefined, {
        requestedSkills: [{ name: 'alpha' }],
        composerScope: 'bot-1:draft-a',
      })
      await flushPromises()
      const invocationId = wsInvocationId(0)
      const runId = wsRunId(0)

      h.streamHandler?.({ type: 'session_created', invocation_id: invocationId, session_id: 'session-first' })
      h.streamHandler?.({ type: 'session_created', invocation_id: invocationId, session_id: 'session-conflict' })

      expect(store.sessionId).toBe('session-first')
      expect(store.knownSessionSummary('session-first')).not.toBeNull()
      expect(store.knownSessionSummary('session-conflict')).toBeNull()

      emitRuntime(runtime.completed, 'session-first', runId)
      await expect(sending).resolves.toMatchObject({ ok: true })
    })

  it('does not select a late session_created event after the user switches sessions', async () => {
      h.sendUpdates = []
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
      h.streamHandler?.({ type: 'session_created', invocation_id: invocationId, session_id: 'created-session' })
      await flushPromises()

      expect(store.sessionId).toBe('session-b')
      // The hidden Draft view still owns this stream, so its new Session is
      // remembered without stealing global focus from session-b.
      expect(store.knownSessionSummary('created-session')).not.toBeNull()

      emitRuntime(runtime.completed, 'created-session', runId)
      await sendPromise

      expect(store.sessionId).toBe('session-b')
    })

  it('deletes a deferred draft session when requested skill preflight fails after session_created', async () => {
      h.sendUpdates = []
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

      h.streamHandler?.({ type: 'session_created', invocation_id: invocationId, session_id: 'created-session' })
      await flushPromises()
      expect(store.sessionId).toBe('created-session')

      h.streamHandler?.({
        type: 'command_error',
        invocation_id: invocationId,
        session_id: 'created-session',
        composer_scope: 'bot-1:draft-a',
        terminal: true,
        error: {
          code: 'unsupported_skill_slash_context',
          message: 'Requested skills are not supported here.',
        },
      })
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
      h.sendUpdates = []
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
      h.streamHandler?.({ type: 'session_created', invocation_id: invocationId, session_id: 'created-session' })
      h.streamHandler?.({
        type: 'command_error',
        invocation_id: invocationId,
        session_id: 'created-session',
        composer_scope: 'bot-1:draft-a',
        terminal: true,
        error: {
          code: 'unsupported_skill_slash_context',
          message: 'Requested skills are not supported here.',
        },
      })
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
      h.sendUpdates = []
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
      h.streamHandler?.({ type: 'session_created', invocation_id: invocationId, session_id: 'created-session' })
      h.streamHandler?.({ type: 'error', run_id: runId, session_id: 'created-session', message: 'model failed' })
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
      emitRuntimeTo(staleHandler, runtime.started, 'old-session', 'old-stream')
      emitRuntimeTo(
        staleHandler,
        runtime.message({ id: 0, type: 'text', content: 'late old-bot output' }),
        'old-session',
        'old-stream',
      )

      expect(store.currentBotId).toBe('bot-2')
      expect(store.isSessionStreaming('bot-1', 'old-session')).toBe(false)
      expect(store.messages).toHaveLength(0)
    })

  it('clears the previous bot sessions before the next bot snapshot arrives', async () => {
      api.fetchBots.mockResolvedValue([
        { id: 'bot-1', status: 'active', name: 'Bot 1' },
        { id: 'bot-2', status: 'active', name: 'Bot 2' },
      ])
      const botTwoSessions = deferred<{
        items: Array<{ id: string, bot_id: string, title: string, type: string }>
        nextCursor: null
      }>()
      api.fetchSessions
        .mockResolvedValueOnce({
          items: [{ id: 'session-old', bot_id: 'bot-1', title: 'Old', type: 'chat' }],
          nextCursor: null,
        })
        .mockReturnValueOnce(botTwoSessions.promise)
      const store = useChatStore()

      await store.selectBot('bot-1')
      const switching = store.selectBot('bot-2')

      expect(store.currentBotId).toBe('bot-2')
      expect(store.loadingChats).toBe(true)
      expect(store.sessions).toEqual([])

      botTwoSessions.resolve({
        items: [{ id: 'session-new', bot_id: 'bot-2', title: 'New', type: 'chat' }],
        nextCursor: null,
      })
      await switching

      expect(store.sessions.map(session => session.id)).toEqual(['session-new'])
    })

  it('reattaches an active stream even when return hydration fails', async () => {
      h.sendUpdates = []
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
      emitRuntime(runtime.message({ id: 0, type: 'text', content: 'live' }), 'session-a', runId)

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
      emitRuntime(runtime.completed, 'session-a', runId)
      await sending
    })

  it('does not expose a cached Session transcript while it rehydrates', async () => {
      api.fetchSessions.mockResolvedValueOnce({ items: [
        { id: 'session-a', bot_id: 'bot-1', title: 'A', type: 'chat' },
        { id: 'session-b', bot_id: 'bot-1', title: 'B', type: 'chat' },
      ], nextCursor: null })
      const freshSessionA = deferred<UITurn[]>()
      api.fetchMessagesUI
        .mockResolvedValueOnce([{
          id: 'cached-a',
          turn_id: 'cached-a',
          role: 'user',
          text: 'cached',
          attachments: [],
          timestamp: '2026-01-01T00:00:00.000Z',
        }])
        .mockResolvedValueOnce([])
        .mockReturnValueOnce(freshSessionA.promise)
      const store = useChatStore()

      await store.selectBot('bot-1')
      await flushPromises()
      await store.selectSession('session-b')
      await flushPromises()
      await store.selectSession('session-a')

      expect(store.chatView({
        botId: 'bot-1',
        sessionId: 'session-a',
        viewId: 'chat',
      }).transcript.messages.map(turn => turn.id)).toEqual(['cached-a'])
      expect(store.loadingMessages).toBe(true)
      expect(store.messages).toEqual([])

      freshSessionA.resolve([{
        id: 'fresh-a',
        turn_id: 'fresh-a',
        role: 'user',
        text: 'fresh',
        attachments: [],
        timestamp: '2026-01-02T00:00:00.000Z',
      }])
      await flushPromises()

      expect(store.messages.map(turn => turn.id)).toEqual(['fresh-a'])
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
      // Subagent sessions are writable: the user can talk to (and abort) the
      // spawned agent directly from its own session view.
      expect(store.activeChatReadOnly).toBe(false)

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
        turn_id: 'turn-fx-14-1',
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

  it('drops a pagination page when a newer full refresh replaces its cursor', async () => {
      const oldPage = deferred<{
        items: Array<{ id: string, bot_id: string, title: string, type: string }>
        nextCursor: null
      }>()
      api.fetchSessions
        .mockResolvedValueOnce({
          items: [{ id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' }],
          nextCursor: 'cursor-old',
        })
        .mockReturnValueOnce(oldPage.promise)
        .mockResolvedValueOnce({
          items: [{ id: 'session-new', bot_id: 'bot-1', title: 'New', type: 'chat' }],
          nextCursor: 'cursor-new',
        })
      const store = useChatStore()

      await store.selectBot('bot-1')
      const loadingPage = store.loadMoreSessions()
      await flushPromises()

      h.sessionsActivityHandler?.({
        type: 'session_created',
        session_id: 'session-new',
        session_type: 'chat',
        title: 'New',
      })
      await flushPromises()
      expect(store.sessions.map(session => session.id)).toEqual(['session-new'])
      expect(store.sessionsCursor).toBe('cursor-new')

      oldPage.resolve({
        items: [{ id: 'session-stale-page', bot_id: 'bot-1', title: 'Stale', type: 'chat' }],
        nextCursor: null,
      })
      await loadingPage

      expect(store.sessions.map(session => session.id)).toEqual(['session-new'])
      expect(store.sessionsCursor).toBe('cursor-new')
      expect(store.hasMoreSessions).toBe(true)
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
      h.sendUpdates = [
        runtime.started,
        runtime.message({ id: 0, type: 'text', content: 'hello' }),
      ]
      api.fetchSessions.mockResolvedValueOnce({
        items: [{ id: 'session-1', bot_id: 'bot-1', title: 'Chat', type: 'chat' }],
        nextCursor: null,
      })
      const store = useChatStore()
      await store.selectBot('bot-1')
      await flushPromises()

      // Drive the actual send: this appends an optimistic user turn plus a
      // streaming assistant turn. The WS mock auto-replays h.sendUpdates.
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
          turn_id: 'turn-fx-14-2',
          role: 'user',
          text: 'hi',
          attachments: [],
          timestamp: past,
        },
        {
          id: 'server-assistant',
          turn_id: 'turn-fx-14-2',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'hello', running: false }],
          timestamp: past,
        },
      ])
      emitRuntime(runtime.completed, h.lastSessionId, h.lastRunId)
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
        id: `00000000-0000-4000-8000-${String(idx).padStart(12, '0')}`,
        turn_id: `turn-${idx}`,
        // A settled page always carries turn positions; the older-page cursor
        // is taken from the oldest turn that has one.
        turn_position: idx + 1,
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
      h.sendUpdates = [runtime.started]
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
      const runId = h.lastRunId
      expect(runId).not.toBe('')

      await store.selectSession('session-2')
      api.deleteSession.mockResolvedValueOnce(undefined)
      await store.removeSession('session-1')
      await expect(sending).resolves.toMatchObject({ ok: false, stage: 'stream' })

      emitRuntime(runtime.message({ id: 1, type: 'text', content: 'late A output' }), 'session-1', runId)
      emitRuntime(runtime.completed, 'session-1', runId)
      await flushPromises()

      expect(h.abortedWSRuns).toContain(runId)
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
          turn_id: 'turn-fx-14-3',
          role: 'user',
          text: 'bot two prompt',
          timestamp: '2026-06-20T00:00:00.000Z',
        },
        {
          id: 'bot-2-assistant',
          turn_id: 'turn-fx-14-3',
          role: 'assistant',
          messages: [{ id: 1, type: 'text', content: 'bot two reply' }],
          timestamp: '2026-06-20T00:00:01.000Z',
        },
      ])
      await store.selectBot('bot-2')
      await flushPromises()
      h.sendUpdates = [runtime.started]
      const botTwoSend = store.sendMessage('keep bot two streaming')
      await flushPromises()
      const botTwoRunId = h.lastRunId

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
      expect(h.abortedWSRuns).not.toContain(botTwoRunId)
      expect(store.deletedSession).toEqual({
        id: 'shared-session',
        botId: 'bot-1',
        seq: 1,
      })
      emitRuntime(runtime.completed, 'shared-session', botTwoRunId)
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
      api.fetchSessions.mockResolvedValueOnce({
        items: [
          { id: 'session-1', bot_id: 'bot-1', title: 'A', type: 'chat' },
          { id: 'session-3', bot_id: 'bot-1', title: 'C', type: 'chat' },
        ],
        nextCursor: null,
      })
      h.sessionsActivityHandler?.({
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
      h.sessionsActivityHandler?.({ type: 'dropped', count: 2 })
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
      h.sessionsActivityHandler?.({
        type: 'session_created',
        session_id: 'session-2',
        session_type: 'discuss',
        title: 'New',
      })
      await flushPromises()

      expect(store.sessions.map(s => s.id)).toEqual(['session-2', 'session-1'])
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
      // Hydration proof: the summary's subagent type reached the target; the
      // session itself stays writable so the user can chat with the agent.
      expect(store.chatReadOnlyFor(targetB)).toBe(false)
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
      expect(h.sentWSMessages).toEqual([])

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
      h.sendUpdates = [runtime.started]
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
      expect(h.sentWSMessages.at(-1)).toMatchObject({ session_id: 'session-a', composer_scope: 'bot-1:chat:a' })

      store.abort(targetA)
      await expect(sending).resolves.toMatchObject({ ok: false, stage: 'stream' })
      expect(store.sessionId).toBe('session-b')
      expect(store.chatView(targetB).transcript.messages).toEqual([])
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
      h.sendUpdates = [runtime.started]
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
      h.sendUpdates = []
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
      h.streamHandler?.({
        type: 'session_created',
        invocation_id: invocationId,
        session_id: 'session-a',
      })

      expect(store.sessionId).toBeNull()
      expect(store.chatView({ ...targetA, sessionId: 'session-a' }).sessionId).toBe('session-a')
      expect(store.chatView(targetB).kind).toBe('draft')

      emitRuntime(runtime.completed, 'session-a', runId)
      await expect(sending).resolves.toMatchObject({ ok: true })
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

      h.sendUpdates = richActiveRunStoreScript()
      const sendPromise = store.sendMessage('please inspect')
      await flushPromises()
      await flushPromises()

      expect(h.sentWSMessages[0]).toMatchObject({
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

      emitRuntime(runtime.completed, h.lastSessionId, h.lastRunId)
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

      h.sendUpdates = interruptedRunStoreScript()
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

      emitRuntime(runtime.started, 'session-1', 'run-old')
      emitRuntime(
        runtime.message({ id: 0, type: 'text', content: 'old session output' }),
        'session-1',
        'run-old',
      )

      expect(store.messages).toEqual([])

      emitRuntime(runtime.completed, 'session-1', 'run-old')
    })
})
