import { defineStore } from 'pinia'
import { StorageSerializers, useLocalStorage } from '@vueuse/core'
import { useTabScopedStorage } from '@/utils/tab-scoped-storage'

// Accounts whose last bot this browser keeps; older ones drop off.
const REMEMBERED_ACCOUNTS = 10

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
  // default External Agent startup must be allowed to override that. A manually selected
  // or newly-created session is different and should survive reloads.
  const explicitSelection = useTabScopedStorage<boolean>('chat-explicit-selection', false)
  // Did the user intentionally sit on the draft "New Session" page (vs. just never
  // having selected anything)? A null sessionId is ambiguous on reload: this flag
  // lets initialize keep the draft instead of force-opening a random session, while
  // a fresh/never-selected load (flag false) still auto-opens the latest session.
  const draftIntent = useTabScopedStorage<boolean>('chat-draft-intent', false)

  // Draft Agent choices live in memory. On reload, preserve the empty-draft
  // intent but discard its stale explicit-choice flag so Bot defaults can load.
  // Real sessions retain their persisted selection and runtime.
  if (!sessionId.value && explicitSelection.value) {
    draftIntent.value = true
    explicitSelection.value = false
  }

  // The bot each account last had open in this browser, most recent account
  // first. The per-Tab selection above is cleared on every sign-in and
  // sign-out so no account inherits another's; these entries name their
  // account instead, so they outlive sign-out and let the same account open
  // its bot right after signing back in, without waiting for the bot list.
  // The list still checks it once it loads (ensureBot moves to another bot
  // when this one is gone).
  const lastBots = useLocalStorage<Array<{ userId: string, botId: string }>>(
    'memoh:chat:last-bots',
    [],
    { serializer: StorageSerializers.object },
  )
  const rememberedBots = () => (Array.isArray(lastBots.value) ? lastBots.value : [])

  function rememberBot(userId: string, botId: string) {
    const user = userId.trim()
    const bot = botId.trim()
    if (!user || !bot) return
    const entries = rememberedBots()
    if (entries[0]?.userId === user && entries[0]?.botId === bot) return
    lastBots.value = [
      { userId: user, botId: bot },
      ...entries.filter(entry => entry.userId !== user),
    ].slice(0, REMEMBERED_ACCOUNTS)
  }

  // Selects the account's last bot when no bot is selected yet.
  function restoreBot(userId: string) {
    const user = userId.trim()
    const bot = rememberedBots().find(entry => entry.userId === user)?.botId?.trim() ?? ''
    if (!user || !bot || currentBotId.value) return
    currentBotId.value = bot
  }

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
    rememberBot,
    restoreBot,
    setSession,
    setExplicitSelection,
    clear,
  }
})
