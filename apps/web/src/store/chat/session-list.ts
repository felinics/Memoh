import { computed, isRef, ref, type Ref } from 'vue'
import type { SessionSummary } from '@/composables/api/useChat.types'
import {
  isSessionVisibleInSidebarMode,
  sortByRecency,
  type SidebarSessionMode,
} from '../chat-list.utils'
import { messageIdentityId } from '../chat-list.normalize'
import type { ChatMessage } from './types'

// The factory owns the list state (reactive array + non-reactive O(1) map +
// remembered/deleted bookkeeping) and every local mutation. Anything that
// calls a transport (fetch/delete/rename) or orchestrates across other
// clusters stays in the chat store.

export interface SessionListDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  // The active transcript, read (never written) by fork-anchor relocation.
  messages: ChatMessage[] | Ref<ChatMessage[]>
}

export type SessionLookupSource = 'listed' | 'remembered' | 'unknown'

export interface SessionTouchResult {
  source: SessionLookupSource
  session: SessionSummary | null
  visibleInRecents: boolean
}

export function createSessionList({ currentBotId, sessionId, messages }: SessionListDeps) {
  const sessions = ref<SessionSummary[]>([])
  // O(1) lookup keeps event handlers off the list scan for bots with thousands
  // of sessions.
  const sessionById = new Map<string, SessionSummary>()
  const sessionLookupRevision = ref(0)
  const rememberedSessions = ref<Record<string, SessionSummary>>({})
  const deletedSessionIdsByBot = new Map<string, Set<string>>()
  const sessionsCursor = ref<string | null>(null)
  const hasMoreSessions = ref(false)
  const loadingMoreSessions = ref(false)
  const activeMessages = () => isRef(messages) ? messages.value : messages

  const activeSession = computed(() => knownSessionSummary(sessionId.value ?? ''))
  const knownSessions = computed<SessionSummary[]>(() => {
    const byId = new Map<string, SessionSummary>()
    for (const session of sessions.value) byId.set(session.id, session)
    for (const session of Object.values(rememberedSessions.value)) {
      if (!byId.has(session.id)) byId.set(session.id, session)
    }
    return [...byId.values()]
  })

  const activeChatReadOnly = computed(() => {
    const session = activeSession.value
    if (!session) return false
    const type = session.type ?? 'chat'
    if (type === 'schedule') return true
    const ct = (session.channel_type ?? '').trim().toLowerCase()
    if (ct && ct !== 'local') return true
    return false
  })

  const activeChatCanFork = computed(() => activeSession.value?.type === 'chat')

  function markSessionLookupChanged() {
    sessionLookupRevision.value++
  }

  function trackSessionLookupRevision() {
    return sessionLookupRevision.value
  }

  function currentSessionListRevision() {
    return sessionLookupRevision.value
  }

  // --- Fork-anchor tracking -------------------------------------------------
  // The anchor is stored as metadata.forked_from.fork_message_id on the
  // SessionSummary; there is no dedicated state beyond the session records.

  function forkSourceMetadata(session: SessionSummary | null | undefined): Record<string, unknown> | null {
    const metadata = session?.metadata
    if (!metadata || typeof metadata !== 'object') return null
    const raw = (metadata as Record<string, unknown>).forked_from
    return raw && typeof raw === 'object' ? raw as Record<string, unknown> : null
  }

  function forkMessageCandidates(message: ChatMessage): string[] {
    const out = [
      message.serverId,
      message.id,
      message.role === 'system' ? undefined : message.externalMessageId,
    ]
      .map(value => value?.trim() ?? '')
      .filter(Boolean)
    return Array.from(new Set(out))
  }

  function messageMatchesForkAnchor(message: ChatMessage, anchor: string): boolean {
    const target = anchor.trim()
    return Boolean(target) && forkMessageCandidates(message).includes(target)
  }

  function latestInheritedAssistantBefore(
    index: number,
    transcript = activeMessages(),
  ): string {
    for (let i = index - 1; i >= 0; i--) {
      const message = transcript[i]
      if (message?.role !== 'assistant') continue
      return messageIdentityId(message) || message.id
    }
    return ''
  }

  function updateForkAnchorForReplacedMessage(
    targetSessionId: string,
    target: ChatMessage,
    transcript = activeMessages(),
  ): (() => void) | null {
    const sid = targetSessionId.trim()
    if (!sid) return null
    const known = knownSessionSummary(sid)
    const fork = forkSourceMetadata(known)
    const currentAnchor = String(fork?.fork_message_id ?? '').trim()
    if (!known || !fork || !currentAnchor) return null

    const targetIndex = transcript.indexOf(target)
    if (targetIndex < 0) return null
    const replacedTailContainsAnchor = transcript
      .slice(targetIndex)
      .some(message => messageMatchesForkAnchor(message, currentAnchor))
    if (!replacedTailContainsAnchor) return null
    const nextAnchor = latestInheritedAssistantBefore(targetIndex, transcript)
    if (nextAnchor === currentAnchor) return null

    const metadata = known.metadata && typeof known.metadata === 'object' ? known.metadata : {}
    const nextFork = { ...fork }
    if (nextAnchor) {
      nextFork.fork_message_id = nextAnchor
    } else {
      delete nextFork.fork_message_id
    }
    const next = {
      ...known,
      metadata: {
        ...metadata,
        forked_from: nextFork,
      },
    }
    replaceKnownSessionSummary(next)
    rememberSession(next)
    return () => {
      replaceKnownSessionSummary(known)
      rememberSession(known)
    }
  }

  function preserveSessionSummary(incoming: SessionSummary, known?: SessionSummary | null): SessionSummary {
    if (known && !(incoming.title ?? '').trim() && (known.title ?? '').trim()) {
      return { ...incoming, title: known.title }
    }
    return incoming
  }

  // --- Session-list mutations ------------------------------------------------

  function replaceSessions(items: SessionSummary[]): SessionSummary[] {
    const currentDeleted = deletedSessionIdsByBot.get((currentBotId.value ?? '').trim())
    // A racing list refresh can fetch a session before the backend's
    // title-generation flow has persisted a title, while the client already
    // holds one — the optimistic provisional title set in ensureActiveSession,
    // which is local-only and never sent to the server. (Server-published
    // titles don't have this problem: applyFallbackTitle and the LLM path
    // both UpdateTitle before publishing, so the DB is current by the time any
    // client sees the SSE.) An empty title in the snapshot means "server
    // hasn't set one yet," not "title cleared," so preserve our non-empty title
    // instead of letting the refresh erase it (which split the sidebar from
    // the sticky tab title).
    const merged = items
      .filter(s => !currentDeleted?.has(s.id))
      .map(s => preserveSessionSummary(s, sessionById.get(s.id) ?? rememberedSessions.value[s.id]))
    sessions.value = merged
    sessionById.clear()
    for (const s of merged) sessionById.set(s.id, s)
    markSessionLookupChanged()
    return merged
  }

  function appendSessions(items: SessionSummary[]) {
    if (items.length === 0) return
    const currentDeleted = deletedSessionIdsByBot.get((currentBotId.value ?? '').trim())
    const fresh = items
      .filter(s => !sessionById.has(s.id) && !currentDeleted?.has(s.id))
      .map(s => preserveSessionSummary(s, rememberedSessions.value[s.id]))
    if (fresh.length === 0) return
    sessions.value = [...sessions.value, ...fresh]
    for (const s of fresh) sessionById.set(s.id, s)
    markSessionLookupChanged()
  }

  function upsertSession(updated: SessionSummary) {
    const currentDeleted = deletedSessionIdsByBot.get((currentBotId.value ?? '').trim())
    if (currentDeleted?.has(updated.id)) return
    const existing = sessionById.get(updated.id)
    const remembered = rememberedSessions.value[updated.id]
    const next = preserveSessionSummary(updated, existing ?? remembered)
    if (existing) {
      const rest = sessions.value.filter(session => session.id !== updated.id)
      sessions.value = [next, ...rest]
    } else {
      sessions.value = [next, ...sessions.value]
    }
    sessionById.set(next.id, next)
    markSessionLookupChanged()
    if (remembered) rememberSession(next)
  }

  function replaceKnownSessionSummary(updated: SessionSummary) {
    const currentDeleted = deletedSessionIdsByBot.get((currentBotId.value ?? '').trim())
    if (currentDeleted?.has(updated.id)) return
    if (sessionById.has(updated.id)) {
      sessions.value = sessions.value.map(session => (session.id === updated.id ? updated : session))
    }
    sessionById.set(updated.id, updated)
    markSessionLookupChanged()
  }

  function rememberSession(updated: SessionSummary) {
    const bid = (updated.bot_id ?? currentBotId.value ?? '').trim()
    if (deletedSessionIdsByBot.get(bid)?.has(updated.id)) return
    rememberedSessions.value = {
      ...rememberedSessions.value,
      [updated.id]: updated,
    }
    markSessionLookupChanged()
  }

  function forgetRememberedSession(id: string) {
    if (!rememberedSessions.value[id]) return
    const next = { ...rememberedSessions.value }
    delete next[id]
    rememberedSessions.value = next
    markSessionLookupChanged()
  }

  function knownSessionSummary(targetSessionId: string): SessionSummary | null {
    const sid = targetSessionId.trim()
    if (!sid) return null
    // `sessionById` is intentionally a non-reactive O(1) lookup. Tie computed
    // consumers such as activeChatTarget to a tiny revision so an early null
    // read is invalidated when the session list/hydration later fills the map.
    trackSessionLookupRevision()
    return sessionById.get(sid) ?? rememberedSessions.value[sid] ?? null
  }

  function hasListedSession(targetSessionId: string): boolean {
    return sessionById.has(targetSessionId.trim())
  }

  function isRecentsSession(session: SessionSummary): boolean {
    const type = (session.type ?? 'chat').trim()
    return type === 'chat' || type === 'discuss' || type === 'acp_agent'
  }

  // patchSessionInList applies a partial update to one session in BOTH the
  // reactive `sessions` array (reassigned so the sidebar list and any
  // `sessions`-derived computed re-run) and the `sessionById` lookup map. SSE
  // title/touch handlers must route through this: mutating the map's stored
  // object in place (`target.title = ...`) writes the raw object but never
  // triggers `sessions.value`, so the UI stays stale until a full REST refresh
  // (the Cmd+R symptom).
  function patchSessionInList(id: string, patch: Partial<SessionSummary>) {
    const existing = sessionById.get(id)
    if (!existing) return
    const currentDeleted = deletedSessionIdsByBot.get((existing.bot_id ?? currentBotId.value ?? '').trim())
    if (currentDeleted?.has(id)) return
    const next = { ...existing, ...patch }
    sessionById.set(id, next)
    sessions.value = sessions.value.map(session => (session.id === id ? next : session))
    markSessionLookupChanged()
  }

  function updateKnownSessionTitle(id: string, title: string) {
    const sid = id.trim()
    const nextTitle = title.trim()
    if (!sid || !nextTitle) return
    if (sessionById.has(sid)) patchSessionInList(sid, { title: nextTitle })
    const remembered = rememberedSessions.value[sid]
    if (remembered) rememberSession({ ...remembered, title: nextTitle })
  }

  function removeSessionFromList(id: string) {
    if (!sessionById.has(id) && !rememberedSessions.value[id]) return
    sessions.value = sessions.value.filter(session => session.id !== id)
    sessionById.delete(id)
    markSessionLookupChanged()
    forgetRememberedSession(id)
  }

  function touchSessionInList(targetSessionId: string, timestamp?: string) {
    const target = sessionById.get(targetSessionId)
    if (!target) return
    if (timestamp && (!target.updated_at || timestamp > target.updated_at)) {
      patchSessionInList(targetSessionId, { updated_at: timestamp })
    }
  }

  function touchKnownSession(targetSessionId: string, timestamp?: string): SessionTouchResult {
    const sid = targetSessionId.trim()
    if (!sid) return { source: 'unknown', session: null, visibleInRecents: false }

    const listed = sessionById.get(sid)
    if (listed) {
      touchSessionInList(sid, timestamp)
      const session = sessionById.get(sid) ?? listed
      return { source: 'listed', session, visibleInRecents: isRecentsSession(session) }
    }

    const remembered = rememberedSessions.value[sid]
    if (!remembered) return { source: 'unknown', session: null, visibleInRecents: false }
    if (timestamp && (!remembered.updated_at || timestamp > remembered.updated_at)) {
      const touched = { ...remembered, updated_at: timestamp }
      rememberSession(touched)
      return { source: 'remembered', session: touched, visibleInRecents: isRecentsSession(touched) }
    }
    return { source: 'remembered', session: remembered, visibleInRecents: isRecentsSession(remembered) }
  }

  function fallbackSessionAfterDelete(mode: SidebarSessionMode): SessionSummary | null {
    const visibleSessions = sessions.value.filter(session => isSessionVisibleInSidebarMode(session, mode))
    return sortByRecency(visibleSessions)[0] ?? null
  }

  function markSessionDeleted(botId: string, targetSessionId: string) {
    const bid = botId.trim()
    const sid = targetSessionId.trim()
    if (!bid || !sid) return
    const deletedIds = deletedSessionIdsByBot.get(bid) ?? new Set<string>()
    if (deletedIds.has(sid)) return
    deletedIds.add(sid)
    deletedSessionIdsByBot.set(bid, deletedIds)
    markSessionLookupChanged()
  }

  function clearDeletedSessionIds() {
    if (deletedSessionIdsByBot.size === 0) return
    deletedSessionIdsByBot.clear()
    markSessionLookupChanged()
  }

  function clearRememberedSessions() {
    if (Object.keys(rememberedSessions.value).length === 0) return
    rememberedSessions.value = {}
    markSessionLookupChanged()
  }

  return {
    // state
    sessions,
    sessionsCursor,
    hasMoreSessions,
    loadingMoreSessions,
    // computeds
    activeSession,
    knownSessions,
    activeChatReadOnly,
    activeChatCanFork,
    currentSessionListRevision,
    updateForkAnchorForReplacedMessage,
    // mutations
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
  }
}
