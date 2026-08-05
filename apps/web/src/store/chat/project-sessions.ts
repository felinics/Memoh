import { ref, type Ref } from 'vue'
import { fetchSessions, type SessionSummary } from '@/composables/api/useChat'
import { sortByRecency } from '../chat-list.utils'

// Per-project session paging for the sidebar's Projects folders.
//
// A folder cannot just filter the shared `sessions` list: that list is one
// cursor-paged timeline over ALL of a bot's sessions, so a project whose chats
// are older than the pages loaded so far reads as empty (or partial) with no
// way to reach the missing rows. Each folder therefore pages the
// project-filtered endpoint on its own.
//
// Fetched rows are handed to rememberSession rather than cached as objects
// here: remembered sessions already ride the store's title, touch, and delete
// machinery, so a folder's rows stay current and a deleted chat disappears
// without this module tracking any of it.

export interface ProjectSessionsState {
  loading: boolean
  hasMore: boolean
  loaded: boolean
}

const EMPTY_STATE: ProjectSessionsState = { loading: false, hasMore: false, loaded: false }

export function createProjectSessions(deps: {
  currentBotId: Ref<string | null>
  sessions: Ref<SessionSummary[]>
  userScopeGeneration: () => number
  knownSession: (sessionId: string) => SessionSummary | null | undefined
  rememberSession: (session: SessionSummary) => void
}) {
  // Ordered fetched ids per project — the paging order the backend returned.
  const idsByProject = ref<Record<string, string[]>>({})
  const cursorByProject = ref<Record<string, string | null>>({})
  const loadingByProject = ref<Record<string, boolean>>({})
  const loadedByProject = ref<Record<string, boolean>>({})
  // Bumped by reset() so a response that lands after a bot switch (which also
  // clears the remembered sessions its ids point at) is discarded.
  let generation = 0

  function stateFor(projectId: string | null | undefined): ProjectSessionsState {
    const pid = (projectId ?? '').trim()
    if (!pid) return EMPTY_STATE
    return {
      loading: loadingByProject.value[pid] === true,
      hasMore: (cursorByProject.value[pid] ?? null) !== null,
      loaded: loadedByProject.value[pid] === true,
    }
  }

  function sessionsFor(projectId: string | null | undefined): SessionSummary[] {
    const pid = (projectId ?? '').trim()
    if (!pid) return []
    const byId = new Map<string, SessionSummary>()
    for (const id of idsByProject.value[pid] ?? []) {
      const session = deps.knownSession(id)
      // A row the store has since dropped (deleted chat) is simply gone.
      if (session) byId.set(id, session)
    }
    // Sessions the shared list already holds — including ones created after
    // this folder was fetched — belong in the folder too.
    for (const session of deps.sessions.value) {
      if ((session.project_id ?? '').trim() === pid) byId.set(session.id, session)
    }
    return sortByRecency([...byId.values()])
  }

  async function load(projectId: string, mode: 'initial' | 'more') {
    const botId = (deps.currentBotId.value ?? '').trim()
    const pid = projectId.trim()
    if (!botId || !pid || loadingByProject.value[pid]) return
    if (mode === 'initial' && loadedByProject.value[pid]) return
    const cursor = mode === 'more' ? (cursorByProject.value[pid] ?? '') : ''
    if (mode === 'more' && !cursor) return

    const resetGeneration = generation
    const userGeneration = deps.userScopeGeneration()
    loadingByProject.value = { ...loadingByProject.value, [pid]: true }
    try {
      const response = await fetchSessions(botId, {
        projectId: pid,
        ...(cursor ? { cursor } : {}),
      })
      if (
        resetGeneration !== generation
        || userGeneration !== deps.userScopeGeneration()
        || (deps.currentBotId.value ?? '').trim() !== botId
      ) return
      for (const session of response.items) deps.rememberSession(session)
      const ids = mode === 'more' ? [...(idsByProject.value[pid] ?? [])] : []
      const seen = new Set(ids)
      for (const session of response.items) {
        if (seen.has(session.id)) continue
        seen.add(session.id)
        ids.push(session.id)
      }
      idsByProject.value = { ...idsByProject.value, [pid]: ids }
      cursorByProject.value = { ...cursorByProject.value, [pid]: response.nextCursor }
      loadedByProject.value = { ...loadedByProject.value, [pid]: true }
    } catch (error) {
      console.error('Failed to load project sessions:', error)
    } finally {
      if (resetGeneration === generation) {
        loadingByProject.value = { ...loadingByProject.value, [pid]: false }
      }
    }
  }

  return {
    projectSessionsFor: sessionsFor,
    projectSessionsState: stateFor,
    ensureProjectSessions: (projectId: string) => load(projectId, 'initial'),
    loadMoreProjectSessions: (projectId: string) => load(projectId, 'more'),
    reset: () => {
      generation += 1
      idsByProject.value = {}
      cursorByProject.value = {}
      loadingByProject.value = {}
      loadedByProject.value = {}
    },
  }
}
