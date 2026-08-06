import { ref, type Ref } from 'vue'
import { fetchSessions, type SessionSummary } from '@/composables/api/useChat'
import { sortByRecency } from '../chat-list.utils'

// Per-workdir session paging for the sidebar's folder list.
//
// A folder cannot just filter the shared `sessions` list: that list is one
// cursor-paged timeline over ALL of a bot's sessions, so a workdir whose chats
// are older than the pages loaded so far reads as empty (or partial) with no
// way to reach the missing rows. Each folder therefore pages the
// workdir-filtered endpoint on its own.
//
// Fetched rows are handed to rememberSession rather than cached as objects
// here: remembered sessions already ride the store's title, touch, and delete
// machinery, so a folder's rows stay current and a deleted chat disappears
// without this module tracking any of it.

export interface WorkdirSessionsState {
  loading: boolean
  hasMore: boolean
  loaded: boolean
}

const EMPTY_STATE: WorkdirSessionsState = { loading: false, hasMore: false, loaded: false }

export function createWorkdirSessions(deps: {
  currentBotId: Ref<string | null>
  sessions: Ref<SessionSummary[]>
  userScopeGeneration: () => number
  knownSession: (sessionId: string) => SessionSummary | null | undefined
  rememberSession: (session: SessionSummary) => void
}) {
  // Ordered fetched ids per workdir — the paging order the backend returned.
  const idsByWorkdir = ref<Record<string, string[]>>({})
  const cursorByWorkdir = ref<Record<string, string | null>>({})
  const loadingByWorkdir = ref<Record<string, boolean>>({})
  const loadedByWorkdir = ref<Record<string, boolean>>({})
  // Bumped by reset() so a response that lands after a bot switch (which also
  // clears the remembered sessions its ids point at) is discarded.
  let generation = 0

  function stateFor(workdirId: string | null | undefined): WorkdirSessionsState {
    const pid = (workdirId ?? '').trim()
    if (!pid) return EMPTY_STATE
    return {
      loading: loadingByWorkdir.value[pid] === true,
      hasMore: (cursorByWorkdir.value[pid] ?? null) !== null,
      loaded: loadedByWorkdir.value[pid] === true,
    }
  }

  function sessionsFor(workdirId: string | null | undefined): SessionSummary[] {
    const pid = (workdirId ?? '').trim()
    if (!pid) return []
    const byId = new Map<string, SessionSummary>()
    for (const id of idsByWorkdir.value[pid] ?? []) {
      const session = deps.knownSession(id)
      // A row the store has since dropped (deleted chat) is simply gone.
      if (session) byId.set(id, session)
    }
    // Sessions the shared list already holds — including ones created after
    // this folder was fetched — belong in the folder too.
    for (const session of deps.sessions.value) {
      if ((session.workdir_id ?? '').trim() === pid) byId.set(session.id, session)
    }
    return sortByRecency([...byId.values()])
  }

  async function load(workdirId: string, mode: 'initial' | 'more') {
    const botId = (deps.currentBotId.value ?? '').trim()
    const pid = workdirId.trim()
    if (!botId || !pid || loadingByWorkdir.value[pid]) return
    if (mode === 'initial' && loadedByWorkdir.value[pid]) return
    const cursor = mode === 'more' ? (cursorByWorkdir.value[pid] ?? '') : ''
    if (mode === 'more' && !cursor) return

    const resetGeneration = generation
    const userGeneration = deps.userScopeGeneration()
    loadingByWorkdir.value = { ...loadingByWorkdir.value, [pid]: true }
    try {
      const response = await fetchSessions(botId, {
        workdirId: pid,
        ...(cursor ? { cursor } : {}),
      })
      if (
        resetGeneration !== generation
        || userGeneration !== deps.userScopeGeneration()
        || (deps.currentBotId.value ?? '').trim() !== botId
      ) return
      for (const session of response.items) deps.rememberSession(session)
      const ids = mode === 'more' ? [...(idsByWorkdir.value[pid] ?? [])] : []
      const seen = new Set(ids)
      for (const session of response.items) {
        if (seen.has(session.id)) continue
        seen.add(session.id)
        ids.push(session.id)
      }
      idsByWorkdir.value = { ...idsByWorkdir.value, [pid]: ids }
      cursorByWorkdir.value = { ...cursorByWorkdir.value, [pid]: response.nextCursor }
      loadedByWorkdir.value = { ...loadedByWorkdir.value, [pid]: true }
    } catch (error) {
      console.error('Failed to load workdir sessions:', error)
    } finally {
      if (resetGeneration === generation) {
        loadingByWorkdir.value = { ...loadingByWorkdir.value, [pid]: false }
      }
    }
  }

  return {
    workdirSessionsFor: sessionsFor,
    workdirSessionsState: stateFor,
    ensureWorkdirSessions: (workdirId: string) => load(workdirId, 'initial'),
    loadMoreWorkdirSessions: (workdirId: string) => load(workdirId, 'more'),
    reset: () => {
      generation += 1
      idsByWorkdir.value = {}
      cursorByWorkdir.value = {}
      loadingByWorkdir.value = {}
      loadedByWorkdir.value = {}
    },
  }
}
