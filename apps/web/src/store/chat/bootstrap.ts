import { ref, type Ref } from 'vue'
import { fetchSessions, type SessionSummary } from '@/composables/api/useChat'
import type { ExternalAgentSessionInput } from './types'

export interface ChatBootstrapDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  draftIntent: Ref<boolean>
  explicitSessionSelection: Ref<boolean>
  userScopeGeneration: () => number
  bumpSelectSessionRequest: () => number
  currentSelectSessionRequest: () => number
  prepareForInitialization: () => void
  resetRefreshCoordinator: () => void
  resetSessionActivity: () => void
  stopStreams: () => void
  stopWebSocket: () => void
  ensureBot: () => Promise<string | null>
  replaceSessions: (sessions: SessionSummary[]) => SessionSummary[]
  sessionsCursor: Ref<string | null>
  hasMoreSessions: Ref<boolean>
  defaultRuntimeIsExternalAgent: (botId: string) => Promise<boolean>
  ensureSessionSummary: (
    botId: string,
    sessionId: string,
    requestId?: number,
  ) => Promise<SessionSummary | null>
  pendingExternalAgentSessionInput: Ref<ExternalAgentSessionInput | null>
  clearPendingExternalAgentSession: () => void
  clearHistoryView: () => void
  markHistoryEmpty: () => void
  knownSessionSummary: (sessionId: string) => SessionSummary | null | undefined
  startWebSocket: (botId: string) => void
  startBotSessionsActivityStream: (botId: string) => void
  startSessionRuntime: (botId: string, sessionId: string) => void
  releasePreviousSession: (botId: string, sessionId: string) => void
  abort: () => void
  abortAllAssistantStreams: () => void
  clearFsForBotSwitch: () => void
  clearRememberedSessions: () => void
  resetToEmptyComposer: (options: {
    clearPendingExternalAgent?: boolean
    explicitSelection?: boolean
    draftIntent?: boolean
  }) => void
  stageDefaultExternalAgentFromSettings: (requestId: number) => Promise<void>
}

export function createChatBootstrap(deps: ChatBootstrapDeps) {
  const loadingChats = ref(false)
  const initializing = ref(false)
  let rerunRequested = false
  let initializingBotId: string | null = null
  let initializePromise: Promise<void> | null = null

  async function initialize() {
    if (initializing.value) {
      const requestedBotId = (deps.currentBotId.value ?? '').trim() || null
      if (initializingBotId && requestedBotId !== initializingBotId) rerunRequested = true
      if (initializePromise) await initializePromise
      return
    }

    const generation = deps.userScopeGeneration()
    // The promise clears its own slot in finally, so it must be assigned after
    // construction while remaining visible to that closure.
    let run!: Promise<void>
    // eslint-disable-next-line prefer-const
    run = (async () => {
      initializing.value = true
      loadingChats.value = true
      try {
        do {
          if (generation !== deps.userScopeGeneration()) return
          rerunRequested = false
          initializingBotId = (deps.currentBotId.value ?? '').trim() || null
          deps.prepareForInitialization()
          deps.resetRefreshCoordinator()
          deps.resetSessionActivity()
          deps.stopStreams()
          deps.stopWebSocket()

          const botId = await deps.ensureBot()
          if (generation !== deps.userScopeGeneration()) return
          if (!botId) {
            deps.replaceSessions([])
            deps.sessionsCursor.value = null
            deps.hasMoreSessions.value = false
            deps.sessionId.value = null
            deps.clearPendingExternalAgentSession()
            deps.clearHistoryView()
            continue
          }
          initializingBotId = botId

          let response: Awaited<ReturnType<typeof fetchSessions>>
          let defaultIsExternalAgent = false
          try {
            ;[response, defaultIsExternalAgent] = await Promise.all([
              fetchSessions(botId),
              deps.defaultRuntimeIsExternalAgent(botId),
            ])
          } catch (error) {
            if (generation !== deps.userScopeGeneration()) return
            if ((deps.currentBotId.value ?? '').trim() !== botId) {
              rerunRequested = true
              continue
            }
            throw error
          }
          if (generation !== deps.userScopeGeneration()) return
          if ((deps.currentBotId.value ?? '').trim() !== botId) {
            rerunRequested = true
            continue
          }

          const visibleSessions = deps.replaceSessions(response.items)
          deps.sessionsCursor.value = response.nextCursor
          deps.hasMoreSessions.value = response.nextCursor !== null

          const restoredSessionId = (deps.sessionId.value ?? '').trim()
          const restoredExplicitSession = restoredSessionId
            && deps.explicitSessionSelection.value
            ? await deps.ensureSessionSummary(botId, restoredSessionId)
            : null
          if (generation !== deps.userScopeGeneration()) return
          if ((deps.currentBotId.value ?? '').trim() !== botId) {
            rerunRequested = true
            continue
          }
          const preservePendingExternalAgentStage = !!deps.pendingExternalAgentSessionInput.value
            && !deps.sessionId.value
          const preserveExplicitEmptyComposer = deps.explicitSessionSelection.value
            && !deps.sessionId.value
          const preferDefaultExternalAgent = defaultIsExternalAgent
            && !preservePendingExternalAgentStage
            && !preserveExplicitEmptyComposer
            && !deps.explicitSessionSelection.value

          if (preservePendingExternalAgentStage) {
            deps.sessionId.value = null
            deps.markHistoryEmpty()
          } else if (preserveExplicitEmptyComposer) {
            deps.sessionId.value = null
            deps.clearHistoryView()
          } else if (preferDefaultExternalAgent) {
            deps.sessionId.value = null
            deps.explicitSessionSelection.value = false
            deps.draftIntent.value = false
            deps.clearHistoryView()
          } else if (restoredExplicitSession) {
            deps.draftIntent.value = false
          } else if (!visibleSessions.length) {
            deps.sessionId.value = null
            deps.explicitSessionSelection.value = false
            deps.clearHistoryView()
          } else if (
            deps.sessionId.value
            && deps.knownSessionSummary(deps.sessionId.value)
          ) {
            deps.draftIntent.value = false
          } else if (deps.draftIntent.value) {
            deps.sessionId.value = null
            deps.clearHistoryView()
          } else {
            const firstConversation = visibleSessions.find(
              session => (session.type ?? 'chat') !== 'schedule',
            )
            deps.sessionId.value = (firstConversation ?? visibleSessions[0]!).id
            deps.explicitSessionSelection.value = false
          }

          deps.startWebSocket(botId)
          deps.startBotSessionsActivityStream(botId)
          if (deps.sessionId.value) {
            deps.startSessionRuntime(botId, deps.sessionId.value)
          }
        } while (rerunRequested)
      } finally {
        if (generation === deps.userScopeGeneration()) {
          loadingChats.value = false
          initializing.value = false
          initializingBotId = null
          rerunRequested = false
        }
        if (initializePromise === run) initializePromise = null
      }
    })()
    initializePromise = run
    await run
  }

  function switchActiveSession(sessionId: string, previousSessionId = '') {
    const botId = (deps.currentBotId.value ?? '').trim()
    if (!botId || !sessionId) return
    const previous = previousSessionId.trim()
    if (previous && previous !== sessionId) {
      deps.releasePreviousSession(botId, previous)
    }
    deps.startSessionRuntime(botId, sessionId)
  }

  async function selectBot(targetBotId: string) {
    if (deps.currentBotId.value === targetBotId) return
    deps.bumpSelectSessionRequest()
    deps.abort()
    deps.abortAllAssistantStreams()
    deps.clearPendingExternalAgentSession()
    deps.clearFsForBotSwitch()
    deps.resetSessionActivity()
    deps.replaceSessions([])
    deps.sessionsCursor.value = null
    deps.hasMoreSessions.value = false
    deps.currentBotId.value = targetBotId
    deps.sessionId.value = null
    deps.clearRememberedSessions()
    deps.explicitSessionSelection.value = false
    deps.draftIntent.value = false
    await initialize()
  }

  async function selectSession(
    targetSessionId: string,
    options: { explicitSelection?: boolean } = {},
  ) {
    const sessionId = targetSessionId.trim()
    if (!sessionId) return
    const previousSessionId = (deps.sessionId.value ?? '').trim()
    const sameSession = sessionId === previousSessionId
    const requestId = deps.bumpSelectSessionRequest()
    const botId = (deps.currentBotId.value ?? '').trim()
    deps.clearPendingExternalAgentSession()
    deps.sessionId.value = sessionId
    deps.draftIntent.value = false
    deps.explicitSessionSelection.value = options.explicitSelection !== false
    if (!sameSession) switchActiveSession(sessionId, previousSessionId)
    await deps.ensureSessionSummary(botId, sessionId, requestId)
  }

  async function createNewSession(options: { explicitSelection?: boolean } = {}) {
    const botId = await deps.ensureBot()
    if (!botId) return
    deps.resetToEmptyComposer({
      explicitSelection: options.explicitSelection === true,
      draftIntent: true,
    })
  }

  function selectDraft(options: { explicitSelection?: boolean } = {}) {
    const explicitSelection = options.explicitSelection === true
    deps.resetToEmptyComposer({
      clearPendingExternalAgent: false,
      explicitSelection,
      draftIntent: true,
    })
    if (!explicitSelection) {
      void deps.stageDefaultExternalAgentFromSettings(deps.currentSelectSessionRequest())
    }
  }

  function reset() {
    loadingChats.value = false
    initializing.value = false
    rerunRequested = false
    initializingBotId = null
    initializePromise = null
  }

  return {
    loadingChats,
    initialize,
    switchActiveSession,
    selectBot,
    selectSession,
    createNewSession,
    selectDraft,
    reset,
  }
}
