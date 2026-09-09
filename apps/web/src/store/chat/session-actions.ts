import { ref, type Ref } from 'vue'
import { toast } from '@felinic/ui'
import { resolveApiErrorMessage } from '@/utils/api-error'
import {
  deleteSession,
  forkSessionFromTurn,
  updateSessionTitle,
  type SessionSummary,
  type UITurn,
} from '@/composables/api/useChat'
import type { SidebarSessionMode } from '../chat-list.utils'
import type { ChatViewEntry } from './view-registry'
import type { ChatViewTarget } from './types'

export function createSessionActions(deps: {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  draftIntent: Ref<boolean>
  explicitSessionSelection: Ref<boolean>
  focusedViewId: Ref<string>
  userScopeGeneration: () => number
  normalizeTarget: (target?: Partial<ChatViewTarget>) => ChatViewTarget
  isFocusedTarget: (target: ChatViewTarget) => boolean
  chatView: (target?: Partial<ChatViewTarget>) => ChatViewEntry
  readOnlyFor: (target: ChatViewTarget) => boolean
  canForkFor: (target: ChatViewTarget) => boolean
  isStreaming: (target: ChatViewTarget) => boolean
  abort: (target: ChatViewTarget) => void
  stopSessionRuntime: (botId: string, sessionId: string) => void
  clearRuntimeStatus: (botId: string, sessionId: string) => void
  removeSessionView: (botId: string, sessionId: string) => void
  pruneViews: () => void
  clearHistoryView: () => void
  markSessionDeleted: (botId: string, sessionId: string) => void
  removeSessionFromList: (sessionId: string) => void
  fallbackSessionAfterDelete: (mode: SidebarSessionMode) => SessionSummary | null
  switchActiveSession: (sessionId: string, previousSessionId?: string) => void
  patchSessionInList: (sessionId: string, patch: Partial<SessionSummary>) => void
  upsertSession: (session: SessionSummary) => void
  rememberSession: (session: SessionSummary) => void
  refreshSessionsList: (botId: string) => Promise<void>
  fetchSessionWindow: (botId: string, sessionId: string) => Promise<UITurn[]>
  replaceSessionHistory: (botId: string, sessionId: string, turns: UITurn[]) => void
  rescopeCommandToComposer: (botId: string, sessionId: string) => string
  forkFailedMessage: () => string
}) {
  const deletedSession = ref<{
    id: string
    botId: string
    seq: number
    composerScope?: string
  } | null>(null)
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
  const forkingTurns = new Set<string>()
  let deletedSessionSeq = 0
  let forkedSessionRequestSeq = 0

  function recordDeleted(botId: string, sessionId: string, composerScope = '') {
    const signal: NonNullable<typeof deletedSession.value> = {
      id: sessionId,
      botId,
      seq: ++deletedSessionSeq,
    }
    if (composerScope) signal.composerScope = composerScope
    deletedSession.value = signal
  }

  async function cleanupFailedDeferredSession(
    botId: string,
    sessionId: string,
    fallbackComposerScope = '',
  ) {
    const bid = botId.trim()
    const sid = sessionId.trim()
    if (!bid || !sid) return
    const composerScope = deps.rescopeCommandToComposer(bid, sid)
      || fallbackComposerScope.trim()
    deps.markSessionDeleted(bid, sid)
    recordDeleted(bid, sid, composerScope)
    deps.clearRuntimeStatus(bid, sid)
    deps.stopSessionRuntime(bid, sid)
    deps.removeSessionView(bid, sid)
    if ((deps.currentBotId.value ?? '').trim() === bid) {
      deps.removeSessionFromList(sid)
      if ((deps.sessionId.value ?? '').trim() === sid) {
        deps.sessionId.value = null
        deps.explicitSessionSelection.value = false
        deps.draftIntent.value = true
        deps.clearHistoryView()
      }
    }
    try {
      await deleteSession(bid, sid)
    } catch {
      // Best-effort cleanup; the original send failure remains user-facing.
    }
  }

  async function removeSession(
    sessionId: string,
    options: { fallbackMode?: SidebarSessionMode } = {},
  ) {
    const sid = sessionId.trim()
    if (!sid) return
    const botId = deps.currentBotId.value ?? ''
    if (!botId) throw new Error('Bot not selected')
    await deleteSession(botId, sid)
    deps.abort({ botId, sessionId: sid, viewId: deps.focusedViewId.value })
    deps.markSessionDeleted(botId, sid)
    recordDeleted(botId, sid)
    deps.stopSessionRuntime(botId, sid)
    deps.removeSessionView(botId, sid)
    if ((deps.currentBotId.value ?? '').trim() !== botId) return
    deps.clearRuntimeStatus(botId, sid)
    deps.removeSessionFromList(sid)
    if (deps.sessionId.value !== sid) return
    const next = deps.fallbackSessionAfterDelete(
      options.fallbackMode ?? 'recent',
    )
    if (!next) {
      deps.sessionId.value = null
      deps.explicitSessionSelection.value = false
      deps.draftIntent.value = false
      deps.clearHistoryView()
      return
    }
    deps.sessionId.value = next.id
    deps.explicitSessionSelection.value = false
    deps.draftIntent.value = false
    deps.switchActiveSession(next.id, sid)
  }

  async function renameSession(sessionId: string, title: string) {
    const sid = sessionId.trim()
    const nextTitle = title.trim()
    if (!sid) throw new Error('Session not selected')
    const botId = deps.currentBotId.value ?? ''
    if (!botId) throw new Error('Bot not selected')
    const updated = await updateSessionTitle(botId, sid, nextTitle)
    const patch: Partial<SessionSummary> = { title: updated.title ?? nextTitle }
    if (updated.updated_at) patch.updated_at = updated.updated_at
    deps.patchSessionInList(sid, patch)
    return updated
  }

  async function forkTurn(
    turnId: string,
    options: { title?: string; target?: ChatViewTarget } = {},
  ) {
    const target = deps.normalizeTarget(options.target)
    const botId = target.botId
    const sessionId = target.sessionId ?? ''
    const id = turnId.trim()
    const view = deps.chatView(target)
    const generation = deps.userScopeGeneration()
    const activate = deps.isFocusedTarget(target)
    if (
      !botId || !sessionId || !id
      || deps.readOnlyFor(target)
      || !deps.canForkFor(target)
      || deps.isStreaming(target)
      || view.transcript.loadingMessages.value
    ) return false

    const key = `${botId}:${sessionId}:${id}`
    if (forkingTurns.has(key)) return false
    forkingTurns.add(key)
    try {
      const forked = await forkSessionFromTurn(
        botId,
        sessionId,
        id,
        { title: options.title },
      )
      if (
        generation !== deps.userScopeGeneration()
        || (deps.currentBotId.value ?? '').trim() !== botId
      ) return true
      deps.upsertSession(forked)
      deps.rememberSession(forked)
      void deps.refreshSessionsList(botId)

      const turns = await deps.fetchSessionWindow(botId, forked.id)
      if (
        generation !== deps.userScopeGeneration()
        || (deps.currentBotId.value ?? '').trim() !== botId
      ) return true
      deps.replaceSessionHistory(botId, forked.id, turns)
      forkedSessionRequested.value = {
        botId,
        viewId: target.viewId,
        expectedSessionId: sessionId,
        sessionId: forked.id,
        title: (forked.title ?? options.title ?? '').trim(),
        explicitSelection: true,
        activate,
        seq: ++forkedSessionRequestSeq,
      }
      deps.pruneViews()
      return true
    } catch (error) {
      toast.error(resolveApiErrorMessage(error, deps.forkFailedMessage()))
      return false
    } finally {
      forkingTurns.delete(key)
    }
  }

  return {
    deletedSession,
    forkedSessionRequested,
    cleanupFailedDeferredSession,
    removeSession,
    renameSession,
    forkTurn,
    reset: () => {
      deletedSession.value = null
      forkedSessionRequested.value = null
      forkingTurns.clear()
    },
  }
}
