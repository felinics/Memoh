import { toast } from '@felinic/ui'
import { watch, type Ref } from 'vue'
import i18n from '@/i18n'
import { resolveApiErrorMessage } from '@/utils/api-error'
import type { FetchSessionsResult, SessionSummary } from '@/composables/api/useChat'

export function createSessionListSnapshot(deps: {
  replaceSessions: (items: SessionSummary[]) => SessionSummary[]
  sessionsCursor: Ref<string | null>
  hasMoreSessions: Ref<boolean>
}) {
  function applySessionsSnapshot(response: FetchSessionsResult) {
    deps.replaceSessions(response.items)
    deps.sessionsCursor.value = response.nextCursor
    deps.hasMoreSessions.value = response.nextCursor !== null
  }

  return { applySessionsSnapshot }
}

export function bindBotIdInitializeWatch(deps: {
  currentBotId: Ref<string | null>
  initialize: () => Promise<void>
  resetUserScopedState: () => void
}) {
  watch(deps.currentBotId, (newId) => {
    if (newId) void initializeWithRetry(deps, newId.trim())
    else deps.resetUserScopedState()
  }, { immediate: true })
}

// Entry for the first-open path where no bot is selected yet (bare home
// route). Captures the current bot id (possibly '') so a later bot selection
// cleanly hands recovery off to the watcher's own retry loop instead of
// running a second one.
export function createInitializeRecovery(deps: {
  currentBotId: Ref<string | null>
  initialize: () => Promise<void>
}) {
  return {
    initializeWithRecovery: () =>
      initializeWithRetry(deps, (deps.currentBotId.value ?? '').trim()),
  }
}

const INITIALIZE_RETRY_INITIAL_DELAY_MS = 1000
const INITIALIZE_RETRY_MAX_DELAY_MS = 30000

function sleep(ms: number) {
  return new Promise<void>((resolve) => setTimeout(resolve, ms))
}

// Recovery must rerun the FULL bootstrap, not hand-patch the sessions list:
// initialize() stops the WebSocket / activity streams before its first request
// and restarts them (plus session selection and the session runtime) only on
// the success path — a list-only retry would look recovered while realtime
// stays dead. First-open failures are usually transient (server still booting
// behind an already-serving proxy, a brief network blip), so retry with
// exponential backoff until the bootstrap succeeds. Giving up after a single
// retry used to leave the page with no WebSocket and no automatic recovery
// until a manual refresh (#1070).
async function initializeWithRetry(
  deps: { currentBotId: Ref<string | null>; initialize: () => Promise<void> },
  botId: string,
) {
  let delay = INITIALIZE_RETRY_INITIAL_DELAY_MS
  let failures = 0
  for (;;) {
    // A scope change (bot switch, sign-out) owns its own initialize — never
    // keep retrying for a scope the user has already left.
    if ((deps.currentBotId.value ?? '').trim() !== botId) return
    try {
      await deps.initialize()
      return
    } catch (error) {
      if ((deps.currentBotId.value ?? '').trim() !== botId) return
      failures += 1
      if (failures === 1) {
        console.error('Chat initialize failed; retrying with backoff:', error)
      }
      // Surface the outage once (on the second consecutive failure, matching
      // the previous single-retry UX) and keep retrying silently afterwards.
      if (failures === 2) {
        toast.error(resolveApiErrorMessage(
          error,
          i18n.global.t('chat.sessionsLoadFailed'),
        ))
      }
      await sleep(delay)
      delay = Math.min(delay * 2, INITIALIZE_RETRY_MAX_DELAY_MS)
    }
  }
}
