import { defineStore } from 'pinia'
import { useTabScopedStorage } from '@/utils/tab-scoped-storage'

export const useChatSelectionStore = defineStore('chat-selection', () => {
  // Per-Tab: which bot/session THIS browser tab is focused on. Was localStorage,
  // which broadcast every selection to every other tab (the cross-tab session
  // hijack). `currentBotId` keeps a cold-start seed so reopening defaults to the
  // last bot; the session focus is not seeded — it follows the restored layout's
  // active panel, so a stale seed would fight that restore.
  const currentBotId = useTabScopedStorage<string | null>('chat-bot-id', null, { seed: true })
  const sessionId = useTabScopedStorage<string | null>('chat-session-id', null)
  // Persist the user's intent separately from the raw session id. `sessionId`
  // can be written by initialize() when it auto-picks the latest conversation;
  // default ACP startup must be allowed to override that. A manually selected
  // or newly-created session is different and should survive reloads.
  const explicitSelection = useTabScopedStorage<boolean>('chat-explicit-selection', false)
  // Did the user intentionally sit on the draft "New Session" page (vs. just never
  // having selected anything)? A null sessionId is ambiguous on reload: this flag
  // lets initialize keep the draft instead of force-opening a random session, while
  // a fresh/never-selected load (flag false) still auto-opens the latest session.
  const draftIntent = useTabScopedStorage<boolean>('chat-draft-intent', false)

  function setBot(botId: string | null) {
    currentBotId.value = (botId ?? '').trim() || null
  }

  function setSession(targetSessionId: string | null, options: { explicitSelection?: boolean } = {}) {
    sessionId.value = (targetSessionId ?? '').trim() || null
    if (options.explicitSelection !== undefined) {
      explicitSelection.value = options.explicitSelection
    } else if (!sessionId.value) {
      explicitSelection.value = false
    }
  }

  function setExplicitSelection(value: boolean) {
    explicitSelection.value = value
  }

  function clear() {
    currentBotId.value = null
    sessionId.value = null
    explicitSelection.value = false
    draftIntent.value = false
  }

  return {
    currentBotId,
    sessionId,
    explicitSelection,
    draftIntent,
    setBot,
    setSession,
    setExplicitSelection,
    clear,
  }
})
