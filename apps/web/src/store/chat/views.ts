import { computed, reactive, ref, type Ref } from 'vue'
import {
  fetchMessagesUI,
  locateMessageUI,
} from '@/composables/api/useChat'
import type { RuntimeProjectionState } from './runtime-projection'
import { isRuntimeRunActive } from './runtime-projection'
import { createAssistantStreamRegistry } from './assistant-streams'
import type { createTranscriptController } from './transcript'
import { createChatViewRegistry, type ChatViewEntry } from './view-registry'
import type {
  ChatMessage,
  ChatViewTarget,
  ChatWorkspaceTargetSelectionSource,
  ChatWorkspaceTargetSnapshot,
} from './types'

type Transcript = ReturnType<typeof createTranscriptController>

export interface ChatViewsDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  rememberBackgroundTask: Parameters<typeof createChatViewRegistry>[0]['rememberBackgroundTask']
  applyPendingBackgroundEventsToTool: Parameters<typeof createChatViewRegistry>[0]['applyPendingBackgroundEventsToTool']
  bumpFsChangedAtIfFsMutation: Parameters<typeof createChatViewRegistry>[0]['bumpFsChangedAtIfFsMutation']
}

export function createChatViews(deps: ChatViewsDeps) {
  const focusedViewId = ref('chat')
  let runtimeProjectionProbe: (sessionId: string) => RuntimeProjectionState | undefined =
    () => undefined
  let refreshAppliedHook: (
    view: ChatViewEntry,
    sessionId: string,
    latestTimestamp?: string,
  ) => void = () => {}
  let stopSessionRuntime: (botId: string, sessionId: string) => void = () => {}
  let startSessionRuntime: (botId: string, sessionId: string) => void = () => {}
  let discardDraft: (view: ChatViewEntry) => void = () => {}
  let invalidateDraftCommand: (target: ChatViewTarget) => void = () => {}
  let saveDraftExternalAgent: () => void = () => {}
  let activateDraftExternalAgent: (target: ChatViewTarget) => void = () => {}
  let ensureVisibleSummary: (botId: string, sessionId: string) => void = () => {}
  const projectionVersion = ref(0)

  const chatViews = createChatViewRegistry({
    rememberBackgroundTask: deps.rememberBackgroundTask,
    applyPendingBackgroundEventsToTool: deps.applyPendingBackgroundEventsToTool,
    bumpFsChangedAtIfFsMutation: deps.bumpFsChangedAtIfFsMutation,
    fetchMessages: fetchMessagesUI,
    locateMessage: locateMessageUI,
    isSessionStreaming: (botId, sessionId) => isSessionStreaming(botId, sessionId),
    isTurnLive: (sessionId, turnId) => {
      const run = runtimeProjectionProbe(sessionId)?.currentRunView
      return Boolean(
        run
        && run.turn_id === turnId
        && isRuntimeRunActive(run.status),
      )
    },
    onRefreshApplied: (view, sessionId, latestTimestamp) => {
      refreshAppliedHook(view, sessionId, latestTimestamp)
    },
    onEvict: (view) => {
      if (view.kind === 'session' && view.sessionId) {
        stopSessionRuntime(view.botId, view.sessionId)
      } else if (view.kind === 'draft') {
        discardDraft(view)
      }
    },
  })

  function normalizeTarget(target?: Partial<ChatViewTarget>): ChatViewTarget {
    const botId = (target?.botId ?? deps.currentBotId.value ?? '').trim()
      || '__unbound__'
    const targetSessionId = target && 'sessionId' in target
      ? target.sessionId?.trim() || null
      : deps.sessionId.value?.trim() || null
    const viewId = target?.viewId?.trim() || focusedViewId.value.trim() || 'chat'
    return { botId, sessionId: targetSessionId, viewId }
  }

  function isFocusedTarget(target: ChatViewTarget): boolean {
    const resolved = normalizeTarget(target)
    if (
      resolved.botId !== (deps.currentBotId.value ?? '').trim()
      || resolved.viewId !== focusedViewId.value
    ) return false
    const selectedSessionId = (deps.sessionId.value ?? '').trim()
    return resolved.sessionId
      ? selectedSessionId === resolved.sessionId
      : !selectedSessionId
  }

  const draftSessionCreations = reactive(new Set<string>())

  function draftCreationKey(target: ChatViewTarget): string {
    const resolved = normalizeTarget(target)
    return `${resolved.botId}\u0000${resolved.viewId}`
  }

  function isCreatingDraft(target: ChatViewTarget): boolean {
    const resolved = normalizeTarget(target)
    return !resolved.sessionId && draftSessionCreations.has(draftCreationKey(resolved))
  }

  function chatView(target?: Partial<ChatViewTarget>): ChatViewEntry {
    return chatViews.getOrCreate(normalizeTarget(target))
  }

  function transcriptForTarget(target?: Partial<ChatViewTarget>) {
    return chatView(target).transcript
  }

  function sessionTranscript(botId: string, sessionId: string) {
    return chatViews.getOrCreate({
      botId: botId.trim(),
      sessionId: sessionId.trim(),
      viewId: focusedViewId.value,
    }).transcript
  }

  function transcriptForTurn(turn: ChatMessage): Transcript | null {
    return chatViews.entries().find(view => view.transcript.messages.includes(turn))
      ?.transcript ?? null
  }

  const messages = computed(() => transcriptForTarget().visibleMessages.value)
  const loadingMessages = computed(() => transcriptForTarget().loadingMessages.value)
  const loadingOlder = computed(() => transcriptForTarget().loadingOlder.value)
  const hasMoreOlder = computed(() => transcriptForTarget().hasMoreOlder.value)
  const hasLoadedOlder = computed({
    get: () => transcriptForTarget().hasLoadedOlder.value,
    set: value => { transcriptForTarget().hasLoadedOlder.value = value },
  })

  const clearHistoryView = (...args: Parameters<Transcript['clearHistoryView']>) =>
    transcriptForTarget().clearHistoryView(...args)
  const prepareForInitialization = () => transcriptForTarget().prepareForInitialization()
  const markHistoryEmpty = () => transcriptForTarget().markHistoryEmpty()
  async function refreshCurrentSession(botId?: string, sessionId?: string) {
    const resolvedBotId = (botId ?? deps.currentBotId.value ?? '').trim()
    const resolvedSessionId = (sessionId ?? deps.sessionId.value ?? '').trim()
    if (!resolvedBotId || !resolvedSessionId) return
    await sessionTranscript(resolvedBotId, resolvedSessionId)
      .refreshCurrentSession(resolvedBotId, resolvedSessionId)
  }
  async function resyncRuntimeTranscript(
    botId: string,
    sessionId: string,
    runtimeTranscript: Parameters<Transcript['applyRuntimeTranscript']>[0],
  ) {
    const view = chatViews.getSession(botId, sessionId)
    if (!view) return
    await view.transcript.refreshCurrentSession(botId, sessionId, () => {
      view.transcript.applyRuntimeTranscript(runtimeTranscript)
    })
  }
  async function loadInitialMessages(
    botId: string,
    sessionId: string,
    commitInitialHistory: (applyHistory: () => void) => Promise<void>,
  ) {
    const view = chatViews.getOrCreate({
      botId,
      sessionId,
      viewId: focusedViewId.value,
    })
    await view.transcript.loadInitialMessages(botId, sessionId, commitInitialHistory)
    view.initialized = true
  }
  const fetchSessionWindow = (botId: string, sessionId: string) =>
    sessionTranscript(botId, sessionId).fetchSessionWindow(botId, sessionId)
  const loadOlderMessages = (target?: ChatViewTarget) =>
    transcriptForTarget(target).loadOlderMessages()
  const findMessageIdByExternalId = (
    externalMessageId: string,
    target?: ChatViewTarget,
  ) => transcriptForTarget(target).findMessageIdByExternalId(externalMessageId)
  const locateMessageByExternalId = (
    externalMessageId: string,
    target?: ChatViewTarget,
  ) => transcriptForTarget(target).locateMessageByExternalId(externalMessageId)

  const assistantStreams = createAssistantStreamRegistry({
    finishAssistantTurn: turn => { transcriptForTurn(turn)?.finishAssistantTurn(turn) },
  })

  function isSessionStreaming(
    botId: string | null | undefined,
    sessionId: string | null | undefined,
  ): boolean {
    void projectionVersion.value
    const resolvedBotId = (botId ?? '').trim()
    const resolvedSessionId = (sessionId ?? '').trim()
    if (
      !resolvedBotId
      || !resolvedSessionId
      || resolvedBotId !== (deps.currentBotId.value ?? '').trim()
    ) return false
    return isRuntimeRunActive(
      runtimeProjectionProbe(resolvedSessionId)?.currentRunView?.status,
    )
  }

  const streamingSessionId = computed(() => {
    void projectionVersion.value
    const botId = (deps.currentBotId.value ?? '').trim()
    const activeSessionId = (deps.sessionId.value ?? '').trim()
    if (activeSessionId && isSessionStreaming(botId, activeSessionId)) {
      return activeSessionId
    }
    return chatViews.entries().find(view =>
      view.kind === 'session'
      && view.botId === botId
      && view.sessionId
      && isSessionStreaming(botId, view.sessionId),
    )?.sessionId ?? null
  })

  const streaming = computed(() => {
    const botId = (deps.currentBotId.value ?? '').trim()
    const sessionId = (deps.sessionId.value ?? '').trim()
    return sessionId
      ? isSessionStreaming(botId, sessionId)
      : assistantStreams.isUnboundComposerStreaming(botId)
  })

  function isChatViewStreaming(target: ChatViewTarget, composerScope?: string) {
    const resolved = normalizeTarget(target)
    return resolved.sessionId
      ? isSessionStreaming(resolved.botId, resolved.sessionId)
      : assistantStreams.isUnboundComposerStreaming(
          resolved.botId,
          composerScope?.trim() || `${resolved.botId}:${resolved.viewId}`,
        )
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
    const currentSource = chatView(target).workspaceTargetSelectionSource.value
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

  function releaseHiddenSessionView(view: ChatViewEntry | null) {
    if (!view || view.kind !== 'session' || !view.sessionId) return
    if (view.visiblePanelIds.size > 0) return
    // A hidden view with an active run still needs its runtime subscription to
    // settle the send promise and collect terminal state. The terminal
    // projection calls this again, at which point it is safe to unsubscribe.
    if (isSessionStreaming(view.botId, view.sessionId)) return
    if (assistantStreams.assistantStreamsForSession(view.botId, view.sessionId).length > 0) {
      return
    }
    stopSessionRuntime(view.botId, view.sessionId)
    chatViews.prune()
  }

  function bindChatView(panelId: string, target: ChatViewTarget, visible = true) {
    const change = chatViews.bindPanel(panelId, normalizeTarget(target), visible)
    releaseHiddenSessionView(change.deactivatedSession)
    if (change.activatedSession?.sessionId) {
      startSessionRuntime(
        change.activatedSession.botId,
        change.activatedSession.sessionId,
      )
    }
    if (visible && change.view.kind === 'session' && change.view.sessionId) {
      ensureVisibleSummary(change.view.botId, change.view.sessionId)
    }
    if (change.view.kind === 'draft' && focusedViewId.value === change.view.viewId) {
      activateDraftExternalAgent({
        botId: change.view.botId,
        sessionId: null,
        viewId: change.view.viewId,
      })
    }
    return change.view
  }

  function setChatViewVisible(panelId: string, visible: boolean) {
    const change = chatViews.setPanelVisible(panelId, visible)
    if (!change) return
    releaseHiddenSessionView(change.deactivatedSession)
    if (change.activatedSession?.sessionId) {
      startSessionRuntime(change.activatedSession.botId, change.activatedSession.sessionId)
    }
    if (visible && change.view.kind === 'session' && change.view.sessionId) {
      ensureVisibleSummary(change.view.botId, change.view.sessionId)
    }
  }

  function focusChatView(viewId: string) {
    const id = viewId.trim()
    if (!id || id === focusedViewId.value) return
    saveDraftExternalAgent()
    focusedViewId.value = id
    const view = chatViews.getPanel(id)
    if (view?.kind === 'draft') {
      activateDraftExternalAgent({ botId: view.botId, sessionId: null, viewId: view.viewId })
    }
  }

  function promoteDraftChatView(target: ChatViewTarget, sessionId: string) {
    invalidateDraftCommand(target)
    const promoted = chatViews.promoteDraft(target.botId, target.viewId, sessionId)
    if (promoted.visiblePanelIds.size > 0 && promoted.sessionId) {
      startSessionRuntime(promoted.botId, promoted.sessionId)
    }
    return promoted
  }

  function configure(options: {
    runtimeProjection: (sessionId: string) => RuntimeProjectionState | undefined
    startSessionRuntime: (botId: string, sessionId: string) => void
    stopSessionRuntime: (botId: string, sessionId: string) => void
    discardDraft: (view: ChatViewEntry) => void
    invalidateDraftCommand: (target: ChatViewTarget) => void
    saveDraftExternalAgent: () => void
    activateDraftExternalAgent: (target: ChatViewTarget) => void
    refreshAppliedHook: typeof refreshAppliedHook
    ensureVisibleSummary: (botId: string, sessionId: string) => void
  }) {
    runtimeProjectionProbe = options.runtimeProjection
    startSessionRuntime = options.startSessionRuntime
    stopSessionRuntime = options.stopSessionRuntime
    discardDraft = options.discardDraft
    invalidateDraftCommand = options.invalidateDraftCommand
    saveDraftExternalAgent = options.saveDraftExternalAgent
    activateDraftExternalAgent = options.activateDraftExternalAgent
    refreshAppliedHook = options.refreshAppliedHook
    ensureVisibleSummary = options.ensureVisibleSummary
  }

  return {
    focusedViewId,
    projectionVersion,
    chatViews,
    assistantStreams,
    draftSessionCreations,
    draftCreationKey,
    isCreatingDraft,
    normalizeTarget,
    isFocusedTarget,
    chatView,
    transcriptForTarget,
    sessionTranscript,
    transcriptForTurn,
    messages,
    loadingMessages,
    loadingOlder,
    hasMoreOlder,
    hasLoadedOlder,
    clearHistoryView,
    prepareForInitialization,
    markHistoryEmpty,
    refreshCurrentSession,
    resyncRuntimeTranscript,
    loadInitialMessages,
    fetchSessionWindow,
    loadOlderMessages,
    findMessageIdByExternalId,
    locateMessageByExternalId,
    isSessionStreaming,
    streamingSessionId,
    streaming,
    isChatViewStreaming,
    workspaceTargetSelectionFor,
    setWorkspaceTargetSelection,
    initializeWorkspaceTargetSelection,
    resetWorkspaceTargetSelection,
    releaseHiddenSessionView,
    bindChatView,
    setChatViewVisible,
    unbindChatView: (panelId: string) =>
      releaseHiddenSessionView(chatViews.unbindPanel(panelId)),
    focusChatView,
    promoteDraftChatView,
    configure,
    reset: () => chatViews.resetAll(),
  }
}
