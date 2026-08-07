import { toast } from '@felinic/ui'
import { watch, type Ref } from 'vue'
import i18n from '@/i18n'
import { resolveApiErrorMessage } from '@/utils/api-error'
import type { FetchSessionsResult, SessionSummary } from '@/composables/api/useChat'

export function createSessionListSnapshot(deps: {
  replaceSessions: (items: SessionSummary[]) => SessionSummary[]
  sessionsCursor: Ref<string | null>
  hasMoreSessions: Ref<boolean>
  fetchSessions: (botId: string) => Promise<FetchSessionsResult>
  currentBotId: () => string | null
}) {
  function applySessionsSnapshot(response: FetchSessionsResult) {
    deps.replaceSessions(response.items)
    deps.sessionsCursor.value = response.nextCursor
    deps.hasMoreSessions.value = response.nextCursor !== null
  }

  async function recoverSessionsListAfterInitializeFailure(botId: string, error: unknown) {
    const bid = botId.trim()
    if (!bid) return
    console.error('Chat initialize failed; retrying session list fetch:', error)
    try {
      const response = await deps.fetchSessions(bid)
      if ((deps.currentBotId() ?? '').trim() !== bid) return
      applySessionsSnapshot(response)
    } catch (retryError) {
      console.error('Failed to load sessions after initialize retry:', retryError)
      toast.error(resolveApiErrorMessage(
        retryError,
        i18n.global.t('chat.sessionsLoadFailed'),
      ))
    }
  }

  return { applySessionsSnapshot, recoverSessionsListAfterInitializeFailure }
}

export function bindBotIdInitializeWatch(deps: {
  currentBotId: Ref<string | null>
  initialize: () => Promise<void>
  recoverSessionsListAfterInitializeFailure: (botId: string, error: unknown) => Promise<void>
  resetUserScopedState: () => void
}) {
  watch(deps.currentBotId, (newId) => {
    if (newId) {
      void deps.initialize().catch(error => {
        void deps.recoverSessionsListAfterInitializeFailure(newId, error)
      })
    } else deps.resetUserScopedState()
  }, { immediate: true })
}
