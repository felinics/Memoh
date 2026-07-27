import { defineStore, storeToRefs } from 'pinia'
import { computed, reactive, ref, watch } from 'vue'
import { toast } from '@felinic/ui'
import enMessages from '@/i18n/locales/en.json'
import zhMessages from '@/i18n/locales/zh.json'
import jaMessages from '@/i18n/locales/ja.json'
import { useChatSelectionStore } from '@/store/chat-selection'
import { onAuthSessionCleared } from '@/lib/auth-session'
import { parseMemohError, resolveApiErrorMessage } from '@/utils/api-error'
import {
  normalizedRuntimeType,
  provisionalSessionTitle,
  shouldRefreshFromMessageCreated,
  type SidebarSessionMode,
} from './chat-list.utils'
import {
  cloneRequestedSkills,
  createInvocationId,
  hasUserAttachments,
  isPendingBot,
  nextId,
  normalizeRequestedSkills,
  requestedSkillRequestsForWire,
} from './chat-list.normalize'
import { createFsChangeBeacon } from './chat/fs-beacon'
import { createCommandEventRegistry } from './chat/command-events'
import { createSessionList } from './chat/session-list'
import {
  acpSessionMetadata,
  createACPStaging,
  type DetachedACPSession,
} from './chat/acp-staging'
import { createTranscriptController } from './chat/transcript'
import {
  createChatViewRegistry,
  type ChatViewEntry,
} from './chat/view-registry'
import { createAssistantStreamRegistry } from './chat/assistant-streams'
import { createChatRealtimeController } from './chat/realtime'
import { createACPRuntimeRegistry } from './chat/acp-runtime-registry'
import { createChatRefreshCoordinator } from './chat/refresh-coordinator'
import {
  createApprovalResponseTracker,
  type ApprovalResponse,
  type ApprovalResponseOutcome,
} from './chat/approval-responses'
import {
  createBackgroundTaskTracker,
  normalizeBackgroundTask,
} from './chat/background-tasks'
import type {
  ACPAgentSessionInput,
  ActiveChatTarget,
  ChatAssistantTurn,
  ChatMessage,
  ChatUserTurn,
  ChatWorkspaceTargetSelectionSource,
  ChatWorkspaceTargetSnapshot,
  ChatViewTarget,
  SendMessageOptions,
  SendMessageResult,
  SendMessageStage,
} from './chat/types'
import {
  createSession,
  deleteSession as requestDeleteSession,
  updateSessionAgent as requestUpdateSessionAgent,
  updateSessionTitle as requestUpdateSessionTitle,
  forkSessionFromMessage as requestForkSessionFromMessage,
  fetchSession,
  fetchSessions,
  type Bot,
  type BotSessionActivityEvent,
  type SessionSummary,
  type SessionMessageStreamEvent,
  type ChatAttachment,
  type CommandEventResponse,
  type UIToolApproval,
  type UIUserInput,
  type RequestedSkillSelection,
  type WSUserInputAnswer,
  type UIStreamEvent,
  executeQuickAction,
  fetchBots,
  fetchMessagesUI,
  locateMessageUI,
} from '@/composables/api/useChat'
import { ACP_DEFAULT_PROJECT_MODE, ACP_DEFAULT_PROJECT_PATH } from '@/utils/acp'
import { isGuiToolName } from '@/utils/gui-tools'
import { getBotsByBotIdSettings } from '@memohai/sdk'

export type {
  ACPAgentSessionInput,
  ActiveChatTarget,
  AttachmentBlock,
  AttachmentItem,
  BackgroundTask,
  ChatAssistantTurn,
  ChatMessage,
  ChatSystemTurn,
  ChatUserTurn,
  ChatWorkspaceTargetSnapshot,
  ContentBlock,
  ErrorBlock,
  SendMessageOptions,
  SendMessageResult,
  SendMessageStage,
  TextBlock,
  ThinkingBlock,
  ToolCallBlock,
} from './chat/types'

// fs-change beacon lives in ./chat/fs-beacon; types re-exported so existing
// consumers keep importing them from the store module.
export type { FsChangeBatch, FsChangeEvent, FsToolKind } from './chat/fs-beacon'
export type { ChatViewEntry, ChatViewTarget } from './chat/view-registry'

function currentLocale() {
  const storage = globalThis.localStorage
  const locale = typeof storage?.getItem !== 'function'
    ? ''
    : storage.getItem('language')
  return locale === 'zh' || locale === 'ja' ? locale : 'en'
}

function localizedMessages() {
  const locale = currentLocale()
  return locale === 'zh' ? zhMessages : locale === 'ja' ? jaMessages : enMessages
}

function userInputConnectionLostMessage() {
  const messages = localizedMessages()
  return messages.chat.tools.userInputConnectionLost
}

function sendFailedMessage() {
  const messages = localizedMessages()
  return messages.chat.sendFailed
}

function commandErrorMessage(code: string) {
  const messages = localizedMessages()
  const errors = messages.chat.slash.errorMessages as Record<string, string>
  return errors[code] || errors.generic || 'Slash command failed.'
}

function forkFailedMessage() {
  const messages = localizedMessages()
  return messages.chat.forkFailed
}

type WebNewCommandResult =
  | { kind: 'none' }
  | { kind: 'handled' }
  | { kind: 'error'; message: string }

type WebSlashCommandResult =
  | { kind: 'none' }
  | { kind: 'handled' }
  | { kind: 'error'; message: string }

function parseWebNewCommand(text: string): { mode: 'chat' | 'discuss' | ''; agentId: string } | null {
  const input = text.trim()
  if (!input.startsWith('/new')) return null
  const parts = input.split(/\s+/)
  if (parts[0] !== '/new') return null
  const positional = parts.slice(1).filter(part => part && !part.startsWith('-'))
  const first = positional[0]?.toLowerCase() ?? ''
  const second = positional[1]?.toLowerCase() ?? ''
  if (first === 'chat' || first === 'discuss') {
    return { mode: first, agentId: second }
  }
  return { mode: '', agentId: first }
}

interface StartupSendFailure {
  id: string
  botId: string
  sessionId: string
  composerScope?: string
  error: string
  restoreInput: string
  restoreAttachments?: ChatAttachment[]
  restoreRequestedSkills?: RequestedSkillSelection[]
}

interface GuiToolUseRequest {
  botId: string
  sessionId: string
  toolCallId: string
  toolName: string
  seq: number
}

class StreamFailureError extends Error {
  stage: SendMessageStage
  feedback?: unknown

  constructor(message: string, stage: SendMessageStage, feedback?: unknown) {
    super(message)
    this.name = 'StreamFailureError'
    this.stage = stage
    this.feedback = feedback
  }
}

class CommandStreamError extends StreamFailureError {
  constructor(message: string) {
    super(message, 'startup')
    this.name = 'CommandStreamError'
  }
}

export const useChatStore = defineStore('chat', () => {
  const selectionStore = useChatSelectionStore()
  const { currentBotId, sessionId, draftIntent, explicitSelection: explicitSessionSelection } = storeToRefs(selectionStore)
  const acpRuntimeRegistry = createACPRuntimeRegistry({ currentBotId, sessionId })
  const {
    acpRuntimeStatuses,
    acpRuntimePending,
    acpRuntimeKey,
    clearACPRuntimeStatus,
    ensureACPRuntime,
    setACPRuntimeModel,
    setACPRuntimeReasoning,
    resetACPRuntimeRegistry,
  } = acpRuntimeRegistry

  const fsBeacon = createFsChangeBeacon({ currentBotId, sessionId })
  const {
    fsChangedAt,
    markFsChanged,
    affectsPath,
    fsEventForPath,
    bumpFsChangedAtIfFsMutation,
    resetFsBeacon,
    clearFsForBotSwitch,
  } = fsBeacon
  const backgroundTasks = createBackgroundTaskTracker()
  const {
    rememberBackgroundTask,
    applyPendingBackgroundEventsToTool,
  } = backgroundTasks
  const guiToolUseRequested = ref<GuiToolUseRequest | null>(null)
  const seenGuiToolCalls = new Set<string>()
  let guiToolUseRequestSeq = 0
  const focusedChatViewId = ref('chat')
  let transcriptSnapshotHook: (view: ChatViewEntry, targetSessionId: string | undefined, turns: import('@/composables/api/useChat.types').UITurn[]) => void = () => {}
  let transcriptRefreshAppliedHook: (view: ChatViewEntry, targetSessionId: string, latestTimestamp?: string) => void = () => {}
  let sessionStreamingProbe: (botId: string, targetSessionId: string) => boolean = () => false
  let stopEvictedSessionStream: (botId: string, targetSessionId: string) => void = () => {}
  let discardEvictedDraftStage: (view: ChatViewEntry) => void = () => {}
  const chatViews = createChatViewRegistry({
    rememberBackgroundTask,
    applyPendingBackgroundEventsToTool,
    bumpFsChangedAtIfFsMutation,
    fetchMessages: fetchMessagesUI,
    locateMessage: locateMessageUI,
    isSessionStreaming: (botId, targetSessionId) => sessionStreamingProbe(botId, targetSessionId),
    onSnapshot: (view, targetSessionId, turns) => transcriptSnapshotHook(view, targetSessionId, turns),
    onRefreshApplied: (view, targetSessionId, latestTimestamp) => {
      transcriptRefreshAppliedHook(view, targetSessionId, latestTimestamp)
    },
    onEvict: (view) => {
      if (view.kind === 'session' && view.sessionId) {
        stopEvictedSessionStream(view.botId, view.sessionId)
      } else if (view.kind === 'draft') {
        discardEvictedDraftStage(view)
      }
    },
  })

  function normalizedChatViewTarget(target?: Partial<ChatViewTarget>): ChatViewTarget {
    const botId = (target?.botId ?? currentBotId.value ?? '').trim() || '__unbound__'
    const targetSessionId = target && 'sessionId' in target
      ? target.sessionId?.trim() || null
      : sessionId.value?.trim() || null
    const viewId = target?.viewId?.trim() || focusedChatViewId.value.trim() || 'chat'
    return { botId, sessionId: targetSessionId, viewId }
  }

  function isFocusedChatTarget(target: ChatViewTarget): boolean {
    const resolved = normalizedChatViewTarget(target)
    if (
      resolved.botId !== (currentBotId.value ?? '').trim()
      || resolved.viewId !== focusedChatViewId.value
    ) return false
    const selectedSessionId = (sessionId.value ?? '').trim()
    return resolved.sessionId
      ? selectedSessionId === resolved.sessionId
      : !selectedSessionId
  }

  const draftSessionCreations = reactive(new Set<string>())

  function draftSessionCreationKey(target: ChatViewTarget): string {
    const resolved = normalizedChatViewTarget(target)
    return `${resolved.botId}\u0000${resolved.viewId}`
  }

  function isChatViewCreatingSession(target: ChatViewTarget): boolean {
    const resolved = normalizedChatViewTarget(target)
    return !resolved.sessionId && draftSessionCreations.has(draftSessionCreationKey(resolved))
  }

  function chatView(target?: Partial<ChatViewTarget>): ChatViewEntry {
    return chatViews.getOrCreate(normalizedChatViewTarget(target))
  }

  function workspaceTargetSelectionFor(target?: ChatViewTarget) {
    const view = chatView(target)
    return {
      targetId: view.workspaceTargetId.value,
      snapshot: view.workspaceTargetSnapshot.value,
      source: view.workspaceTargetSelectionSource.value,
    }
  }

  function setWorkspaceTargetSelection(
    target: ChatViewTarget,
    targetId: string,
    snapshot: ChatWorkspaceTargetSnapshot | null = null,
    source: ChatWorkspaceTargetSelectionSource = 'user',
  ) {
    const id = targetId.trim()
    if (!id) return
    const view = chatView(target)
    view.workspaceTargetId.value = id
    view.workspaceTargetSnapshot.value = snapshot ? { ...snapshot, target_id: id } : null
    view.workspaceTargetSelectionSource.value = source
  }

  function initializeWorkspaceTargetSelection(
    target: ChatViewTarget,
    targetId: string,
    snapshot: ChatWorkspaceTargetSnapshot | null,
    source: Extract<ChatWorkspaceTargetSelectionSource, 'default' | 'session'>,
  ) {
    const id = targetId.trim()
    if (!id) return
    const view = chatView(target)
    const currentSource = view.workspaceTargetSelectionSource.value
    if (currentSource === 'user') return
    if (source === 'default' && currentSource !== 'unset') return
    setWorkspaceTargetSelection(target, id, snapshot, source)
  }

  function resetWorkspaceTargetSelection(target: ChatViewTarget) {
    const view = chatView(target)
    view.workspaceTargetId.value = ''
    view.workspaceTargetSnapshot.value = null
    view.workspaceTargetSelectionSource.value = 'unset'
  }

  function sessionTranscript(botId: string, targetSessionId: string) {
    return chatViews.getOrCreate({
      botId: botId.trim(),
      sessionId: targetSessionId.trim(),
      viewId: focusedChatViewId.value,
    }).transcript
  }

  function activeTranscript() {
    return chatView().transcript
  }

  function transcriptForTarget(target?: Partial<ChatViewTarget>) {
    return chatView(target).transcript
  }

  function transcriptForTurn(turn: ChatMessage) {
    return chatViews.entries().find(view => view.transcript.messages.includes(turn))?.transcript
      ?? null
  }

  const messages = computed(() => activeTranscript().messages)
  const loadingMessages = computed(() => activeTranscript().loadingMessages.value)
  const loadingOlder = computed(() => activeTranscript().loadingOlder.value)
  const hasMoreOlder = computed(() => activeTranscript().hasMoreOlder.value)
  const hasLoadedOlder = computed({
    get: () => activeTranscript().hasLoadedOlder.value,
    set: value => { activeTranscript().hasLoadedOlder.value = value },
  })

  const normalizeTurn = (...args: Parameters<ReturnType<typeof createTranscriptController>['normalizeTurn']>) => activeTranscript().normalizeTurn(...args)
  const clearHistoryView = (...args: Parameters<ReturnType<typeof createTranscriptController>['clearHistoryView']>) => activeTranscript().clearHistoryView(...args)
  const prepareForInitialization = () => activeTranscript().prepareForInitialization()
  const markHistoryEmpty = () => activeTranscript().markHistoryEmpty()
  async function refreshCurrentSession(targetBotId?: string, targetSessionId?: string) {
    const bid = (targetBotId ?? currentBotId.value ?? '').trim()
    const sid = (targetSessionId ?? sessionId.value ?? '').trim()
    if (!bid || !sid) return
    await sessionTranscript(bid, sid).refreshCurrentSession(bid, sid)
  }
  async function loadInitialMessages(botId: string, targetSessionId: string) {
    const view = chatViews.getOrCreate({ botId, sessionId: targetSessionId, viewId: focusedChatViewId.value })
    await view.transcript.loadInitialMessages(botId, targetSessionId)
    view.initialized = true
  }
  const fetchSessionWindow = (botId: string, targetSessionId: string) => sessionTranscript(botId, targetSessionId).fetchSessionWindow(botId, targetSessionId)
  const loadOlderMessages = (target?: ChatViewTarget) => transcriptForTarget(target).loadOlderMessages()
  const findMessageIdByExternalId = (externalMessageId: string, target?: ChatViewTarget) => transcriptForTarget(target).findMessageIdByExternalId(externalMessageId)
  const locateMessageByExternalId = (externalMessageId: string, target?: ChatViewTarget) => transcriptForTarget(target).locateMessageByExternalId(externalMessageId)
  function isActiveSessionTarget(botId: string, targetSessionId: string) {
    return currentBotId.value === botId.trim() && sessionId.value === targetSessionId.trim()
  }
  const appendTurnToSession = (botId: string, targetSessionId: string, turn: ChatMessage) => sessionTranscript(botId, targetSessionId).appendTurnToSession(botId, targetSessionId, turn)
  const reattachTurnToSession = (botId: string, targetSessionId: string, turn: ChatMessage) => sessionTranscript(botId, targetSessionId).reattachTurnToSession(botId, targetSessionId, turn)
  const removeTurnFromSession = (botId: string, targetSessionId: string, turn: ChatMessage) => {
    const transcript = targetSessionId.trim()
      ? sessionTranscript(botId, targetSessionId)
      : transcriptForTurn(turn)
    transcript?.removeTurnFromSession(botId, targetSessionId, turn)
  }
  const restoreTailFromOptimistic = (botId: string, targetSessionId: string, optimisticUserTurn: ChatUserTurn | null, assistantTurn: ChatAssistantTurn, replacedTurns: ChatMessage[]) => sessionTranscript(botId, targetSessionId).restoreTailFromOptimistic(botId, targetSessionId, optimisticUserTurn, assistantTurn, replacedTurns)
  const createOptimisticAssistantTurn = () => activeTranscript().createOptimisticAssistantTurn()
  const upsertAssistantUIMessage = (...args: Parameters<ReturnType<typeof createTranscriptController>['upsertAssistantUIMessage']>) => transcriptForTurn(args[0])?.upsertAssistantUIMessage(...args)
  const hasVisibleAssistantBlocks = (turn: ChatAssistantTurn) => transcriptForTurn(turn)?.hasVisibleAssistantBlocks(turn) ?? false
  const finishAssistantTurn = (turn: ChatAssistantTurn) => { transcriptForTurn(turn)?.finishAssistantTurn(turn) }
  const finalizeStreamFailure = (assistantTurn: ChatAssistantTurn, botId: string, targetSessionId: string, error: Error) => {
    transcriptForTurn(assistantTurn)?.finalizeStreamFailure(assistantTurn, botId, targetSessionId, error)
  }
  const hasTurn = (turn: ChatMessage) => chatViews.entries().some(view => view.transcript.hasTurn(turn))
  const markToolApprovalDecision = (approvalId: string, status: 'approved' | 'rejected' | 'pending') => activeTranscript().markToolApprovalDecision(approvalId, status)
  const resetTranscriptUserScope = () => chatViews.resetAll()
  const assistantStreams = createAssistantStreamRegistry({ currentBotId, sessionId, finishAssistantTurn })
  const {
    streaming,
    streamingSessionId,
    assistantStreamsForSession,
    activeUnboundInvocationIds,
    isSessionStreaming,
    isUnboundComposerStreaming,
    invocationIdForEvent,
    trackAssistantStream,
    bindRunId,
    requestAbort,
    getAssistantStream,
    mapAssistantStreamMessage,
    resolveAssistantStream,
    rejectAssistantStream,
    discardAssistantStream,
    isTerminalInvocation,
    rejectAllStreams,
    recordCreatedSession,
    createdSessionIdForInvocation,
    forgetCreatedSession,
    clearStreamHistory,
  } = assistantStreams
  sessionStreamingProbe = isSessionStreaming

  function isChatViewStreaming(target: ChatViewTarget, composerScope?: string): boolean {
    const resolved = normalizedChatViewTarget(target)
    return resolved.sessionId
      ? isSessionStreaming(resolved.botId, resolved.sessionId)
      : isUnboundComposerStreaming(
          resolved.botId,
          composerScope?.trim() || `${resolved.botId}:${resolved.viewId}`,
        )
  }
  const approvalResponses = createApprovalResponseTracker({
    rollbackApproval: approvalId => markToolApprovalDecision(approvalId, 'pending'),
    onExpired: handleExpiredApprovalResponse,
  })
  const {
    hasPendingApprovalResponse,
    beginApprovalResponse,
    getApprovalResponse,
    settleApprovalResponse,
    pendingApprovalResponses,
    pendingApprovalResponsesForSession,
    isTerminalApprovalResponse,
    resetApprovalResponses,
  } = approvalResponses
  const forkingMessages = new Set<string>()
  // Sessions-list bookkeeping + fork-anchor tracking (see ./chat/session-list).
  const sessionList = createSessionList({ currentBotId, sessionId, messages })
  const {
    sessions,
    sessionsCursor,
    hasMoreSessions,
    loadingMoreSessions,
    activeSession,
    knownSessions,
    activeChatReadOnly,
    activeChatCanFork,
    withForkAnchorFromUITurns,
    syncForkAnchorFromUITurns,
    updateForkAnchorForReplacedMessage,
    replaceSessions,
    appendSessions,
    upsertSession,
    rememberSession,
    knownSessionSummary,
    hasListedSession,
    patchSessionInList,
    updateKnownSessionTitle,
    removeSessionFromList,
    touchSessionInList,
    touchKnownSession,
    fallbackSessionAfterDelete,
    markSessionDeleted,
    clearDeletedSessionIds,
    clearRememberedSessions,
  } = sessionList
  transcriptSnapshotHook = (_view, targetSessionId, turns) => {
    syncForkAnchorFromUITurns(targetSessionId, turns)
  }
  transcriptRefreshAppliedHook = (_view, targetSessionId, latestTimestamp) => {
    touchSessionInList(targetSessionId, latestTimestamp)
  }
  const refreshCoordinator = createChatRefreshCoordinator({
    currentBotId,
    sessionId,
    fetchSessions,
    applySessionsSnapshot: (response) => {
      replaceSessions(response.items)
      sessionsCursor.value = response.nextCursor
      hasMoreSessions.value = response.nextCursor !== null
    },
    isSessionStreaming,
    refreshCurrentSession,
  })
  const {
    refreshSessionsList,
    scheduleSessionRefresh,
    resetRefreshCoordinator,
  } = refreshCoordinator
  const realtime = createChatRealtimeController({
    onWebSocketEvent: (botId, event) => handleWSStreamEvent(event, undefined, botId),
    prepareSessionMessages,
    onSessionMessageEvent: handleSessionMessageEvent,
    onBotSessionsActivityEvent: handleBotSessionsActivityEvent,
  })
  const {
    startWebSocket,
    stopWebSocket,
    ensureWebSocketConnected,
    sendWebSocketMessage,
    abortWebSocketRun,
    startSessionMessagesStream,
    stopSessionMessagesStream,
    startBotSessionsActivityStream,
    stopStreams,
  } = realtime
  stopEvictedSessionStream = (botId, targetSessionId) => {
    stopSessionMessagesStream(botId, targetSessionId)
  }

  function releaseHiddenSessionView(view: ChatViewEntry | null) {
    if (!view || view.kind !== 'session' || !view.sessionId) return
    if (view.visiblePanelIds.size > 0) return
    // Visibility owns the live subscription; the registry independently owns
    // cached transcript retention for streaming and pending interactions.
    stopSessionMessagesStream(view.botId, view.sessionId)
    chatViews.prune()
  }

  function bindChatView(panelId: string, target: ChatViewTarget, visible = true): ChatViewEntry {
    const change = chatViews.bindPanel(panelId, normalizedChatViewTarget(target), visible)
    if (panelId.trim() === focusedChatViewId.value && change.view.kind === 'draft') {
      activateDraftACPStage({
        botId: change.view.botId,
        sessionId: null,
        viewId: change.view.viewId,
      })
    }
    releaseHiddenSessionView(change.deactivatedSession)
    if (change.activatedSession?.sessionId) {
      startSessionMessagesStream(change.activatedSession.botId, change.activatedSession.sessionId)
    }
    if (visible && change.view.kind === 'session' && change.view.sessionId) {
      void ensureVisibleSessionSummary(change.view.botId, change.view.sessionId)
    }
    return change.view
  }

  function setChatViewVisible(panelId: string, visible: boolean) {
    const change = chatViews.setPanelVisible(panelId, visible)
    if (!change) return
    releaseHiddenSessionView(change.deactivatedSession)
    if (change.activatedSession?.sessionId) {
      startSessionMessagesStream(change.activatedSession.botId, change.activatedSession.sessionId)
    }
    if (visible && change.view.kind === 'session' && change.view.sessionId) {
      void ensureVisibleSessionSummary(change.view.botId, change.view.sessionId)
    }
  }

  function unbindChatView(panelId: string) {
    releaseHiddenSessionView(chatViews.unbindPanel(panelId))
  }

  function focusChatView(viewId: string) {
    const id = viewId.trim()
    if (!id || id === focusedChatViewId.value) return
    saveLiveDraftACPStage()
    focusedChatViewId.value = id
    const view = chatViews.getPanel(id)
    if (view?.kind === 'draft') {
      activateDraftACPStage({ botId: view.botId, sessionId: null, viewId: view.viewId })
    }
  }

  function promoteDraftChatView(target: ChatViewTarget, targetSessionId: string): ChatViewEntry {
    invalidateDraftViewCommand(target)
    const promoted = chatViews.promoteDraft(target.botId, target.viewId, targetSessionId)
    if (promoted.visiblePanelIds.size > 0 && promoted.sessionId) {
      startSessionMessagesStream(promoted.botId, promoted.sessionId)
    }
    return promoted
  }
  const loading = ref(false)
  // `loadingChats` covers the bot-level boot path (sessions list fetch), so
  // the sidebar can show its skeleton + suppress its empty-state placeholder
  // exactly while the sessions list is in flight.
  // `loadingMessages` covers the per-session transcript fetch — the sidebar
  // never reacts to it, only the chat pane uses it to keep its own empty
  // placeholders hidden while a fresh transcript is on its way.
  const loadingChats = ref(false)
  const initializing = ref(false)
  let initializeRerunRequested = false
  let initializingBotId: string | null = null
  let initializePromise: Promise<void> | null = null
  let userScopeGeneration = 0
  const bots = ref<Bot[]>([])
  const overrideModelId = ref<string>('')
  const overrideReasoningEffort = ref<string>('')
  const startupSendFailures = ref<Record<string, StartupSendFailure>>({})
  function startupSendFailureKey(botId: string, targetSessionId: string, composerScope = '') {
    const bid = botId.trim()
    const scope = composerScope.trim()
    if (scope) return `composer:${bid}:${scope}`
    return `session:${bid}:${targetSessionId.trim()}`
  }
  function startupSendFailureFor(target: ChatViewTarget, composerScope = ''): StartupSendFailure | null {
    const resolved = normalizedChatViewTarget(target)
    const scopedKey = startupSendFailureKey(resolved.botId, resolved.sessionId ?? '', composerScope)
    const scoped = startupSendFailures.value[scopedKey]
    if (scoped) return scoped
    if (resolved.sessionId) {
      return startupSendFailures.value[startupSendFailureKey(resolved.botId, resolved.sessionId)] ?? null
    }
    return null
  }
  const startupSendFailure = computed(() => startupSendFailureFor(
    normalizedChatViewTarget(),
    focusedChatViewId.value === 'chat'
      ? 'chat'
      : `${(currentBotId.value ?? '').trim()}:${focusedChatViewId.value}`,
  ))
  // Slash-command event registry (see ./chat/command-events for scoping rules).
  const commandEventRegistry = createCommandEventRegistry({ currentBotId, sessionId })
  const {
    commandEvent,
    commandEventForScope,
    rememberCommandEvent,
    showCommandError,
    clearCommandEvent,
    rescopeSessionCommandEventToComposer,
    resetCommandEvents,
  } = commandEventRegistry
  // Bumps when the user sends a message, carrying the resolved session id and
  // whether that send just promoted a draft (created the session). The workspace
  // tab store watches this to pin the chat tab — a session you have sent in is no
  // longer an ephemeral "preview" tab. seq forces the watch to fire on repeats.
  const userSentInSession = ref<{
    id: string
    botId: string
    viewId: string
    wasDraft: boolean
    seq: number
  } | null>(null)
  let userSendSeq = 0
  const draftViewRequested = ref<{
    botId: string
    viewId: string
    expectedSessionId: string | null
    explicitSelection: boolean
    input: ACPAgentSessionInput | null
    activate: boolean
    seq: number
  } | null>(null)
  let draftViewRequestSeq = 0
  const draftViewCommandVersions = new Map<string, number>()
  let draftViewCommandSequence = 0
  const forkedSessionRequested = ref<{
    botId: string
    viewId: string
    expectedSessionId: string
    sessionId: string
    title: string
    explicitSelection: true
    activate: boolean
    seq: number
  } | null>(null)
  let forkedSessionRequestSeq = 0
  // Bumps after a session delete succeeds. Consumers that own per-session UI
  // chrome must not infer deletion from the paginated session list: a valid open
  // tab can fall off the current page without being deleted.
  const deletedSession = ref<{ id: string, botId: string, seq: number, composerScope?: string } | null>(null)
  let deletedSessionSeq = 0

  let selectSessionRequestId = 0
  const visibleSessionSummaryRequests = new Map<string, Promise<SessionSummary | null>>()

  const hasExplicitSessionSelection = computed(() => explicitSessionSelection.value)




















  async function ensureSessionSummary(targetBotId: string, targetSessionId: string, requestId?: number): Promise<SessionSummary | null> {
    const bid = targetBotId.trim()
    const sid = targetSessionId.trim()
    if (!bid || !sid) return null
    const known = knownSessionSummary(sid)
    if (known) return known

    try {
      const fetched = await fetchSession(bid, sid)
      if (requestId !== undefined && requestId !== selectSessionRequestId) return null
      if ((currentBotId.value ?? '').trim() !== bid || (sessionId.value ?? '').trim() !== sid) return null
      rememberSession(fetched)
      return fetched
    } catch {
      return null
    }
  }

  function ensureVisibleSessionSummary(targetBotId: string, targetSessionId: string): Promise<SessionSummary | null> {
    const bid = targetBotId.trim()
    const sid = targetSessionId.trim()
    if (!bid || !sid) return Promise.resolve(null)
    const known = knownSessionSummary(sid)
    if (known) return Promise.resolve(known)
    const key = `${bid}\u0000${sid}`
    const pending = visibleSessionSummaryRequests.get(key)
    if (pending) return pending
    const generation = userScopeGeneration
    const request = (async () => {
      try {
        const fetched = await fetchSession(bid, sid)
        if (generation !== userScopeGeneration || (currentBotId.value ?? '').trim() !== bid) return null
        rememberSession(fetched)
        return fetched
      } catch {
        return null
      } finally {
        if (visibleSessionSummaryRequests.get(key) === request) {
          visibleSessionSummaryRequests.delete(key)
        }
      }
    })()
    visibleSessionSummaryRequests.set(key, request)
    return request
  }




  async function cleanupFailedDeferredSession(botId: string, targetSessionId: string, fallbackComposerScope = '') {
    const bid = botId.trim()
    const sid = targetSessionId.trim()
    if (!bid || !sid) return

    const rescopedComposerScope = rescopeSessionCommandEventToComposer(bid, sid)
    const composerScope = rescopedComposerScope || fallbackComposerScope.trim()
    markSessionDeleted(bid, sid)
    const deletedSignal: { id: string, botId: string, seq: number, composerScope?: string } = { id: sid, botId: bid, seq: ++deletedSessionSeq }
    if (composerScope) deletedSignal.composerScope = composerScope
    deletedSession.value = deletedSignal
    clearACPRuntimeStatus(bid, sid)
    stopSessionMessagesStream(bid, sid)
    chatViews.removeSession(bid, sid)
    if ((currentBotId.value ?? '').trim() === bid) {
      removeSessionFromList(sid)
      if ((sessionId.value ?? '').trim() === sid) {
        sessionId.value = null
        explicitSessionSelection.value = false
        draftIntent.value = true
        clearHistoryView()
      }
    }

    try {
      await requestDeleteSession(bid, sid)
    } catch {
      // Best-effort cleanup: the send failure result is the user-facing error.
    }
  }





  // Pending-ACP session staging (see ./chat/acp-staging for the generation /
  // identity-key model). Transcript and select-session invalidation are
  // injected so the staging machine never touches store internals directly.
  const acpStaging = createACPStaging({
    currentBotId,
    sessionId,
    draftIntent,
    explicitSessionSelection,
    runtimeRegistry: acpRuntimeRegistry,
    bumpSelectSessionRequest: () => {
      selectSessionRequestId++
    },
    clearTranscriptForDraft: () => {
      const bid = (currentBotId.value ?? '').trim()
      const sid = (sessionId.value ?? '').trim()
      if (bid && sid) stopSessionMessagesStream(bid, sid)
      clearHistoryView()
    },
  })
  const {
    pendingACPSessionInput,
    pendingACPRuntimeId,
    pendingACPSessionMetadata,
    pendingACPRuntimeStatus,
    pendingACPRuntimeEnsuring,
    rememberDefaultACPInput,
    cachedDefaultACPInput,
    cacheDefaultACPSession,
    stageACPSession: stageFocusedACPSession,
    stageDefaultACPSession: stageFocusedDefaultACPSession,
    stageNewACPSession: stageFocusedNewACPSession,
    resetToEmptyComposer: resetFocusedEmptyComposer,
    ensurePendingACPRuntime: ensureFocusedPendingACPRuntime,
    setPendingACPModel: setFocusedPendingACPModel,
    setPendingACPReasoning: setFocusedPendingACPReasoning,
    clearPendingACPSession,
    detachPendingACPSession,
    restorePendingACPSession,
    releasePendingACPSession,
    discardDetachedACPSession,
    pendingACPMatchesInput: focusedPendingACPMatchesInput,
  } = acpStaging

  interface DraftACPStage extends DetachedACPSession {
    viewId: string
  }

  const draftACPStages = ref<Record<string, DraftACPStage>>({})
  let liveDraftACP: { botId: string, viewId: string } | null = null

  function draftACPStageKey(botId: string, viewId: string) {
    return `${botId.trim()}\u0000${viewId.trim()}`
  }

  function sameDraftACPStage(left: { botId: string, viewId: string } | null, right: ChatViewTarget) {
    return !!left
      && left.botId === right.botId.trim()
      && left.viewId === right.viewId.trim()
      && !right.sessionId
  }

  function rememberDraftACPStage(target: Pick<ChatViewTarget, 'botId' | 'viewId'>, detached: DetachedACPSession) {
    const key = draftACPStageKey(target.botId, target.viewId)
    draftACPStages.value = {
      ...draftACPStages.value,
      [key]: {
        botId: detached.botId.trim() || target.botId.trim(),
        viewId: target.viewId.trim(),
        input: { ...detached.input },
        runtimeId: detached.runtimeId.trim(),
      },
    }
  }

  function syncLiveDraftACPStage() {
    if (!liveDraftACP || !pendingACPSessionInput.value) return
    rememberDraftACPStage(liveDraftACP, {
      botId: liveDraftACP.botId,
      input: pendingACPSessionInput.value,
      runtimeId: pendingACPRuntimeId.value,
    })
  }

  function saveLiveDraftACPStage() {
    if (!liveDraftACP) return
    const owner = liveDraftACP
    const detached = detachPendingACPSession()
    if (detached) rememberDraftACPStage(owner, detached)
    liveDraftACP = null
  }

  function activateDraftACPStage(target: ChatViewTarget) {
    const resolved = normalizedChatViewTarget(target)
    if (resolved.sessionId || !resolved.botId || !resolved.viewId) return
    if (sameDraftACPStage(liveDraftACP, resolved)) return
    saveLiveDraftACPStage()
    liveDraftACP = { botId: resolved.botId, viewId: resolved.viewId }
    const saved = draftACPStages.value[draftACPStageKey(resolved.botId, resolved.viewId)]
    if (saved) {
      restorePendingACPSession(saved.input, saved.runtimeId, saved.botId)
    } else {
      releasePendingACPSession()
    }
  }

  function forgetDraftACPStage(target: ChatViewTarget) {
    const resolved = normalizedChatViewTarget(target)
    const key = draftACPStageKey(resolved.botId, resolved.viewId)
    if (sameDraftACPStage(liveDraftACP, resolved)) {
      releasePendingACPSession()
      liveDraftACP = null
    }
    if (!(key in draftACPStages.value)) return
    const { [key]: _removed, ...rest } = draftACPStages.value
    draftACPStages.value = rest
  }

  function discardDraftACPStage(target: ChatViewTarget) {
    const resolved = normalizedChatViewTarget(target)
    const key = draftACPStageKey(resolved.botId, resolved.viewId)
    if (sameDraftACPStage(liveDraftACP, resolved)) {
      clearPendingACPSession()
      liveDraftACP = null
    } else {
      const saved = draftACPStages.value[key]
      if (saved) discardDetachedACPSession(saved)
    }
    if (!(key in draftACPStages.value)) return
    const { [key]: _removed, ...rest } = draftACPStages.value
    draftACPStages.value = rest
  }

  discardEvictedDraftStage = (view) => {
    draftViewCommandVersions.delete(draftSessionCreationKey({
      botId: view.botId,
      sessionId: null,
      viewId: view.viewId,
    }))
    discardDraftACPStage({ botId: view.botId, sessionId: null, viewId: view.viewId })
  }

  function pendingACPStateFor(target: ChatViewTarget) {
    const resolved = normalizedChatViewTarget(target)
    if (resolved.sessionId) return null
    const live = sameDraftACPStage(liveDraftACP, resolved)
    const saved = live && pendingACPSessionInput.value
      ? {
          botId: liveDraftACP!.botId,
          viewId: liveDraftACP!.viewId,
          input: pendingACPSessionInput.value,
          runtimeId: pendingACPRuntimeId.value,
        }
      : draftACPStages.value[draftACPStageKey(resolved.botId, resolved.viewId)]
    if (!saved) return null
    const runtimeKey = acpRuntimeKey(saved.botId, saved.runtimeId)
    return {
      input: { ...saved.input },
      metadata: acpSessionMetadata(saved.input),
      runtimeId: saved.runtimeId,
      runtimeStatus: runtimeKey ? acpRuntimeStatuses.value[runtimeKey] : undefined,
      ensuring: live ? pendingACPRuntimeEnsuring.value : false,
    }
  }

  function targetDraftForACP(target?: ChatViewTarget): ChatViewTarget {
    const resolved = normalizedChatViewTarget(target)
    return { ...resolved, sessionId: null }
  }

  function stageACPSession(
    input: ACPAgentSessionInput,
    options: { explicitSelection?: boolean } = {},
    target?: ChatViewTarget,
  ) {
    const draft = targetDraftForACP(target)
    invalidateDraftViewCommand(draft)
    activateDraftACPStage(draft)
    stageFocusedACPSession(input, options)
    syncLiveDraftACPStage()
  }

  function stageDefaultACPSession(input: ACPAgentSessionInput, target?: ChatViewTarget) {
    const draft = targetDraftForACP(target)
    invalidateDraftViewCommand(draft)
    activateDraftACPStage(draft)
    stageFocusedDefaultACPSession(input)
    syncLiveDraftACPStage()
  }

  function stageNewACPSession(input: ACPAgentSessionInput, target?: ChatViewTarget) {
    const draft = targetDraftForACP(target)
    invalidateDraftViewCommand(draft)
    activateDraftACPStage(draft)
    stageFocusedNewACPSession(input)
    syncLiveDraftACPStage()
  }

  function resetToEmptyComposer(
    options: { clearPendingACP?: boolean, explicitSelection?: boolean, draftIntent?: boolean } = {},
    target?: ChatViewTarget,
  ) {
    const draft = targetDraftForACP(target)
    resetWorkspaceTargetSelection(draft)
    invalidateDraftViewCommand(draft)
    activateDraftACPStage(draft)
    resetFocusedEmptyComposer(options)
    if (options.clearPendingACP !== false) forgetDraftACPStage(draft)
  }

  async function ensurePendingACPRuntime(target?: ChatViewTarget) {
    const draft = targetDraftForACP(target)
    activateDraftACPStage(draft)
    try {
      return await ensureFocusedPendingACPRuntime()
    } finally {
      syncLiveDraftACPStage()
    }
  }

  async function setPendingACPModel(modelId: string, target?: ChatViewTarget) {
    const draft = targetDraftForACP(target)
    invalidateDraftViewCommand(draft)
    activateDraftACPStage(draft)
    try {
      return await setFocusedPendingACPModel(modelId)
    } finally {
      syncLiveDraftACPStage()
    }
  }

  async function setPendingACPReasoning(effort: string, target?: ChatViewTarget) {
    const draft = targetDraftForACP(target)
    invalidateDraftViewCommand(draft)
    activateDraftACPStage(draft)
    try {
      return await setFocusedPendingACPReasoning(effort)
    } finally {
      syncLiveDraftACPStage()
    }
  }

  function pendingACPMatchesInput(input: ACPAgentSessionInput, target?: ChatViewTarget) {
    if (!target) return focusedPendingACPMatchesInput(input)
    const state = pendingACPStateFor(target)
    if (!state) return false
    const metadata = acpSessionMetadata(input)
    return state.metadata.acp_agent_id === metadata.acp_agent_id
      && state.metadata.project_path === metadata.project_path
      && state.metadata.acp_project_mode === metadata.acp_project_mode
  }

  watch(currentBotId, (newId) => {
    if (newId) {
      void initialize()
    } else {
      resetUserScopedState()
    }
  }, { immediate: true })

  onAuthSessionCleared(() => resetUserScopedState({ clearSelection: true }))



  function refreshLoadingForSession(botId: string, targetSessionId: string) {
    if (!isActiveSessionTarget(botId, targetSessionId)) return
    loading.value = isSessionStreaming(botId, targetSessionId)
  }

  function isActiveSessionStreaming() {
    return isSessionStreaming(currentBotId.value, sessionId.value)
  }


  function ensureDiscussStream(invocationId: string, targetSessionId: string, targetBotId: string) {
    const id = invocationIdForEvent(targetBotId, { invocation_id: invocationId, session_id: targetSessionId }, targetSessionId)
    const existing = getAssistantStream(id)
    if (existing) return existing
    if (isTerminalInvocation(id)) return null
    const sid = targetSessionId.trim()
    const bid = targetBotId.trim()
    const assistantTurn = createOptimisticAssistantTurn()
    appendTurnToSession(bid, sid, assistantTurn)
    void trackAssistantStream({ invocationId: id, assistantTurn, botId: bid, sessionId: sid }).catch((error: Error) => {
      finalizeStreamFailure(assistantTurn, bid, sid, error)
    })
    return getAssistantStream(id)!
  }


  // session_created precedes the run, so it is addressed by the invocation.
  function handleWSSessionCreated(event: { invocation_id?: string; session_id: string }, sourceBotId = '') {
    const eventSessionId = event.session_id.trim()
    if (isTerminalInvocation(event.invocation_id) || isTerminalApprovalResponse(event.invocation_id)) return
    const pending = event.invocation_id ? getAssistantStream(event.invocation_id) : undefined
    const bid = (pending?.botId || sourceBotId || currentBotId.value || '').trim()
    if (!bid || !eventSessionId) return
    const sid = recordCreatedSession(event.invocation_id, eventSessionId) || eventSessionId
    const viewId = pending?.viewId?.trim() || focusedChatViewId.value
    const promoted = promoteDraftChatView({ botId: bid, sessionId: null, viewId }, sid)
    if ((currentBotId.value ?? '').trim() !== bid) return

    const now = new Date().toISOString()
    if (!knownSessionSummary(sid)) {
      upsertSession({
        id: sid,
        bot_id: bid,
        type: 'chat',
        session_mode: 'chat',
        runtime_type: 'model',
        title: provisionalSessionTitle(promoted.transcript.latestOptimisticUserText()),
        created_at: now,
        updated_at: now,
      })
    }
    userSentInSession.value = {
      id: sid,
      botId: bid,
      viewId,
      wasDraft: true,
      seq: ++userSendSeq,
    }
    if (focusedChatViewId.value !== viewId) return
    if (sessionId.value && sessionId.value !== sid) return
    sessionId.value = sid
    explicitSessionSelection.value = true
    draftIntent.value = false
  }

  function rememberStartupSendFailure(failure: Omit<StartupSendFailure, 'id'>) {
    const stored: StartupSendFailure = {
      ...failure,
      id: nextId(),
      restoreAttachments: failure.restoreAttachments ? [...failure.restoreAttachments] : undefined,
      restoreRequestedSkills: failure.restoreRequestedSkills ? failure.restoreRequestedSkills.map(skill => ({ ...skill })) : undefined,
    }
    const key = startupSendFailureKey(failure.botId, failure.sessionId, failure.composerScope)
    startupSendFailures.value = { ...startupSendFailures.value, [key]: stored }
  }

  function clearStartupSendFailure(id?: string) {
    if (!id) {
      startupSendFailures.value = {}
      return
    }
    const next = { ...startupSendFailures.value }
    for (const [key, failure] of Object.entries(next)) {
      if (failure.id === id) delete next[key]
    }
    startupSendFailures.value = next
  }

  function pruneEmptyAssistantTurnIfPending(invocationId: string) {
    const session = getAssistantStream(invocationId)
    if (!session) return
    const turn = session.assistantTurn
    if (turn.messages.length > 0) return
    removeTurnFromSession(session.botId, session.sessionId, turn)
  }

  function handleExpiredApprovalResponse(response: ApprovalResponse) {
    abortRun(response.invocationId)
    const stream = getAssistantStream(response.invocationId)
    if (stream) {
      const turn = stream.assistantTurn
      discardAssistantStream(response.invocationId)
      if (turn.messages.length === 0) {
        removeTurnFromSession(response.botId, response.sessionId, turn)
      }
    }
    refreshLoadingForSession(response.botId, response.sessionId)
  }

  function handleWSStreamEvent(event: UIStreamEvent, targetSessionId?: string, sourceBotId = '') {
    if (event.type === 'session_created') {
      handleWSSessionCreated(event, sourceBotId)
      return
    }
    // run_accepted is the first event of every turn and the only place the
    // server's name for it arrives. A stop pressed before this point had no run
    // to address, so it is replayed here.
    if (event.type === 'run_accepted') {
      const accepted = bindRunId(event.invocation_id, event.run_id)
      if (accepted?.abortRequested) {
        abortWebSocketRun(accepted.runId, accepted.botId || sourceBotId)
      }
      return
    }
    // A rejected submission never became a run, so it is answered by invocation.
    // The event carries a stable code, which is what callers branch on: one of
    // them is worth retrying unchanged and the other never is.
    if (event.type === 'run_rejected') {
      const invocation = event.invocation_id?.trim() ?? ''
      if (!invocation) return
      const rejected = getAssistantStream(invocation)
      // A silent turn has no stream to fail, but it still holds the optimistic
      // decision that has to roll back.
      settleApprovalResponse(invocation, 'failed')
      if (!rejected) return
      const message = resolveApiErrorMessage(event, event.message || sendFailedMessage())
      const stage: SendMessageStage = hasVisibleAssistantBlocks(rejected.assistantTurn) ? 'stream' : 'startup'
      pruneEmptyAssistantTurnIfPending(invocation)
      rejectAssistantStream(invocation, new StreamFailureError(message, stage, event))
      loading.value = isActiveSessionStreaming()
      return
    }
    if (event.type === 'user_message') {
      const sid = (event.session_id ?? targetSessionId ?? sessionId.value ?? '').trim()
      const bid = sourceBotId || currentBotId.value || ''
      const invocationId = invocationIdForEvent(bid, event, sid)
      if (isTerminalInvocation(invocationId) || isTerminalApprovalResponse(invocationId)) return
      appendTurnToSession(bid, sid, normalizeTurn(event.data))
      const pending = getAssistantStream(invocationId)
      if (pending && !hasTurn(pending.assistantTurn)) {
        appendTurnToSession(bid || pending.botId, sid || pending.sessionId, pending.assistantTurn)
      }
      return
    }
    if (event.type === 'command_result' || event.type === 'command_error') {
      const commandInvocationId = event.invocation_id?.trim() ?? ''
      const pending = commandInvocationId ? getAssistantStream(commandInvocationId) : undefined
      rememberCommandEvent(event, {
        botId: pending?.botId || sourceBotId,
        sessionId: event.session_id || pending?.sessionId || targetSessionId,
        composerScope: pending?.composerScope || event.composer_scope,
      })
      if (event.type === 'command_error' && commandInvocationId) {
        if (pending) {
          const message = event.error?.message || 'slash command failed'
          rejectAssistantStream(commandInvocationId, new CommandStreamError(message))
          loading.value = isActiveSessionStreaming()
        }
      }
      return
    }

    const sid = (event.session_id ?? targetSessionId ?? sessionId.value ?? '').trim()
    const bid = sourceBotId || currentBotId.value || ''
    const invocationId = invocationIdForEvent(bid, event, sid)
    // The server may emit end after error. It must not recreate the stream, but
    // it still triggers the final authoritative refresh below.
    if ((isTerminalInvocation(invocationId) || isTerminalApprovalResponse(invocationId)) && event.type !== 'end') return

    if (getApprovalResponse(invocationId)?.silent) {
      if (event.type === 'end' || event.type === 'error') {
        if (event.type === 'error') {
          settleApprovalResponse(invocationId, 'failed')
          toast.error(resolveApiErrorMessage(event, event.message || 'tool approval failed'))
        } else {
          settleApprovalResponse(invocationId, 'succeeded')
        }
        loading.value = isActiveSessionStreaming()
      }
      return
    }

    switch (event.type) {
      case 'start':
        ensureDiscussStream(invocationId, sid, bid)
        break
      case 'message':
        if (event.data.type === 'tool' && event.data.running && isGuiToolName(event.data.name)) {
          const toolCallId = event.data.tool_call_id?.trim() ?? ''
          const dedupeKey = `${bid}:${sid}:${toolCallId || `${invocationId}:${event.data.id}:${event.data.name}`}`
          if (!seenGuiToolCalls.has(dedupeKey)) {
            seenGuiToolCalls.add(dedupeKey)
            guiToolUseRequested.value = {
              botId: bid,
              sessionId: sid,
              toolCallId,
              toolName: event.data.name,
              seq: ++guiToolUseRequestSeq,
            }
          }
        }
        const messageStream = ensureDiscussStream(invocationId, sid, bid)
        if (messageStream) {
          upsertAssistantUIMessage(
            messageStream.assistantTurn,
            mapAssistantStreamMessage(invocationId, event.data),
          )
        }
        break
      case 'end':
        const endedSession = getAssistantStream(invocationId)
        const endedBotId = endedSession?.botId ?? currentBotId.value ?? ''
        const endedSessionId = (endedSession?.sessionId || sid || '').trim()
        settleApprovalResponse(invocationId, 'succeeded')
        pruneEmptyAssistantTurnIfPending(invocationId)
        resolveAssistantStream(invocationId)
        loading.value = isActiveSessionStreaming()
        if (endedSessionId && !isSessionStreaming(endedBotId, endedSessionId)) {
          const endedView = chatViews.getSession(endedBotId, endedSessionId)
          if (endedView) {
            void refreshCurrentSession(endedBotId, endedSessionId)
              .finally(() => releaseHiddenSessionView(endedView))
          } else {
            touchSessionInList(endedSessionId, new Date().toISOString())
          }
        }
        break
      case 'error': {
        const session = getAssistantStream(invocationId) ?? ensureDiscussStream(invocationId, sid, bid)
        if (!session) break
        const message = resolveApiErrorMessage(event, event.message || 'stream error')
        const stage: SendMessageStage = hasVisibleAssistantBlocks(session.assistantTurn) ? 'stream' : 'startup'
        settleApprovalResponse(invocationId, 'failed')
        rejectAssistantStream(invocationId, new StreamFailureError(message, stage, event.feedback ?? event))
        loading.value = isActiveSessionStreaming()
        releaseHiddenSessionView(chatViews.getSession(session.botId, session.sessionId) ?? null)
        break
      }
    }
  }

  function resetUserScopedState(options: { clearSelection?: boolean } = {}) {
    userScopeGeneration += 1
    stopStreams()
    abortAllAssistantStreams()
    stopWebSocket()
    clearPendingACPSession()

    resetRefreshCoordinator()

    replaceSessions([])
    clearDeletedSessionIds()
    sessionsCursor.value = null
    hasMoreSessions.value = false
    loadingMoreSessions.value = false
    bots.value = []
    sessionId.value = null
    explicitSessionSelection.value = false
    if (options.clearSelection && currentBotId.value) {
      currentBotId.value = null
    }
    resetTranscriptUserScope()
    draftACPStages.value = {}
    liveDraftACP = null
    loading.value = false
    loadingChats.value = false
    initializing.value = false
    initializeRerunRequested = false
    initializingBotId = null
    initializePromise = null
    overrideModelId.value = ''
    overrideReasoningEffort.value = ''
    startupSendFailures.value = {}
    draftViewRequested.value = null
    forkedSessionRequested.value = null
    draftViewCommandVersions.clear()
    visibleSessionSummaryRequests.clear()
    draftSessionCreations.clear()
    resetCommandEvents()
    resetFsBeacon()
    resetACPRuntimeRegistry()

    clearStreamHistory()
    resetApprovalResponses()
    forkingMessages.clear()
    backgroundTasks.clearBackgroundTasks()
    seenGuiToolCalls.clear()
    guiToolUseRequested.value = null
  }

  async function loadMoreSessions(): Promise<void> {
    if (!hasMoreSessions.value || loadingMoreSessions.value) return
    const bid = (currentBotId.value ?? '').trim()
    const cursor = sessionsCursor.value
    if (!bid || !cursor) return
    loadingMoreSessions.value = true
    try {
      const response = await fetchSessions(bid, { cursor })
      if ((currentBotId.value ?? '').trim() !== bid) return
      appendSessions(response.items)
      sessionsCursor.value = response.nextCursor
      hasMoreSessions.value = response.nextCursor !== null
    } catch (error) {
      console.error('Failed to load more sessions:', error)
    } finally {
      loadingMoreSessions.value = false
    }
  }

  function handleSessionMessageEvent(targetBotId: string, targetSessionId: string, event: SessionMessageStreamEvent) {
    if (event.type === 'ping') return
    if (event.type === 'dropped') {
      void refreshCurrentSession(targetBotId, targetSessionId)
      return
    }

    if (event.type === 'background_task') {
      const eventSessionId = event.session_id?.trim()
      if (eventSessionId && eventSessionId !== targetSessionId) return
      const task = normalizeBackgroundTask(event, event.type)
      if (!task) return
      const view = chatViews.getSession(targetBotId, targetSessionId)
      if (view) {
        backgroundTasks.mergeBackgroundTaskIntoMatchingTools(
          rememberBackgroundTask(task),
          view.transcript.messages,
        )
      }
      if (eventSessionId) touchSessionInList(eventSessionId)
      return
    }

    if (event.type === 'session_title_updated') {
      const sid = event.session_id.trim()
      const title = event.title.trim()
      if (!sid || !title) return
      updateKnownSessionTitle(sid, title)
      return
    }

    // message_created. Per-session SSE delivers raw messages; the server's
    // backlog handshake (per-stream backlogIDs) ensures the client sees only
    // genuinely live events post-backlog, so we don't try to dedup against
    // already-known message ids — the comparison was unsound anyway because
    // `messages` holds aggregated UI turns whose ids live in a different
    // namespace from raw bot_history_messages.id. The downstream
    // The keyed session refresh is debounced and idempotent, so an
    // occasional redundant REST round trip is cheap.
    const raw = event.message
    if (!raw) return
    const messageSessionId = String(raw.session_id ?? '').trim()
    if (messageSessionId && messageSessionId !== targetSessionId) return
    if (messageSessionId) touchSessionInList(messageSessionId, raw.created_at)
    const sid = messageSessionId || targetSessionId
    if (!shouldRefreshFromMessageCreated(
      targetBotId,
      sid,
      isSessionStreaming(targetBotId, sid) ? sid : null,
      event,
    )) return
    scheduleSessionRefresh(targetBotId, sid)
  }

  function handleBotSessionsActivityEvent(targetBotId: string, event: BotSessionActivityEvent) {
    if (event.type === 'ping') return
    if (event.type === 'dropped') {
      void refreshSessionsList(targetBotId)
      return
    }

    if (event.type === 'session_touched') {
      const sid = event.session_id.trim()
      if (!sid) return
      const touched = touchKnownSession(sid, event.updated_at)
      if (touched.source === 'listed') return
      if (touched.source === 'remembered') {
        if (touched.visibleInRecents) void refreshSessionsList(targetBotId)
        return
      }
      // Unknown session — likely created from another channel. Reload the
      // first page so it shows up in the sidebar.
      void refreshSessionsList(targetBotId)
      return
    }

    if (event.type === 'session_title_changed') {
      const sid = event.session_id.trim()
      const title = event.title.trim()
      if (!sid || !title) return
      updateKnownSessionTitle(sid, title)
      return
    }

    // session_created — server filters to user-facing types, but emits only
    // `session_id` / `title` / `created_at` (no session type, no metadata).
    // A stub with `type: undefined` would fail every consumer that branches
    // on session.type, so reload the first page instead and let the server
    // return the full summary.
    const sid = event.session_id.trim()
    if (!sid || hasListedSession(sid)) return
    void refreshSessionsList(targetBotId)
  }

  async function prepareSessionMessages(targetBotId: string, targetSessionId: string) {
    const bid = targetBotId.trim()
    const sid = targetSessionId.trim()
    if (!bid || !sid) return

    // The chat pane reads `loadingMessages` to suppress empty-state
    // placeholders (e.g. "system session has no records") while a fresh
    // transcript is on its way. The sidebar deliberately ignores it — only
    // `loadingChats` (sessions-list boot) makes the sidebar spin.
    try {
      await loadInitialMessages(bid, sid)
    } finally {
      for (const stream of assistantStreamsForSession(bid, sid)) {
        reattachTurnToSession(bid, sid, stream.assistantTurn)
      }
    }
  }

  function abort(target?: ChatViewTarget) {
    const resolved = normalizedChatViewTarget(target)
    const abortError = new Error('aborted')
    abortError.name = 'AbortError'
    const approvalInvocationIds = abortApprovalResponses(
      pendingApprovalResponsesForSession(resolved.botId, resolved.sessionId ?? ''),
      'failed',
    )
    const invocationIds = resolved.sessionId
      ? assistantStreamsForSession(resolved.botId, resolved.sessionId).map(stream => stream.invocationId)
      : activeUnboundInvocationIds(
          resolved.botId,
          target ? `${resolved.botId}:${resolved.viewId}` : undefined,
        )
    for (const invocationId of invocationIds) {
      if (!approvalInvocationIds.has(invocationId)) {
        abortRun(invocationId)
      }
      rejectAssistantStream(invocationId, abortError)
    }
    loading.value = isActiveSessionStreaming()
    chatViews.prune()
  }

  // Stopping addresses the server's run. Before run_accepted there is no such
  // name, so the request is recorded and replayed the moment one arrives.
  function abortRun(invocationId: string) {
    const runId = requestAbort(invocationId)
    if (!runId) return
    abortWebSocketRun(runId, getAssistantStream(invocationId)?.botId)
  }

  function abortApprovalResponses(responses: ApprovalResponse[], outcome: ApprovalResponseOutcome): Set<string> {
    const invocationIds = new Set<string>()
    for (const response of responses) {
      invocationIds.add(response.invocationId)
      abortRun(response.invocationId)
      settleApprovalResponse(response.invocationId, outcome)
    }
    return invocationIds
  }

  function abortAllAssistantStreams() {
    const abortError = new Error('aborted')
    abortError.name = 'AbortError'
    const approvalInvocationIds = abortApprovalResponses(pendingApprovalResponses(), 'canceled')
    rejectAllStreams(abortError, (invocationId) => {
      if (!approvalInvocationIds.has(invocationId)) {
        abortRun(invocationId)
      }
    })
    loading.value = false
  }

  async function ensureBot(): Promise<string | null> {
    const generation = userScopeGeneration
    try {
      const list = await fetchBots()
      if (generation !== userScopeGeneration) return null
      bots.value = list
      if (!list.length) {
        currentBotId.value = null
        return null
      }
      if (currentBotId.value) {
        const found = list.find(bot => bot.id === currentBotId.value)
        if (found && !isPendingBot(found)) return currentBotId.value
      }
      const ready = list.find(bot => !isPendingBot(bot))
      currentBotId.value = ready ? ready.id : list[0]!.id
      return currentBotId.value
    } catch (error) {
      if (generation !== userScopeGeneration) return null
      console.error('Failed to fetch bots:', error)
      return currentBotId.value
    }
  }

  // Re-pull the bot list without touching the current bot/session selection.
  // The store loads bots once at init and isn't wired to the settings pages'
  // query cache, so per-bot config edited in settings (enabled agents, model,
  // name…) would otherwise stay stale in the composer until a full reload.
  // currentBot is a computed over bots, so swapping the list reactively
  // refreshes the composer's agent list and metadata in place.
  async function refreshBots(): Promise<void> {
    const generation = userScopeGeneration
    try {
      const list = await fetchBots()
      if (generation !== userScopeGeneration) return
      bots.value = list
    } catch (error) {
      if (generation !== userScopeGeneration) return
      console.error('Failed to refresh bots:', error)
    }
  }


  function sessionMetadata(session: SessionSummary | null): Record<string, unknown> {
    if (!session) return {}
    return {
      ...(session.metadata && typeof session.metadata === 'object' ? session.metadata : {}),
      ...(session.runtime_metadata && typeof session.runtime_metadata === 'object' ? session.runtime_metadata : {}),
    }
  }

  function chatTargetFor(target?: ChatViewTarget): ActiveChatTarget {
    const resolved = normalizedChatViewTarget(target)
    const focused = resolved.viewId === focusedChatViewId.value
      && resolved.botId === (currentBotId.value ?? '').trim()
    const explicitSelection = focused ? explicitSessionSelection.value : false
    const sid = (resolved.sessionId ?? '').trim()
    if (sid) {
      const session = knownSessionSummary(sid)
      const runtimeType = session ? normalizedRuntimeType(session) : 'unknown'
      return {
        kind: 'session',
        sessionId: sid,
        session,
        runtimeType,
        isACP: runtimeType === 'acp_agent',
        isPendingACP: false,
        metadata: sessionMetadata(session),
        explicitSelection,
      }
    }

    const pendingState = pendingACPStateFor(resolved)
    if (pendingState) {
      return {
        kind: 'draft-acp',
        sessionId: null,
        session: null,
        runtimeType: 'acp_agent',
        isACP: true,
        isPendingACP: true,
        metadata: pendingState.metadata,
        explicitSelection,
      }
    }

    return {
      kind: 'draft-native',
      sessionId: null,
      session: null,
      runtimeType: 'model',
      isACP: false,
      isPendingACP: false,
      metadata: {},
      explicitSelection,
    }
  }

  const activeChatTarget = computed<ActiveChatTarget>(() => chatTargetFor())

  function chatReadOnlyFor(target: ChatViewTarget): boolean {
    const resolved = normalizedChatViewTarget(target)
    const session = chatTargetFor(resolved).session
    if (!session) return Boolean(resolved.sessionId)
    const type = session.type ?? 'chat'
    if (type === 'heartbeat' || type === 'schedule' || type === 'subagent') return true
    const channelType = (session.channel_type ?? '').trim().toLowerCase()
    return Boolean(channelType && channelType !== 'local')
  }

  function chatCanForkFor(target: ChatViewTarget): boolean {
    return chatTargetFor(target).session?.type === 'chat'
  }























  async function createACPSessionRecord(
    botId: string,
    input: ACPAgentSessionInput,
  ): Promise<SessionSummary> {
    const bid = botId.trim()
    if (!bid) throw new Error('Bot not ready')
    const metadata = acpSessionMetadata(input)
    const runtimeId = input.runtimeId?.trim() ?? ''
    const sessionMode = input.sessionMode === 'discuss' ? 'discuss' : 'chat'
    // The warm staged runtime is bound server-side inside session creation;
    // no separate adopt/bind round trip and nothing for a watcher to race.
    return createSession(bid, {
      title: input.title ?? '',
      type: sessionMode,
      sessionMode,
      runtimeType: 'acp_agent',
      metadata: {},
      runtimeMetadata: metadata,
      acpRuntimeId: runtimeId || undefined,
    })
  }

  async function createACPSessionForTarget(
    input: ACPAgentSessionInput,
    target: ChatViewTarget,
  ): Promise<{ session: SessionSummary }> {
    const draft = targetDraftForACP(target)
    const generation = userScopeGeneration
    const stagedBeforeCreate = pendingACPStateFor(draft)
    const runtimeId = input.runtimeId?.trim() ?? ''
    const created = await createACPSessionRecord(draft.botId, input)
    const assertCurrentScope = () => {
      if (
        generation === userScopeGeneration
        && (currentBotId.value ?? '').trim() === draft.botId
      ) return
      const error = new Error('Chat scope changed during ACP Session creation')
      error.name = 'AbortError'
      throw error
    }

    try {
      assertCurrentScope()
    } catch (error) {
      await rollbackFailedACPSessionCreation(created, draft, stagedBeforeCreate?.input ?? input, runtimeId, generation)
      throw error
    }

    upsertSession(created)
    rememberSession(created)
    if (runtimeId) clearACPRuntimeStatus(draft.botId, runtimeId)
    promoteDraftChatView(draft, created.id)
    if (stagedBeforeCreate) {
      if (runtimeId && stagedBeforeCreate.runtimeId === runtimeId) {
        forgetDraftACPStage(draft)
      } else {
        discardDraftACPStage(draft)
      }
    }
    if (isFocusedChatTarget(draft)) {
      sessionId.value = created.id
      explicitSessionSelection.value = true
      draftIntent.value = false
    }
    return { session: created }
  }

  async function rollbackFailedACPSessionCreation(
    created: SessionSummary,
    draft: ChatViewTarget,
    stagedInput: ACPAgentSessionInput,
    stagedRuntimeId: string,
    generation: number,
  ) {
    if (generation !== userScopeGeneration) return

    markSessionDeleted(draft.botId, created.id)
    stopSessionMessagesStream(draft.botId, created.id)
    chatViews.removeSession(draft.botId, created.id)
    clearACPRuntimeStatus(draft.botId, created.id)
    if (stagedRuntimeId) clearACPRuntimeStatus(draft.botId, stagedRuntimeId)
    if ((currentBotId.value ?? '').trim() === draft.botId) {
      removeSessionFromList(created.id)
    }

    if (sameDraftACPStage(liveDraftACP, draft)) {
      releasePendingACPSession()
      liveDraftACP = null
    }
    rememberDraftACPStage(draft, {
      botId: draft.botId,
      input: normalizedACPInput({ ...stagedInput, runtimeId: undefined }),
      runtimeId: '',
    })
    if (isFocusedChatTarget(draft)) activateDraftACPStage(draft)

    try {
      await requestDeleteSession(draft.botId, created.id)
    } catch {
      // The tombstone keeps a failed cleanup out of this client until auth reset.
    }
  }

  async function createACPSession(input: ACPAgentSessionInput): Promise<{ session: SessionSummary }> {
    const bid = currentBotId.value ?? await ensureBot()
    if (!bid) throw new Error('Bot not ready')
    return createACPSessionForTarget(input, {
      botId: bid,
      sessionId: null,
      viewId: focusedChatViewId.value,
    })
  }

  async function updateCurrentSessionAgent(
    input: ACPAgentSessionInput,
    target?: ChatViewTarget,
  ): Promise<{ session: SessionSummary }> {
    const resolved = normalizedChatViewTarget(target)
    if (!resolved.sessionId) return createACPSessionForTarget(input, resolved)
    const bid = resolved.botId
    const sid = resolved.sessionId
    if (!bid) throw new Error('Bot not selected')
    const metadata = acpSessionMetadata(input)
    const targetSession = knownSessionSummary(sid)
    const sessionMode = targetSession?.session_mode || (targetSession?.type === 'discuss' ? 'discuss' : 'chat')
    const generation = userScopeGeneration
    const updated = await requestUpdateSessionAgent(bid, sid, {
      type: sessionMode === 'discuss' ? 'discuss' : 'acp_agent',
      sessionMode,
      runtimeType: 'acp_agent',
      metadata,
      runtimeMetadata: metadata,
    })
    if (generation !== userScopeGeneration || (currentBotId.value ?? '').trim() !== bid) return { session: updated }
    upsertSession(updated)
    if (isFocusedChatTarget(resolved)) {
      explicitSessionSelection.value = true
      draftIntent.value = false
    }
    clearACPRuntimeStatus(bid, sid)
    return { session: updated }
  }

  async function updateCurrentSessionToMemoh(target?: ChatViewTarget): Promise<SessionSummary | null> {
    const resolved = normalizedChatViewTarget(target)
    const bid = resolved.botId
    const sid = resolved.sessionId ?? ''
    if (!bid || !sid) return null
    const targetSession = knownSessionSummary(sid)
    const sessionMode = targetSession?.session_mode || (targetSession?.type === 'discuss' ? 'discuss' : 'chat')
    const generation = userScopeGeneration
    const updated = await requestUpdateSessionAgent(bid, sid, {
      type: sessionMode === 'discuss' ? 'discuss' : 'chat',
      sessionMode,
      runtimeType: 'model',
      metadata: {},
      runtimeMetadata: {},
    })
    if (generation !== userScopeGeneration || (currentBotId.value ?? '').trim() !== bid) return null
    upsertSession(updated)
    if (isFocusedChatTarget(resolved)) {
      explicitSessionSelection.value = true
      draftIntent.value = false
    }
    clearACPRuntimeStatus(bid, sid)
    return updated
  }

  async function ensureChatViewSession(target: ChatViewTarget, firstPrompt?: string): Promise<ChatViewTarget> {
    if (target.sessionId) return target
    const creationKey = draftSessionCreationKey(target)
    if (draftSessionCreations.has(creationKey)) {
      throw new StreamFailureError('Session creation is already in progress', 'startup')
    }
    draftSessionCreations.add(creationKey)
    try {
      const pendingACP = pendingACPStateFor(target)
      if (pendingACP) {
        const { session: created } = await createACPSessionForTarget({
          ...pendingACP.input,
          runtimeId: pendingACP.runtimeId,
        }, target)
        if (firstPrompt?.trim() && !created.title?.trim()) {
          created.title = provisionalSessionTitle(firstPrompt)
          upsertSession(created)
          rememberSession(created)
        }
        return { ...target, sessionId: created.id }
      }

      const generation = userScopeGeneration
      const created = await createSession(target.botId)
      if (
        generation !== userScopeGeneration
        || (currentBotId.value ?? '').trim() !== target.botId
      ) {
        const error = new Error('Chat scope changed during Session creation')
        error.name = 'AbortError'
        throw error
      }
      if (firstPrompt?.trim()) created.title = provisionalSessionTitle(firstPrompt)
      upsertSession(created)
      rememberSession(created)
      promoteDraftChatView(target, created.id)
      if (isFocusedChatTarget(target)) {
        sessionId.value = created.id
        explicitSessionSelection.value = true
        draftIntent.value = false
      }
      return { ...target, sessionId: created.id }
    } finally {
      draftSessionCreations.delete(creationKey)
    }
  }

  // defaultRuntimeIsACP reports whether the bot's default chat runtime is an
  // external ACP agent. It is a lightweight precheck (runtime/agent presence
  // only); full eligibility (workspace_exec, agent enabled, managed fields) is
  // still decided by the chat-pane default-ACP initializer. Used to keep the
  // history-vs-default-ACP decision inside the init chain instead of letting
  // initialize() auto-select a history session that a late watcher then drops.
  async function defaultRuntimeIsACP(botId: string): Promise<boolean> {
    const input = await defaultACPSessionInputFromSettings(botId)
    return input !== null
  }

  async function defaultACPSettingsForAgent(botId: string, agentId: string): Promise<Partial<ACPAgentSessionInput>> {
    try {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const { data } = await (getBotsByBotIdSettings as any)({ path: { bot_id: botId }, throwOnError: true })
      const settings = data as {
        chat_runtime?: string
        chat_acp_agent_id?: string | null
        chat_acp_project_path?: string | null
        chat_acp_project_mode?: string | null
      } | undefined
      if (settings?.chat_runtime !== 'acp_agent') return {}
      if ((settings.chat_acp_agent_id ?? '').trim() !== agentId) return {}
      return {
        projectPath: settings.chat_acp_project_path?.trim() || undefined,
        projectMode: settings.chat_acp_project_mode?.trim() || undefined,
      }
    } catch {
      return {}
    }
  }

  async function defaultACPSessionInputFromSettings(botId: string): Promise<ACPAgentSessionInput | null> {
    const bid = botId.trim()
    if (!bid) return null
    try {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const { data } = await (getBotsByBotIdSettings as any)({ path: { bot_id: bid }, throwOnError: true })
      const settings = data as {
        chat_runtime?: string
        chat_acp_agent_id?: string | null
        chat_acp_project_path?: string | null
        chat_acp_project_mode?: string | null
      } | undefined
      if (settings?.chat_runtime !== 'acp_agent') {
        rememberDefaultACPInput(bid, null)
        return null
      }
      const agentId = settings.chat_acp_agent_id?.trim() ?? ''
      if (!agentId) {
        rememberDefaultACPInput(bid, null)
        return null
      }
      const input = {
        agentId,
        projectPath: settings.chat_acp_project_path?.trim() || ACP_DEFAULT_PROJECT_PATH,
        projectMode: settings.chat_acp_project_mode?.trim() || ACP_DEFAULT_PROJECT_MODE,
      }
      rememberDefaultACPInput(bid, input)
      return input
    } catch {
      return null
    }
  }


  async function stageDefaultACPFromSettings(requestId: number) {
    const bid = (currentBotId.value ?? '').trim()
    if (!bid || sessionId.value || explicitSessionSelection.value) return
    const cached = cachedDefaultACPInput(bid)
    if (cached.loaded) {
      if (cached.input && !pendingACPMatchesInput(cached.input)) stageDefaultACPSession(cached.input)
      return
    }
    const input = await defaultACPSessionInputFromSettings(bid)
    if (!input) return
    if (requestId !== selectSessionRequestId) return
    if ((currentBotId.value ?? '').trim() !== bid || sessionId.value || explicitSessionSelection.value) return
    if (pendingACPMatchesInput(input)) return
    stageDefaultACPSession(input)
  }

  async function initialize() {
    if (initializing.value) {
      const requestedBotId = (currentBotId.value ?? '').trim() || null
      if (initializingBotId && requestedBotId !== initializingBotId) {
        initializeRerunRequested = true
      }
      if (initializePromise) await initializePromise
      return
    }

    const generation = userScopeGeneration
    const run = (async () => {
      initializing.value = true
      loadingChats.value = true
      try {
        do {
          if (generation !== userScopeGeneration) return
          initializeRerunRequested = false
          initializingBotId = (currentBotId.value ?? '').trim() || null
          // Every entry into initialize starts from a clean transcript window. We
          // reset here unconditionally so the success path that hydrates
          // `sessionId` without clearing messages can't carry a stale
          // `hasLoadedOlder = true` from a previous bot into the new bot's first
          // refresh (which would take the merge branch and duplicate optimistic
          // turns).
          prepareForInitialization()
          resetRefreshCoordinator()
          stopStreams()
          stopWebSocket()

          const bid = await ensureBot()
          if (generation !== userScopeGeneration) return
          if (!bid) {
            replaceSessions([])
            sessionsCursor.value = null
            hasMoreSessions.value = false
            sessionId.value = null
            clearPendingACPSession()
            clearHistoryView()
            continue
          }
          initializingBotId = bid

          let response: Awaited<ReturnType<typeof fetchSessions>>
          let defaultIsACP = false
          try {
            ;[response, defaultIsACP] = await Promise.all([
              fetchSessions(bid),
              defaultRuntimeIsACP(bid),
            ])
          } catch (error) {
            if (generation !== userScopeGeneration) return
            if ((currentBotId.value ?? '').trim() !== bid) {
              initializeRerunRequested = true
              continue
            }
            throw error
          }
          if (generation !== userScopeGeneration) return
          if ((currentBotId.value ?? '').trim() !== bid) {
            initializeRerunRequested = true
            continue
          }

          const visibleSessions = replaceSessions(response.items)
          sessionsCursor.value = response.nextCursor
          hasMoreSessions.value = response.nextCursor !== null

          const restoredSessionId = (sessionId.value ?? '').trim()
          const restoredExplicitSession = restoredSessionId && explicitSessionSelection.value
            ? await ensureSessionSummary(bid, restoredSessionId)
            : null
          if (generation !== userScopeGeneration) return
          if ((currentBotId.value ?? '').trim() !== bid) {
            initializeRerunRequested = true
            continue
          }
          const preservePendingACPStage = !!pendingACPSessionInput.value && !sessionId.value
          const preserveExplicitEmptyComposer = explicitSessionSelection.value && !sessionId.value
          const preferDefaultACP = defaultIsACP
            && !preservePendingACPStage
            && !preserveExplicitEmptyComposer
            && !explicitSessionSelection.value

          if (preservePendingACPStage) {
            sessionId.value = null
            markHistoryEmpty()
          } else if (preserveExplicitEmptyComposer) {
            sessionId.value = null
            clearHistoryView()
          } else if (preferDefaultACP) {
            sessionId.value = null
            explicitSessionSelection.value = false
            draftIntent.value = false
            clearHistoryView()
          } else if (restoredExplicitSession) {
            draftIntent.value = false
          } else if (!visibleSessions.length) {
            sessionId.value = null
            explicitSessionSelection.value = false
            clearHistoryView()
          } else {
            // Keep a VALID persisted session; otherwise, if the user intentionally
            // closed down to the draft "New Session" page, keep that on reload instead
            // of force-opening a random session; otherwise pick the most recent real
            // conversation (the server sorts by recency). Skip schedule runs — they
            // are read-only execution history, so landing on a cron run when
            // switching bots would be surprising; a schedule run is reachable from
            // the sidebar's Schedule pivot.
            // Transcript hydration is driven by startSessionMessagesStream below — no
            // eager loadMessages REST round trip from here.
            if (sessionId.value && knownSessionSummary(sessionId.value)) {
              draftIntent.value = false
            } else if (draftIntent.value) {
              sessionId.value = null
              clearHistoryView()
            } else {
              const firstConversation = visibleSessions.find(s => (s.type ?? 'chat') !== 'schedule')
              sessionId.value = (firstConversation ?? visibleSessions[0]!).id
              explicitSessionSelection.value = false
            }
          }

          startWebSocket(bid)
          startBotSessionsActivityStream(bid)
          if (sessionId.value) startSessionMessagesStream(bid, sessionId.value)
        } while (initializeRerunRequested)
      } finally {
        if (generation === userScopeGeneration) {
          loadingChats.value = false
          initializing.value = false
          initializingBotId = null
          initializeRerunRequested = false
        }
        if (initializePromise === run) {
          initializePromise = null
        }
      }
    })()
    initializePromise = run
    await run
  }

  // Selection changes focus only. Visible panel bindings own Session SSE
  // lifetimes, and keyed transcript views retain their own cached history.
  //
  // We deliberately do NOT call `abortAllAssistantStreams()` here: an
  // assistant stream that started in session A keeps running server-side
  // after the user switches to B, and finalizes against A's history when
  // the user comes back (the `appendTurnToSession` / WS handlers are
  // already gated on `sessionId.value === <stream's sessionId>`, so the
  // orphan does not bleed into B's view).
  function switchActiveSession(sid: string, previousSessionId = '') {
    const bid = (currentBotId.value ?? '').trim()
    if (!bid || !sid) return
    const previous = previousSessionId.trim()
    if (previous && previous !== sid) {
      releaseHiddenSessionView(chatViews.getSession(bid, previous) ?? null)
    }
    startSessionMessagesStream(bid, sid)
  }

  async function selectBot(targetBotId: string) {
    if (currentBotId.value === targetBotId) return
    selectSessionRequestId++
    abort()
    abortAllAssistantStreams()
    clearPendingACPSession()
    clearFsForBotSwitch()
    currentBotId.value = targetBotId
    sessionId.value = null
    clearRememberedSessions()
    explicitSessionSelection.value = false
    draftIntent.value = false
    await initialize()
  }

  async function selectSession(targetSessionId: string, options: { explicitSelection?: boolean } = {}) {
    const sid = targetSessionId.trim()
    if (!sid) return
    const previousSessionId = (sessionId.value ?? '').trim()
    const sameSession = sid === previousSessionId
    const requestId = ++selectSessionRequestId
    const bid = (currentBotId.value ?? '').trim()
    clearPendingACPSession()
    sessionId.value = sid
    draftIntent.value = false
    explicitSessionSelection.value = options.explicitSelection !== false
    if (!sameSession) switchActiveSession(sid, previousSessionId)
    // Even when `sid` is already the persisted selection, a page refresh may
    // have no summary for it yet (for example an ACP session outside the first
    // sidebar page). Hydrate before consumers branch on runtime_type.
    await ensureSessionSummary(bid, sid, requestId)
  }

  async function createNewSession(options: { explicitSelection?: boolean } = {}) {
    const bid = await ensureBot()
    if (!bid) return
    resetToEmptyComposer({
      explicitSelection: options.explicitSelection === true,
      draftIntent: true,
    })
  }

  // Switch the global view to the draft (no real session yet). Unlike
  // createNewSession this assumes the bot is already active and only resets the
  // view, so per-session chat tabs can activate their draft tab without minting a
  // session. selectSession early-returns on an empty id, so a draft needs this.
  function selectDraft(options: { explicitSelection?: boolean } = {}) {
    const explicitSelection = options.explicitSelection === true
    resetToEmptyComposer({
      clearPendingACP: false,
      explicitSelection,
      draftIntent: true,
    })
    if (!explicitSelection) {
      void stageDefaultACPFromSettings(selectSessionRequestId)
    }
  }

  function normalizedACPInput(input: ACPAgentSessionInput): ACPAgentSessionInput {
    const metadata = acpSessionMetadata(input)
    return {
      ...input,
      agentId: String(metadata.acp_agent_id ?? ''),
      projectPath: String(metadata.project_path ?? ''),
      projectMode: String(metadata.acp_project_mode ?? ''),
    }
  }

  function applyDraftViewRequest(
    request: NonNullable<typeof draftViewRequested.value>,
    mirrorGlobalSelection: boolean,
  ) {
    const target: ChatViewTarget = {
      botId: request.botId,
      sessionId: null,
      viewId: request.viewId,
    }
    if (mirrorGlobalSelection) {
      if (request.input) stageNewACPSession(request.input, target)
      else resetToEmptyComposer({ explicitSelection: request.explicitSelection, draftIntent: true }, target)
      return
    }

    const draft = chatView(target)
    draft.transcript.clearHistoryView()
    discardDraftACPStage(target)
    if (request.input) {
      rememberDraftACPStage(target, {
        botId: request.botId,
        input: normalizedACPInput(request.input),
        runtimeId: '',
      })
    }
  }

  function requestDraftView(target: ChatViewTarget, input: ACPAgentSessionInput | null, activate = isFocusedChatTarget(target)) {
    const resolved = normalizedChatViewTarget(target)
    const request = {
      botId: resolved.botId,
      viewId: resolved.viewId,
      expectedSessionId: resolved.sessionId,
      explicitSelection: true,
      input: input ? normalizedACPInput(input) : null,
      activate,
      seq: ++draftViewRequestSeq,
    }
    draftViewRequested.value = request
  }

  function invalidateDraftViewCommand(target: ChatViewTarget) {
    const key = draftSessionCreationKey(target)
    draftViewCommandVersions.delete(key)
  }

  function beginDraftViewCommand(target: ChatViewTarget) {
    const key = draftSessionCreationKey(target)
    const version = ++draftViewCommandSequence
    draftViewCommandVersions.set(key, version)
    return {
      isCurrent: () => draftViewCommandVersions.get(key) === version,
      finish: () => {
        if (draftViewCommandVersions.get(key) === version) {
          draftViewCommandVersions.delete(key)
        }
      },
    }
  }

  async function handleWebNewCommand(
    text: string,
    attachments: ChatAttachment[] | undefined,
    target: ChatViewTarget,
  ): Promise<WebNewCommandResult> {
    const parsed = parseWebNewCommand(text)
    if (!parsed) return { kind: 'none' }
    const generation = userScopeGeneration
    const activate = isFocusedChatTarget(target)
    if (attachments?.length) {
      return { kind: 'error', message: 'Attachments are not supported with /new' }
    }
    const agentId = parsed.agentId.trim()
    if (!agentId) {
      if (parsed.mode === 'discuss') {
        return { kind: 'error', message: 'Discuss ACP sessions require an agent, for example /new discuss codex' }
      }
      const command = beginDraftViewCommand(target)
      if (command.isCurrent()) requestDraftView(target, null, activate)
      command.finish()
      return { kind: 'handled' }
    }
    if (agentId !== 'codex' && agentId !== 'claude-code') {
      return { kind: 'error', message: `Unknown ACP agent "${agentId}"` }
    }
    const command = beginDraftViewCommand(target)
    try {
      const targetBotId = target.botId === '__unbound__' ? '' : target.botId
      const bid = targetBotId || await ensureBot()
      if (!bid) return { kind: 'error', message: 'Bot not ready' }
      const defaults = await defaultACPSettingsForAgent(bid, agentId)
      if (
        generation !== userScopeGeneration
        || (currentBotId.value ?? '').trim() !== bid
        || !command.isCurrent()
      ) {
        return { kind: 'handled' }
      }
      requestDraftView(target, {
        agentId,
        sessionMode: parsed.mode === 'discuss' ? 'discuss' : 'chat',
        ...defaults,
      }, activate)
      return { kind: 'handled' }
    } finally {
      command.finish()
    }
  }

  function isWebSlashInput(text: string): boolean {
    return text.trim().startsWith('/')
  }

  function quickActionIDForSlash(text: string): string {
    const parts = text.trim().split(/\s+/)
    const command = parts[0]?.toLowerCase() ?? ''
    const action = parts[1]?.toLowerCase() ?? ''
    if (command === '/help' && !action) return 'help'
    if (command === '/skill' && (!action || action === 'list')) return 'skill.list'
    return ''
  }

  async function handleWebSlashCommand(
    text: string,
    hasRequestedSkills = false,
    composerScope = 'chat',
    target?: ChatViewTarget,
  ): Promise<WebSlashCommandResult> {
    if (!isWebSlashInput(text) || hasRequestedSkills) return { kind: 'none' }
    const resolved = normalizedChatViewTarget(target)
    const bid = resolved.botId
    if (!bid) return { kind: 'error', message: 'Bot not selected' }
    const sid = resolved.sessionId ?? ''
    const scope = composerScope.trim() || 'chat'
    const commandScope = {
      botId: bid,
      sessionId: sid || undefined,
      composerScope: scope,
    }

    const actionID = quickActionIDForSlash(text)
    if (!actionID) return { kind: 'none' }
    const skillActivationAllowed = !chatTargetFor(resolved).isACP
    let event: CommandEventResponse | null
    try {
      event = await executeQuickAction(bid, actionID, {
        invocationId: createInvocationId(),
        composerScope: scope,
        sessionId: sid || undefined,
        skillActivationAllowed,
      })
    } catch (error) {
      const message = resolveApiErrorMessage(error, commandErrorMessage('generic'))
      showCommandError('generic', message, commandScope)
      return { kind: 'error', message }
    }

    if (!event) return { kind: 'none' }
    rememberCommandEvent(event, commandScope)
    if (event.type === 'command_error') {
      return { kind: 'error', message: event.error?.message || commandErrorMessage('generic') }
    }
    return { kind: 'handled' }
  }

  async function removeSession(targetSessionId: string, options: { fallbackMode?: SidebarSessionMode } = {}) {
    const delId = targetSessionId.trim()
    if (!delId) return
    const bid = currentBotId.value ?? ''
    if (!bid) throw new Error('Bot not selected')
    await requestDeleteSession(bid, delId)
    abort({ botId: bid, sessionId: delId, viewId: focusedChatViewId.value })
    markSessionDeleted(bid, delId)
    deletedSession.value = { id: delId, botId: bid, seq: ++deletedSessionSeq }
    stopSessionMessagesStream(bid, delId)
    chatViews.removeSession(bid, delId)
    if ((currentBotId.value ?? '').trim() !== bid) return
    clearACPRuntimeStatus(bid, delId)
    removeSessionFromList(delId)
    if (sessionId.value !== delId) return
    const fallbackMode = options.fallbackMode ?? 'recent'
    const nextSession = fallbackSessionAfterDelete(fallbackMode)
    if (!nextSession) {
      sessionId.value = null
      explicitSessionSelection.value = false
      draftIntent.value = false
      clearHistoryView()
      return
    }
    const next = nextSession.id
    sessionId.value = next
    explicitSessionSelection.value = false
    draftIntent.value = false
    switchActiveSession(next, delId)
  }

  async function renameSession(targetSessionId: string, title: string): Promise<SessionSummary> {
    const sid = targetSessionId.trim()
    const nextTitle = title.trim()
    if (!sid) throw new Error('Session not selected')
    const bid = currentBotId.value ?? ''
    if (!bid) throw new Error('Bot not selected')
    const updated = await requestUpdateSessionTitle(bid, sid, nextTitle)
    const patch: Partial<SessionSummary> = { title: updated.title ?? nextTitle }
    if (updated.updated_at) patch.updated_at = updated.updated_at
    patchSessionInList(sid, patch)
    return updated
  }

  async function forkMessage(messageId: string, options: { title?: string, target?: ChatViewTarget } = {}): Promise<boolean> {
    const target = normalizedChatViewTarget(options.target)
    const bid = target.botId
    const sid = target.sessionId ?? ''
    const mid = messageId.trim()
    const view = chatView(target)
    const generation = userScopeGeneration
    const activate = isFocusedChatTarget(target)
    if (
      !bid || !sid || !mid
      || chatReadOnlyFor(target)
      || !chatCanForkFor(target)
      || isChatViewStreaming(target)
      || view.transcript.loadingMessages.value
    ) return false

    const key = `${bid}:${sid}:${mid}`
    if (forkingMessages.has(key)) return false
    forkingMessages.add(key)
    try {
      const forked = await requestForkSessionFromMessage(bid, sid, mid, { title: options.title })
      if (generation !== userScopeGeneration || (currentBotId.value ?? '').trim() !== bid) return true

      upsertSession(forked)
      rememberSession(forked)
      void refreshSessionsList(bid)

      const turns = await fetchSessionWindow(bid, forked.id)
      if (generation !== userScopeGeneration || (currentBotId.value ?? '').trim() !== bid) return true
      const anchoredForked = withForkAnchorFromUITurns(forked, turns)
      if (anchoredForked !== forked) {
        upsertSession(anchoredForked)
        rememberSession(anchoredForked)
      }
      sessionTranscript(bid, forked.id).replaceHistoryView(turns, forked.id)
      forkedSessionRequested.value = {
        botId: bid,
        viewId: target.viewId,
        expectedSessionId: sid,
        sessionId: forked.id,
        title: (anchoredForked.title ?? options.title ?? '').trim(),
        explicitSelection: true,
        activate,
        seq: ++forkedSessionRequestSeq,
      }
      chatViews.prune()
      return true
    } catch (error) {
      toast.error(resolveApiErrorMessage(error, forkFailedMessage()))
      return false
    } finally {
      forkingMessages.delete(key)
    }
  }

  async function sendMessage(text: string, attachments?: ChatAttachment[], options: SendMessageOptions = {}): Promise<SendMessageResult> {
    const trimmed = text.trim()
    const requestedSkills = normalizeRequestedSkills(options.requestedSkills)
    let viewTarget = normalizedChatViewTarget(options.target)
    const composerScope = options.composerScope?.trim()
      || (options.target ? `${viewTarget.botId}:${viewTarget.viewId}` : 'chat')
    const commandScope = {
      botId: viewTarget.botId,
      sessionId: viewTarget.sessionId ?? undefined,
      composerScope,
    }
    if (!trimmed && !attachments?.length && requestedSkills.length === 0) return { ok: false, stage: 'startup' }

    if (requestedSkills.length > 0 && isWebSlashInput(trimmed)) {
      const message = commandErrorMessage('invalid_skill_slash_syntax')
      showCommandError('invalid_skill_slash_syntax', message, commandScope)
      return { ok: false, stage: 'startup', error: message, restoreInput: text, restoreAttachments: attachments, restoreRequestedSkills: cloneRequestedSkills(requestedSkills) }
    }

    if (isWebSlashInput(trimmed) && attachments?.length) {
      const message = commandErrorMessage('slash_attachments_unsupported')
      showCommandError('slash_attachments_unsupported', message, commandScope)
      return { ok: false, stage: 'startup', error: message, restoreInput: text, restoreAttachments: attachments, restoreRequestedSkills: cloneRequestedSkills(requestedSkills) }
    }

    const newCommand = await handleWebNewCommand(trimmed, attachments, viewTarget)
    if (newCommand.kind === 'handled') return { ok: true }
    if (newCommand.kind === 'error') {
      return { ok: false, stage: 'startup', error: newCommand.message, restoreInput: text, restoreAttachments: attachments, restoreRequestedSkills: cloneRequestedSkills(requestedSkills) }
    }
    const slashCommand = await handleWebSlashCommand(trimmed, requestedSkills.length > 0, composerScope, viewTarget)
    if (slashCommand.kind === 'handled') return { ok: true }
    if (slashCommand.kind === 'error') {
      return { ok: false, stage: 'startup', error: slashCommand.message, restoreInput: text, restoreAttachments: attachments, restoreRequestedSkills: cloneRequestedSkills(requestedSkills) }
    }
    if (viewTarget.sessionId && chatReadOnlyFor(viewTarget)) {
      return { ok: false, stage: 'startup' }
    }
    clearCommandEvent(commandScope)
    const initialView = chatView(viewTarget)
    if (
      isChatViewStreaming(viewTarget, composerScope)
      || isChatViewCreatingSession(viewTarget)
      || initialView.transcript.loadingMessages.value
      || !viewTarget.botId
    ) return { ok: false, stage: 'startup' }

    let assistantTurn: ChatAssistantTurn | null = null
    let userTurn: ChatUserTurn | null = null
    let sendBotId = ''
    let sendSessionId = ''
    let sendInvocationId = ''
    let turnAppendStarted = false

    const wasDraft = !viewTarget.sessionId
    const serverSlashActivation = isWebSlashInput(trimmed) && quickActionIDForSlash(trimmed) === ''
    const serverSkillActivation = requestedSkills.length > 0 || serverSlashActivation
    if (
      serverSkillActivation
      && wasDraft
      && pendingACPStateFor(viewTarget)
    ) {
      const message = commandErrorMessage('unsupported_skill_slash_context')
      showCommandError('unsupported_skill_slash_context', message, commandScope)
      return { ok: false, stage: 'startup', error: message, restoreInput: text, restoreAttachments: attachments, restoreRequestedSkills: cloneRequestedSkills(requestedSkills), composerScope }
    }

    loading.value = true
    const deferSessionCreation = serverSkillActivation && wasDraft
    try {
      const modelId = options.modelId?.trim() || overrideModelId.value || undefined
      const reasoningEffort = options.reasoningEffort?.trim() || overrideReasoningEffort.value || undefined
      if (!deferSessionCreation) {
        viewTarget = await ensureChatViewSession(viewTarget, wasDraft ? trimmed : undefined)
      }

      const bid = viewTarget.botId
      const sid = viewTarget.sessionId ?? ''
      if (!sid && !deferSessionCreation) throw new Error('Session not selected')
      sendBotId = bid
      sendSessionId = sid
      sendInvocationId = createInvocationId()
      const sendTranscript = transcriptForTarget(viewTarget)
      // Tell the tab store to pin (and, for a draft, repoint) this session's tab.
      if (sid) {
        userSentInSession.value = {
          id: sid,
          botId: bid,
          viewId: viewTarget.viewId,
          wasDraft,
          seq: ++userSendSeq,
        }
      }

      assistantTurn = sendTranscript.createOptimisticAssistantTurn()
      turnAppendStarted = true
      options.onBeforeTurnAppend?.()
      if (!serverSkillActivation) {
        userTurn = sendTranscript.createOptimisticUserTurn(trimmed, attachments)
        sendTranscript.appendToView(userTurn, assistantTurn)
      }

      if (!ensureWebSocketConnected(bid)) {
        throw new StreamFailureError('WebSocket is not connected', 'startup')
      }
      const completion = trackAssistantStream({
        invocationId: sendInvocationId,
        assistantTurn,
        botId: bid,
        sessionId: sid,
        composerScope,
        viewId: viewTarget.viewId,
      })
      if (!sendWebSocketMessage(bid, {
        type: 'message',
        invocation_id: sendInvocationId,
        composer_scope: composerScope,
        text: trimmed,
        session_id: sid || undefined,
        attachments,
        requested_skills: requestedSkills.length ? requestedSkillRequestsForWire(requestedSkills) : undefined,
        model_id: modelId,
        reasoning_effort: reasoningEffort,
        workspace_target_id: options.workspaceTargetId?.trim() || undefined,
      })) throw new StreamFailureError('WebSocket is not connected', 'startup')
      await completion
      const createdSessionId = createdSessionIdForInvocation(sendInvocationId)
      const fallbackActiveSessionId = !options.target && (currentBotId.value ?? '').trim() === bid
        ? sessionId.value ?? ''
        : ''
      const refreshSessionId = sendSessionId || createdSessionId || fallbackActiveSessionId
      forgetCreatedSession(sendInvocationId)
      if (refreshSessionId) await refreshCurrentSession(bid, refreshSessionId)

      loading.value = false
      return { ok: true }
    } catch (error) {
      const err = error instanceof Error ? error : new Error('Unknown error')
      const isAbort = err.name === 'AbortError'
      const isCommandError = err instanceof CommandStreamError
      const reason = resolveApiErrorMessage(error, err.message || sendFailedMessage())
      const errorCode = parseMemohError(error)?.code
      const stage: SendMessageStage = err instanceof StreamFailureError
        ? err.stage
        : (assistantTurn && hasVisibleAssistantBlocks(assistantTurn) ? 'stream' : 'startup')
      const createdSessionId = sendInvocationId ? createdSessionIdForInvocation(sendInvocationId) : ''
      const bid = sendBotId || viewTarget.botId || currentBotId.value || ''
      const sid = sendSessionId || createdSessionId

      if (assistantTurn) finalizeStreamFailure(assistantTurn, bid, sid, err)
      if (!isAbort && stage === 'startup' && userTurn) {
        removeTurnFromSession(bid, sid, userTurn)
      }
      if (!isAbort && stage === 'startup' && deferSessionCreation && wasDraft && createdSessionId) {
        await cleanupFailedDeferredSession(bid, createdSessionId, composerScope)
      }

      if (sendInvocationId) discardAssistantStream(sendInvocationId)
      if (sendInvocationId) forgetCreatedSession(sendInvocationId)
      loading.value = false

      if (!isAbort && stage === 'startup' && turnAppendStarted) {
        options.onTurnAppendAborted?.()
      }

      if (isAbort) return { ok: false, stage: 'stream', error: reason, errorCode }
      if (stage === 'startup') {
        const currentBid = (currentBotId.value ?? '').trim()
        const currentSid = (sessionId.value ?? '').trim()
        const restoredOriginalDraft = deferSessionCreation
          && wasDraft
          && !currentSid
          && focusedChatViewId.value === viewTarget.viewId
        const stillCurrent = currentBid === bid
          && (!sid || currentSid === sid || restoredOriginalDraft)
        const deferredDraftStillCurrent = !(deferSessionCreation && wasDraft && currentSid)
        const commandErrorRestoredDraft = isCommandError && deferSessionCreation && wasDraft && !currentSid
        if (stillCurrent && deferredDraftStillCurrent && (!isCommandError || commandErrorRestoredDraft)) {
          rememberStartupSendFailure({ botId: bid, sessionId: sid, composerScope, error: reason, restoreInput: text, restoreAttachments: attachments, restoreRequestedSkills: cloneRequestedSkills(requestedSkills) })
        }
        return { ok: false, stage, error: reason, errorCode, restoreInput: text, restoreAttachments: attachments, restoreRequestedSkills: cloneRequestedSkills(requestedSkills), composerScope }
      }
      return { ok: false, stage, error: reason, errorCode }
    }
  }

  async function retryLatestAssistant(
    messageId: string,
    options: { target?: ChatViewTarget, modelId?: string, reasoningEffort?: string, workspaceTargetId?: string } = {},
  ): Promise<SendMessageResult> {
    const viewTarget = normalizedChatViewTarget(options.target)
    const bid = viewTarget.botId
    const sid = viewTarget.sessionId ?? ''
    const transcript = transcriptForTarget(viewTarget)
    const targetID = messageId.trim()
    if (!bid || !sid || !targetID || chatReadOnlyFor(viewTarget)) return { ok: false, stage: 'startup' }
    if (isChatViewStreaming(viewTarget) || transcript.loadingMessages.value) return { ok: false, stage: 'startup' }
    const target = transcript.findTurnByServerId(targetID)
    if (!target || !transcript.isLatestVisibleAssistantTurn(target)) return { ok: false, stage: 'startup' }

    const invocationId = createInvocationId()
    const assistantTurn = transcript.createOptimisticAssistantTurn()
    const restoreForkAnchor = updateForkAnchorForReplacedMessage(sid, target, transcript.messages)
    const replacedTurns = transcript.replaceTailFromTurn(target, [assistantTurn])
    loading.value = true
    try {
      if (!ensureWebSocketConnected(bid)) {
        throw new StreamFailureError('WebSocket is not connected', 'startup')
      }
      const completion = trackAssistantStream({ invocationId, assistantTurn, botId: bid, sessionId: sid })
      if (!sendWebSocketMessage(bid, {
        type: 'retry_message',
        invocation_id: invocationId,
        session_id: sid,
        message_id: targetID,
        model_id: options.modelId?.trim() || overrideModelId.value || undefined,
        reasoning_effort: options.reasoningEffort?.trim() || overrideReasoningEffort.value || undefined,
        workspace_target_id: options.workspaceTargetId?.trim() || undefined,
      })) throw new StreamFailureError('WebSocket is not connected', 'startup')
      await completion
      await refreshCurrentSession(bid, sid)
      refreshLoadingForSession(bid, sid)
      return { ok: true }
    } catch (error) {
      const err = error instanceof Error ? error : new Error('Unknown error')
      const reason = resolveApiErrorMessage(error, err.message || sendFailedMessage())
      const errorCode = parseMemohError(error)?.code
      const stage: SendMessageStage = err instanceof StreamFailureError
        ? err.stage
        : (hasVisibleAssistantBlocks(assistantTurn) ? 'stream' : 'startup')
      discardAssistantStream(invocationId)
      if (stage === 'startup') {
        restoreForkAnchor?.()
        restoreTailFromOptimistic(bid, sid, null, assistantTurn, replacedTurns)
      } else {
        finalizeStreamFailure(assistantTurn, bid, sid, err)
      }
      refreshLoadingForSession(bid, sid)
      return { ok: false, stage, error: reason, errorCode }
    }
  }

  async function editLatestUser(
    messageId: string,
    text: string,
    options: { target?: ChatViewTarget, modelId?: string, reasoningEffort?: string, workspaceTargetId?: string } = {},
  ): Promise<SendMessageResult> {
    const trimmed = text.trim()
    const viewTarget = normalizedChatViewTarget(options.target)
    const bid = viewTarget.botId
    const sid = viewTarget.sessionId ?? ''
    const transcript = transcriptForTarget(viewTarget)
    const targetID = messageId.trim()
    if (!bid || !sid || !targetID || !trimmed || chatReadOnlyFor(viewTarget)) return { ok: false, stage: 'startup' }
    if (isChatViewStreaming(viewTarget) || transcript.loadingMessages.value) return { ok: false, stage: 'startup' }
    const target = transcript.findTurnByServerId(targetID)
    if (!target || !transcript.isLatestVisibleUserTurn(target)) return { ok: false, stage: 'startup' }
    if (hasUserAttachments(target)) return { ok: false, stage: 'startup' }

    const invocationId = createInvocationId()
    const userTurn = transcript.createOptimisticUserTurn(trimmed)
    const assistantTurn = transcript.createOptimisticAssistantTurn()
    const restoreForkAnchor = updateForkAnchorForReplacedMessage(sid, target, transcript.messages)
    const replacedTurns = transcript.replaceTailFromTurn(target, [userTurn, assistantTurn])
    loading.value = true
    try {
      if (!ensureWebSocketConnected(bid)) {
        throw new StreamFailureError('WebSocket is not connected', 'startup')
      }
      const completion = trackAssistantStream({ invocationId, assistantTurn, botId: bid, sessionId: sid })
      if (!sendWebSocketMessage(bid, {
        type: 'edit_message',
        invocation_id: invocationId,
        session_id: sid,
        message_id: targetID,
        text: trimmed,
        model_id: options.modelId?.trim() || overrideModelId.value || undefined,
        reasoning_effort: options.reasoningEffort?.trim() || overrideReasoningEffort.value || undefined,
        workspace_target_id: options.workspaceTargetId?.trim() || undefined,
      })) throw new StreamFailureError('WebSocket is not connected', 'startup')
      await completion
      await refreshCurrentSession(bid, sid)
      refreshLoadingForSession(bid, sid)
      return { ok: true }
    } catch (error) {
      const err = error instanceof Error ? error : new Error('Unknown error')
      const reason = resolveApiErrorMessage(error, err.message || sendFailedMessage())
      const errorCode = parseMemohError(error)?.code
      const stage: SendMessageStage = err instanceof StreamFailureError
        ? err.stage
        : (hasVisibleAssistantBlocks(assistantTurn) ? 'stream' : 'startup')
      discardAssistantStream(invocationId)
      if (stage === 'startup') {
        restoreForkAnchor?.()
        restoreTailFromOptimistic(bid, sid, userTurn, assistantTurn, replacedTurns)
      } else {
        finalizeStreamFailure(assistantTurn, bid, sid, err)
      }
      refreshLoadingForSession(bid, sid)
      return { ok: false, stage, error: reason, errorCode, restoreInput: text }
    }
  }

  async function respondToolApproval(
    approval: UIToolApproval,
    decision: 'approve' | 'reject',
    target?: ChatViewTarget,
  ) {
    const viewTarget = normalizedChatViewTarget(target)
    const bid = viewTarget.botId
    const sid = viewTarget.sessionId ?? ''
    const transcript = transcriptForTarget(viewTarget)
    const approvalId = approval.approval_id?.trim()
    if (!bid || !sid || !approvalId) return false
    if (approval.status !== 'pending' || approval.can_approve === false) return false
    if (hasPendingApprovalResponse(approvalId)) return false
    if (!ensureWebSocketConnected(bid)) {
      toast.error(userInputConnectionLostMessage())
      return false
    }
    const invocationId = createInvocationId()
    const silent = isSessionStreaming(bid, sid)
    const previousApprovalStates = transcript.snapshotToolApprovalStates(approvalId)
    if (!beginApprovalResponse({
      invocationId,
      approvalId,
      botId: bid,
      sessionId: sid,
      silent,
      rollback: () => transcript.restoreToolApprovalStates(previousApprovalStates),
    })) return false
    let assistantTurn: ChatAssistantTurn | null = null
    let appendedAssistantTurn = false
    if (!silent) {
      assistantTurn = transcript.assistantTurnForApproval(approvalId) ?? transcript.createOptimisticAssistantTurn()
      appendedAssistantTurn = !transcript.hasTurn(assistantTurn)
      if (appendedAssistantTurn) transcript.appendToView(assistantTurn)
      assistantTurn.streaming = true
      void trackAssistantStream({ invocationId, assistantTurn, botId: bid, sessionId: sid }).catch((error: Error) => {
        finalizeStreamFailure(assistantTurn, bid, sid, error)
      })
      loading.value = true
    }
    // Optimistically update the approved/rejected tool block before the
    // server snapshot arrives so the buttons disappear immediately.
    transcript.markToolApprovalDecision(approvalId, decision === 'approve' ? 'approved' : 'rejected')
    try {
      if (!sendWebSocketMessage(bid, {
        type: 'tool_approval_response',
        invocation_id: invocationId,
        session_id: sid,
        approval_id: approvalId,
        short_id: approval.short_id,
        decision,
      })) throw new Error('WebSocket is not connected')
    } catch (error) {
      transcript.restoreToolApprovalStates(previousApprovalStates)
      settleApprovalResponse(invocationId, 'canceled')
      if (!silent) {
        discardAssistantStream(invocationId)
        if (assistantTurn && appendedAssistantTurn) transcript.removeFromView(assistantTurn)
      }
      loading.value = false
      toast.error(resolveApiErrorMessage(error, 'Failed to send tool approval response.'))
      return false
    }
    return true
  }

  async function respondUserInput(
    userInput: UIUserInput,
    payload: { answers?: WSUserInputAnswer[]; canceled?: boolean; reason?: string },
    target?: ChatViewTarget,
  ) {
    const viewTarget = normalizedChatViewTarget(target)
    const bid = viewTarget.botId
    const sid = viewTarget.sessionId ?? ''
    const transcript = transcriptForTarget(viewTarget)
    if (!bid || !sid || !userInput.user_input_id) return
    if (!ensureWebSocketConnected(bid)) {
      toast.error(userInputConnectionLostMessage())
      return
    }
    const invocationId = createInvocationId()
    const previousUserInputStates = transcript.snapshotUserInputStates(userInput.user_input_id)
    const assistantTurn = transcript.assistantTurnForUserInput(userInput.user_input_id) ?? transcript.createOptimisticAssistantTurn()
    const appendedAssistantTurn = !transcript.hasTurn(assistantTurn)
    if (appendedAssistantTurn) transcript.appendToView(assistantTurn)
    assistantTurn.streaming = true
    void trackAssistantStream({ invocationId, assistantTurn, botId: bid, sessionId: sid }).catch((error: Error) => {
      finalizeStreamFailure(assistantTurn, bid, sid, error)
      if (error.name === 'AbortError') {
        transcript.restoreUserInputStates(previousUserInputStates)
        return
      }
      // While the main session stream is still active a refresh would
      // clobber its in-flight state; roll back and let its end refresh
      // bring truth.
      if (isSessionStreaming(bid, sid)) {
        transcript.restoreUserInputStates(previousUserInputStates)
        return
      }
      void refreshCurrentSession(bid, sid).catch(() => {
        transcript.restoreUserInputStates(previousUserInputStates)
      })
    })
    loading.value = true

    const status = payload.canceled ? 'canceled' : 'submitted'
    transcript.markUserInputDecision(userInput.user_input_id, status)

    try {
      if (!sendWebSocketMessage(bid, {
        type: 'user_input_response',
        invocation_id: invocationId,
        session_id: sid,
        user_input_id: userInput.user_input_id,
        short_id: userInput.short_id,
        answers: payload.answers,
        canceled: payload.canceled === true,
        reason: payload.reason,
      })) throw new Error('WebSocket is not connected')
    } catch (error) {
      transcript.restoreUserInputStates(previousUserInputStates)
      discardAssistantStream(invocationId)
      if (appendedAssistantTurn) transcript.removeFromView(assistantTurn)
      loading.value = false
      toast.error(resolveApiErrorMessage(error, 'Failed to send user input response.'))
    }
  }

  return {
    messages,
    chatView,
    bindChatView,
    setChatViewVisible,
    unbindChatView,
    focusChatView,
    promoteDraftChatView,
    chatTargetFor,
    workspaceTargetSelectionFor,
    setWorkspaceTargetSelection,
    initializeWorkspaceTargetSelection,
    resetWorkspaceTargetSelection,
    chatReadOnlyFor,
    chatCanForkFor,
    isChatViewStreaming,
    isChatViewCreatingSession,
    streaming,
    streamingSessionId,
    sessions,
    sessionsCursor,
    hasMoreSessions,
    loadingMoreSessions,
    loadMoreSessions,
    acpRuntimeStatuses,
    acpRuntimePending,
    pendingACPSessionInput,
    pendingACPSessionMetadata,
    pendingACPRuntimeId,
    pendingACPRuntimeStatus,
    pendingACPRuntimeEnsuring,
    pendingACPStateFor,
    sessionId,
    hasExplicitSessionSelection,
    currentBotId,
    bots,
    activeSession,
    activeChatTarget,
    activeChatReadOnly,
    activeChatCanFork,
    knownSessions,
    knownSessionSummary,
    isSessionStreaming,
    loading,
    loadingChats,
    loadingMessages,
    loadingOlder,
    hasMoreOlder,
    // Exposed for tests only — do not branch on this in components. The
    // leading underscore reflects the test-only contract at the call site.
    _hasLoadedOlder: hasLoadedOlder,
    overrideModelId,
    overrideReasoningEffort,
    startupSendFailure,
    startupSendFailureFor,
    commandEvent,
    commandEventForScope,
    showCommandError,
    fsChangedAt,
    markFsChanged,
    affectsPath,
    fsEventForPath,
    initialize,
    refreshBots,
    selectBot,
    selectSession,
    stageACPSession,
    stageDefaultACPSession,
    cacheDefaultACPSession,
    resetToEmptyComposer,
    ensurePendingACPRuntime,
    setPendingACPModel,
    setPendingACPReasoning,
    clearPendingACPSession,
    createACPSession,
    updateCurrentSessionAgent,
    updateCurrentSessionToMemoh,
    acpRuntimeKey,
    ensureACPRuntime,
    setACPRuntimeModel,
    setACPRuntimeReasoning,
    createNewSession,
    selectDraft,
    userSentInSession,
    draftViewRequested,
    applyDraftViewRequest,
    forkedSessionRequested,
    guiToolUseRequested,
    deletedSession,
    removeSession,
    renameSession,
    forkMessage,
    sendMessage,
    retryLatestAssistant,
    editLatestUser,
    respondToolApproval,
    respondUserInput,
    loadOlderMessages,
    findMessageIdByExternalId,
    locateMessageByExternalId,
    clearStartupSendFailure,
    clearCommandEvent,
    abort,
  }
})
