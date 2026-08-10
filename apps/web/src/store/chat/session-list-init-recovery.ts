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

// Recovery must rerun the FULL bootstrap, not hand-patch the sessions list:
// initialize() stops the WebSocket / activity streams before its first request
// and restarts them (plus session selection and the session runtime) only on
// the success path — a list-only retry would look recovered while realtime
// stays dead. The bootstrap's reentrancy guard resets before its promise
// rejects, so the second call below is a clean, idempotent rerun.
async function initializeWithRetry(
  deps: { currentBotId: Ref<string | null>; initialize: () => Promise<void> },
  botId: string,
) {
  if (!botId) return
  try {
    await deps.initialize()
    return
  } catch (error) {
    console.error('Chat initialize failed; retrying once:', error)
  }
  // A bot switch during the failed run owns its own initialize — never retry
  // for a bot the user has already left.
  if ((deps.currentBotId.value ?? '').trim() !== botId) return
  try {
    await deps.initialize()
  } catch (retryError) {
    if ((deps.currentBotId.value ?? '').trim() !== botId) return
    console.error('Chat initialize retry failed:', retryError)
    toast.error(resolveApiErrorMessage(
      retryError,
      i18n.global.t('chat.sessionsLoadFailed'),
    ))
  }
}
