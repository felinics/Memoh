import { defineStore, storeToRefs } from 'pinia'
import { computed, onScopeDispose, ref } from 'vue'
import { toast } from '@felinic/ui'
import { useChatSelectionStore } from '@/store/chat-selection'
import { useWorkdirsStore } from '@/store/workdirs'
import { onAuthSessionCleared } from '@/lib/auth-session'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { createInvocationId } from './chat-list.normalize'
import { createFsChangeBeacon } from './chat/fs-beacon'
import { createCommandEventRegistry } from './chat/command-events'
import { createSessionList } from './chat/session-list'
import { createACPController } from './chat/acp-controller'
import { createChatRefreshCoordinator } from './chat/refresh-coordinator'
import { createChatSend } from './chat/send'
import { createStartupSendFailures } from './chat/send-startup'
import { createChatCommands } from './chat/commands'
import { createChatBootstrap } from './chat/bootstrap'
import { createChatRuntimeLayer } from './chat/runtime-layer'
import { createChatViews } from './chat/views'
import { createChatTargets } from './chat/targets'
import { createChatBots } from './chat/bots'
import { createSessionActivity } from './chat/session-activity'
import { createWorkdirSessions } from './chat/workdir-sessions'
import { createSessionActions } from './chat/session-actions'
import {
  commandErrorMessage,
  forkFailedMessage,
  sendFailedMessage,
  userInputConnectionLostMessage,
} from './chat/messages'
import {
  createBackgroundTaskTracker,
} from './chat/background-tasks'
import { fetchSessions } from '@/composables/api/useChat'
import {
  bindBotIdInitializeWatch,
  createInitializeRecovery,
  createSessionListSnapshot,
} from './chat/session-list-init-recovery'

import type {
  ChatAssistantTurn,
  ChatMessage,
  ChatUserTurn,
} from './chat/types'

export type {
  ACPAgentSessionInput, ActiveChatTarget, AttachmentBlock, AttachmentItem,
  BackgroundTask, ChatAssistantTurn, ChatMessage, ChatSystemTurn, ChatUserTurn,
  ChatWorkspaceTargetSnapshot, ContentBlock, ErrorBlock, SendMessageOptions,
  SendMessageResult, SendMessageStage, TextBlock, ThinkingBlock, ToolCallBlock,
} from './chat/types'
export type { FsChangeBatch, FsChangeEvent, FsToolKind } from './chat/fs-beacon'
export type { ChatViewEntry, ChatViewTarget } from './chat/view-registry'

export const useChatStore = defineStore('chat', () => {
  const selectionStore = useChatSelectionStore()
  const workdirsStore = useWorkdirsStore()
  const { currentBotId, sessionId, draftIntent, explicitSelection: explicitSessionSelection } = storeToRefs(selectionStore)
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
  const views = createChatViews({
    currentBotId,
    sessionId,
    rememberBackgroundTask,
    applyPendingBackgroundEventsToTool,
    bumpFsChangedAtIfFsMutation,
  })
  const {
    focusedViewId: focusedChatViewId,
    projectionVersion: runtimeProjectionVersion,
    chatViews, assistantStreams, draftSessionCreations,
    draftCreationKey: draftSessionCreationKey,
    isCreatingDraft: isChatViewCreatingSession,
    normalizeTarget: normalizedChatViewTarget,
    isFocusedTarget: isFocusedChatTarget,
    chatView, transcriptForTarget, sessionTranscript, transcriptForTurn,
    messages, loadingMessages, loadingOlder, hasMoreOlder, hasLoadedOlder,
    clearHistoryView, prepareForInitialization, markHistoryEmpty,
    refreshCurrentSession, resyncRuntimeTranscript, loadInitialMessages, fetchSessionWindow,
    loadOlderMessages, findMessageIdByExternalId, locateMessageByExternalId,
    isSessionStreaming, streamingSessionId, streaming, isChatViewStreaming,
    workspaceTargetSelectionFor, setWorkspaceTargetSelection,
    initializeWorkspaceTargetSelection, resetWorkspaceTargetSelection,
    releaseHiddenSessionView, bindChatView, setChatViewVisible, unbindChatView,
    focusChatView, promoteDraftChatView,
    configure: configureChatViews,
    reset: resetTranscriptUserScope,
  } = views
  const reattachTurnToSession = (botId: string, targetSessionId: string, turn: ChatMessage) => sessionTranscript(botId, targetSessionId).reattachTurnToSession(botId, targetSessionId, turn)
  const removeTurnFromSession = (botId: string, targetSessionId: string, turn: ChatMessage) => {
    const transcript = targetSessionId.trim()
      ? sessionTranscript(botId, targetSessionId)
      : transcriptForTurn(turn)
    transcript?.removeTurnFromSession(botId, targetSessionId, turn)
  }
  const restoreTailFromOptimistic = (botId: string, targetSessionId: string, optimisticUserTurn: ChatUserTurn | null, assistantTurn: ChatAssistantTurn, replacedTurns: ChatMessage[]) => sessionTranscript(botId, targetSessionId).restoreTailFromOptimistic(botId, targetSessionId, optimisticUserTurn, assistantTurn, replacedTurns)
  const hasVisibleAssistantBlocks = (turn: ChatAssistantTurn) => transcriptForTurn(turn)?.hasVisibleAssistantBlocks(turn) ?? false
  const finalizeStreamFailure = (assistantTurn: ChatAssistantTurn, botId: string, targetSessionId: string, error: Error) => {
    transcriptForTurn(assistantTurn)?.finalizeStreamFailure(assistantTurn, botId, targetSessionId, error)
  }
  const {
    trackAssistantStream, discardAssistantStream,
    createdSessionIdForInvocation, forgetCreatedSession, clearStreamHistory,
  } = assistantStreams

  let userScopeGeneration = 0
  let selectSessionRequestId = 0
  const sessionList = createSessionList({ currentBotId, sessionId, messages })
  const {
    sessions, sessionsCursor, hasMoreSessions, loadingMoreSessions,
    activeSession, knownSessions, activeChatReadOnly, activeChatCanFork,
    currentSessionListRevision,
    updateForkAnchorForReplacedMessage,
    replaceSessions, appendSessions, upsertSession, rememberSession,
    knownSessionSummary, hasListedSession, patchSessionInList,
    updateKnownSessionTitle, removeSessionFromList, touchSessionInList,
    touchKnownSession, fallbackSessionAfterDelete, markSessionDeleted,
    clearDeletedSessionIds, clearRememberedSessions,
  } = sessionList
  const {
    workdirSessionsFor, workdirSessionsState, ensureWorkdirSessions,
    loadMoreWorkdirSessions, reset: resetWorkdirSessions,
  } = createWorkdirSessions({
    currentBotId, sessions, rememberSession,
    userScopeGeneration: () => userScopeGeneration,
    knownSession: knownSessionSummary,
  })
  const { applySessionsSnapshot } = createSessionListSnapshot({
    replaceSessions, sessionsCursor, hasMoreSessions,
  })
  const refreshCoordinator = createChatRefreshCoordinator({
    currentBotId,
    fetchSessions,
    currentSessionListRevision,
    applySessionsSnapshot,
  })
  const { refreshSessionsList, resetRefreshCoordinator } = refreshCoordinator
  const {
    ensureSessionSummary, ensureVisibleSessionSummary, loadMoreSessions,
    handleActivity: handleBotSessionsActivityEvent,
    reset: resetSessionActivity,
  } = createSessionActivity({
    currentBotId,
    sessionId,
    userScopeGeneration: () => userScopeGeneration,
    currentSessionListRevision,
    currentSelectRequest: () => selectSessionRequestId,
    knownSession: knownSessionSummary,
    rememberSession,
    sessionsCursor,
    hasMoreSessions,
    loadingMoreSessions,
    appendSessions,
    hasListedSession,
    touchKnownSession,
    updateKnownSessionTitle,
    refreshSessionsList,
  })
  // `loadingChats` covers the bot-level boot path (sessions list fetch), so
  // the sidebar can show its skeleton + suppress its empty-state placeholder
  // exactly while the sessions list is in flight.
  // `loadingMessages` covers the per-session transcript fetch — the sidebar
  // never reacts to it, only the chat pane uses it to keep its own empty
  // placeholders hidden while a fresh transcript is on its way.
  const {
    bots, ensureBot, refreshBots, reset: resetBots,
  } = createChatBots({
    currentBotId,
    userScopeGeneration: () => userScopeGeneration,
  })
  const overrideModelId = ref<string>('')
  const overrideReasoningEffort = ref<string>('')
  const {
    activeFailure: startupSendFailure,
    failureFor: startupSendFailureFor,
    remember: rememberStartupSendFailure,
    clear: clearStartupSendFailure,
    reset: resetStartupSendFailures,
  } = createStartupSendFailures({
    currentBotId,
    focusedViewId: focusedChatViewId,
    normalizeTarget: normalizedChatViewTarget,
  })
  const commandEventRegistry = createCommandEventRegistry({ currentBotId, sessionId })
  const {
    commandEvent, commandEventForScope, rememberCommandEvent, showCommandError,
    clearCommandEvent, rescopeSessionCommandEventToComposer, resetCommandEvents,
  } = commandEventRegistry
  const userSentInSession = ref<{
    id: string
    botId: string
    viewId: string
    wasDraft: boolean
    seq: number
  } | null>(null)
  let userSendSeq = 0
  const { realtime, decisions, integration: runtimeIntegration } =
    createChatRuntimeLayer({
    currentBotId,
    sessionId,
    focusedViewId: focusedChatViewId,
    assistantStreams,
    sessionList,
    chatViews,
    bumpProjectionVersion: () => { runtimeProjectionVersion.value += 1 },
    normalizeTarget: normalizedChatViewTarget,
    promoteDraftView: promoteDraftChatView,
    recordUserSent: (botId, targetSessionId, viewId, wasDraft) => {
      userSentInSession.value = {
        id: targetSessionId,
        botId,
        viewId,
        wasDraft,
        seq: ++userSendSeq,
      }
    },
    rescopeSessionCommandEventToComposer,
    rememberCommandEvent,
    removeTurnFromSession,
    hasVisibleAssistantBlocks,
    finalizeStreamFailure,
    refreshCurrentSession,
    resyncRuntimeTranscript,
    releaseHiddenSessionView: (botId, targetSessionId) => {
      releaseHiddenSessionView(chatViews.getSession(botId, targetSessionId) ?? null)
    },
    loadInitialMessages,
    reattachTurnToSession,
    sendFailedMessage,
    touchSessionInList,
    transcriptForTarget,
    createControlId: createInvocationId,
    connectionLostMessage: userInputConnectionLostMessage,
    resolveErrorMessage: resolveApiErrorMessage,
    showError: message => toast.error(message),
    onBotSessionsActivityEvent: handleBotSessionsActivityEvent,
  })
  const {
    startWebSocket,
    stopWebSocket,
    ensureWebSocket,
    sendWebSocketMessage,
    startSessionRuntime,
    stopSessionRuntime,
    startBotSessionsActivityStream,
    stopStreams,
  } = realtime
  const {
    respondToolApproval,
    respondUserInput,
    reset: resetDecisions,
  } = decisions
  const {
    guiToolUseRequested,
    abort,
    abortAllAssistantStreams,
  } = runtimeIntegration

  const hasExplicitSessionSelection = computed(() => explicitSessionSelection.value)
  const acp = createACPController({
    currentBotId,
    sessionId,
    draftIntent,
    explicitSessionSelection,
    focusedViewId: focusedChatViewId,
    userScopeGeneration: () => userScopeGeneration,
    bumpSelectSessionRequest: () => ++selectSessionRequestId,
    currentSelectSessionRequest: () => selectSessionRequestId,
    normalizeTarget: normalizedChatViewTarget,
    isFocusedTarget: isFocusedChatTarget,
    chatView,
    draftCreationKey: draftSessionCreationKey,
    draftSessionCreations,
    stopSessionRuntime,
    clearHistoryView,
    resetWorkspaceTargetSelection,
    upsertSession,
    rememberSession,
    promoteDraftView: promoteDraftChatView,
    markSessionDeleted,
    removeSessionView: (botId, targetSessionId) => {
      chatViews.removeSession(botId, targetSessionId)
    },
    removeSessionFromList,
    ensureBot,
    knownSession: knownSessionSummary,
    draftWorkdirBindingFor: botId => workdirsStore.sessionWorkdirBindingFor(botId),
    resolveDraftWorkdirIdFor: botId => workdirsStore.resolveSessionWorkdirIdFor(botId),
  })
  const {
    acpRuntimeStatuses, acpRuntimePending, acpRuntimeKey, clearACPRuntimeStatus, ensureACPRuntime,
    refreshACPRuntimeFor,
    setACPRuntimeMode, setACPRuntimeModel, setACPRuntimeReasoning,
  } = acp.runtimeRegistry
  const { cacheDefaultACPSession, clearPendingACPSession } = acp.staging
  const {
    pendingACPSessionInput, pendingACPRuntimeId, pendingACPSessionMetadata,
    pendingACPRuntimeStatus, pendingACPRuntimeEnsuring, pendingACPStateFor,
    pendingACPMatchesInput,
    stageACPSession, stageDefaultACPSession, resetToEmptyComposer,
    ensurePendingACPRuntime, setPendingACPModel, setPendingACPMode, setPendingACPReasoning,
    saveLiveDraftACPStage, activateDraftACPStage, discardEvictedDraft,
  } = acp.orchestration
  const {
    settingsForAgent: defaultACPSettingsForAgent,
    defaultRuntimeIsACP,
    stageFromSettings: stageDefaultACPFromSettings,
  } = acp.defaults
  const {
    createACPSession, updateCurrentSessionAgent,
    updateCurrentSessionToMemoh, ensureChatViewSession,
  } = acp.sessions
  const {
    draftViewRequested, applyDraftViewRequest, requestDraftView,
    invalidateDraftViewCommand, beginDraftViewCommand,
    reset: resetACP,
  } = acp
  configureChatViews({
    runtimeProjection: realtime.runtimeProjection,
    startSessionRuntime,
    stopSessionRuntime,
    discardDraft: discardEvictedDraft,
    invalidateDraftCommand: invalidateDraftViewCommand,
    saveDraftACP: saveLiveDraftACPStage,
    activateDraftACP: activateDraftACPStage,
    refreshAppliedHook: (_view, targetSessionId, latestTimestamp) => {
      touchSessionInList(targetSessionId, latestTimestamp)
    },
    ensureVisibleSummary: (botId, targetSessionId) => {
      void ensureVisibleSessionSummary(botId, targetSessionId)
    },
  })
  const {
    targetFor: chatTargetFor,
    activeTarget: activeChatTarget,
    readOnlyFor: chatReadOnlyFor,
    canForkFor: chatCanForkFor,
  } = createChatTargets({
    currentBotId,
    focusedViewId: focusedChatViewId,
    explicitSessionSelection,
    normalizeTarget: normalizedChatViewTarget,
    knownSession: knownSessionSummary,
    pendingACPState: pendingACPStateFor,
  })


  function resetUserScopedState(options: { clearSelection?: boolean } = {}) {
    userScopeGeneration += 1
    stopStreams()
    abortAllAssistantStreams()
    stopWebSocket()

    resetRefreshCoordinator()

    replaceSessions([])
    clearDeletedSessionIds()
    sessionsCursor.value = null
    hasMoreSessions.value = false
    loadingMoreSessions.value = false
    resetWorkdirSessions()
    resetBots()
    sessionId.value = null
    explicitSessionSelection.value = false
    if (options.clearSelection && currentBotId.value) {
      currentBotId.value = null
    }
    resetTranscriptUserScope()
    resetACP()
    resetBootstrap()
    overrideModelId.value = ''
    overrideReasoningEffort.value = ''
    resetStartupSendFailures()
    resetSessionActions()
    resetSessionActivity()
    draftSessionCreations.clear()
    resetCommandEvents()
    resetFsBeacon()

    clearStreamHistory()
    resetDecisions()
    backgroundTasks.clearBackgroundTasks()
    guiToolUseRequested.value = null
  }

  const {
    loadingChats,
    initialize,
    switchActiveSession,
    selectBot,
    selectSession,
    createNewSession,
    selectDraft,
    reset: resetBootstrap,
  } = createChatBootstrap({
    currentBotId,
    sessionId,
    draftIntent,
    explicitSessionSelection,
    userScopeGeneration: () => userScopeGeneration,
    bumpSelectSessionRequest: () => ++selectSessionRequestId,
    currentSelectSessionRequest: () => selectSessionRequestId,
    prepareForInitialization,
    resetRefreshCoordinator,
    resetSessionActivity,
    stopStreams,
    stopWebSocket,
    ensureBot,
    replaceSessions,
    sessionsCursor,
    hasMoreSessions,
    defaultRuntimeIsACP,
    ensureSessionSummary,
    pendingACPSessionInput,
    clearPendingACPSession,
    clearHistoryView,
    markHistoryEmpty,
    knownSessionSummary,
    startWebSocket,
    startBotSessionsActivityStream,
    startSessionRuntime,
    releasePreviousSession: (botId, targetSessionId) => {
      releaseHiddenSessionView(chatViews.getSession(botId, targetSessionId) ?? null)
    },
    abort,
    abortAllAssistantStreams,
    clearFsForBotSwitch,
    // Folder rows resolve through the remembered-session map, so dropping it
    // must drop the folders' paging state too, or a folder reads as empty.
    clearRememberedSessions: () => { clearRememberedSessions(); resetWorkdirSessions() },
    resetToEmptyComposer,
    stageDefaultACPFromSettings,
  })
  const {
    deletedSession,
    forkedSessionRequested,
    cleanupFailedDeferredSession,
    removeSession,
    renameSession,
    forkTurn,
    reset: resetSessionActions,
  } = createSessionActions({
    currentBotId,
    sessionId,
    draftIntent,
    explicitSessionSelection,
    focusedViewId: focusedChatViewId,
    userScopeGeneration: () => userScopeGeneration,
    normalizeTarget: normalizedChatViewTarget,
    isFocusedTarget: isFocusedChatTarget,
    chatView,
    readOnlyFor: chatReadOnlyFor,
    canForkFor: chatCanForkFor,
    isStreaming: target => isChatViewStreaming(target),
    abort: target => abort(target),
    stopSessionRuntime,
    clearRuntimeStatus: clearACPRuntimeStatus,
    removeSessionView: (botId, targetSessionId) => {
      chatViews.removeSession(botId, targetSessionId)
    },
    pruneViews: chatViews.prune,
    clearHistoryView,
    markSessionDeleted,
    removeSessionFromList,
    fallbackSessionAfterDelete,
    switchActiveSession,
    patchSessionInList,
    upsertSession,
    rememberSession,
    refreshSessionsList,
    fetchSessionWindow,
    replaceSessionHistory: (botId, targetSessionId, turns) => {
      sessionTranscript(botId, targetSessionId)
        .replaceHistoryView(turns, targetSessionId)
    },
    rescopeCommandToComposer: rescopeSessionCommandEventToComposer,
    forkFailedMessage,
  })

  bindBotIdInitializeWatch({
    currentBotId, initialize, resetUserScopedState,
  })
  // First-open recovery for the no-bot-selected entry (bare home route); the
  // watch above only fires once a bot id exists.
  const { initializeWithRecovery } = createInitializeRecovery({
    currentBotId, initialize,
  })

  const stopAuthSessionListener = onAuthSessionCleared(() => {
    resetUserScopedState({ clearSelection: true })
  })
  onScopeDispose(() => {
    stopAuthSessionListener()
    stopStreams()
    stopWebSocket()
  })

  const {
    handleWebNewCommand,
    isWebSlashInput,
    quickActionIDForSlash,
    handleWebSlashCommand,
  } = createChatCommands({
    currentBotId,
    userScopeGeneration: () => userScopeGeneration,
    isFocusedTarget: isFocusedChatTarget,
    beginDraftCommand: beginDraftViewCommand,
    requestDraftView,
    ensureBot,
    defaultACPSettingsForAgent,
    normalizeTarget: normalizedChatViewTarget,
    chatTargetFor,
    commandErrorMessage,
    showCommandError,
    rememberCommandEvent,
    refreshACPRuntime: refreshACPRuntimeFor,
  })

  const { sendMessage, retryLatestAssistant, editLatestUser } = createChatSend({
    currentBotId,
    sessionId,
    focusedChatViewId,
    overrideModelId,
    overrideReasoningEffort,
    normalizeTarget: normalizedChatViewTarget,
    chatView,
    transcriptForTarget,
    isWebSlashInput,
    quickActionIDForSlash,
    isACPTarget: target => chatTargetFor(normalizedChatViewTarget(target)).isACP,
    handleWebNewCommand,
    handleWebSlashCommand,
    commandErrorMessage,
    showCommandError,
    clearCommandEvent,
    chatReadOnlyFor,
    isChatViewStreaming,
    isChatViewCreatingSession,
    pendingACPStateFor,
    ensureChatViewSession,
    startSessionRuntime,
    recordUserSent: (target, targetSessionId, wasDraft) => {
      userSentInSession.value = {
        id: targetSessionId,
        botId: target.botId,
        viewId: target.viewId,
        wasDraft,
        seq: ++userSendSeq,
      }
    },
    ensureWebSocket,
    trackAssistantStream,
    sendWebSocketMessage,
    createdSessionIdForInvocation,
    forgetCreatedSession,
    refreshCurrentSession,
    hasVisibleAssistantBlocks,
    finalizeStreamFailure,
    removeTurnFromSession,
    cleanupFailedDeferredSession,
    discardAssistantStream,
    rememberStartupSendFailure,
    sendFailedMessage,
    updateForkAnchorForReplacedMessage,
    restoreTailFromOptimistic,
  })

  return {
    messages, chatView, bindChatView, setChatViewVisible, unbindChatView,
    focusChatView, promoteDraftChatView, chatTargetFor,
    workspaceTargetSelectionFor, setWorkspaceTargetSelection,
    initializeWorkspaceTargetSelection, resetWorkspaceTargetSelection,
    chatReadOnlyFor, chatCanForkFor, isChatViewStreaming,
    isChatViewCreatingSession, streaming, streamingSessionId,
    sessions, sessionsCursor, hasMoreSessions, loadingMoreSessions,
    loadMoreSessions, activeSession, knownSessions, knownSessionSummary,
    workdirSessionsFor, workdirSessionsState,
    ensureWorkdirSessions, loadMoreWorkdirSessions,
    activeChatReadOnly, activeChatCanFork,
    acpRuntimeStatuses, acpRuntimePending, pendingACPSessionInput,
    pendingACPSessionMetadata, pendingACPRuntimeId, pendingACPRuntimeStatus,
    pendingACPRuntimeEnsuring, pendingACPStateFor,
    pendingACPMatchesInput,
    sessionId, hasExplicitSessionSelection, currentBotId, bots,
    activeChatTarget, isSessionStreaming,
    loadingChats, loadingMessages, loadingOlder, hasMoreOlder,
    // Exposed for tests only — do not branch on this in components. The
    // leading underscore reflects the test-only contract at the call site.
    _hasLoadedOlder: hasLoadedOlder,
    overrideModelId, overrideReasoningEffort,
    startupSendFailure, startupSendFailureFor,
    commandEvent, commandEventForScope, rememberCommandEvent, showCommandError,
    fsChangedAt, markFsChanged, affectsPath, fsEventForPath,
    initialize, initializeWithRecovery, refreshBots, selectBot, selectSession, createNewSession,
    selectDraft, userSentInSession, draftViewRequested, applyDraftViewRequest,
    forkedSessionRequested, guiToolUseRequested, deletedSession,
    stageACPSession, stageDefaultACPSession, cacheDefaultACPSession,
    resetToEmptyComposer, ensurePendingACPRuntime,
    setPendingACPModel, setPendingACPMode, setPendingACPReasoning, clearPendingACPSession,
    createACPSession, updateCurrentSessionAgent, updateCurrentSessionToMemoh,
    acpRuntimeKey, ensureACPRuntime, setACPRuntimeMode, setACPRuntimeModel, setACPRuntimeReasoning,
    removeSession, renameSession, forkTurn,
    sendMessage, retryLatestAssistant, editLatestUser,
    respondToolApproval, respondUserInput,
    loadOlderMessages, findMessageIdByExternalId, locateMessageByExternalId,
    clearStartupSendFailure, clearCommandEvent, abort,
  }
})
