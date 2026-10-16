import { ref, type Ref } from 'vue'
import { fetchBots, type Bot } from '@/composables/api/useChat'
import { isPendingBot } from '../chat-list.normalize'

/**
 * The bot to land on when the current selection is unusable: a ready bot
 * first, otherwise one still being created. A bot being deleted is never a
 * candidate — the server tears it down asynchronously, so it can linger in
 * the list with status `deleting` after DELETE /bots/:id returned.
 */
function pickFallbackBot(list: Bot[]): string | null {
  const ready = list.find(bot => !isPendingBot(bot))
  const next = ready ?? list.find(bot => bot.status !== 'deleting')
  return (next?.id ?? '').trim() || null
}

export function createChatBots(deps: {
  currentBotId: Ref<string | null>
  userScopeGeneration: () => number
  /** Full bot switch (aborts streams/WebSocket of the old bot, re-initializes). */
  selectBot: (botId: string) => Promise<void>
}) {
  const bots = ref<Bot[]>([])

  async function ensureBot(): Promise<string | null> {
    const generation = deps.userScopeGeneration()
    try {
      const list = await fetchBots()
      if (generation !== deps.userScopeGeneration()) return null
      bots.value = list
      if (!list.length) {
        deps.currentBotId.value = null
        return null
      }
      if (deps.currentBotId.value) {
        const found = list.find(bot => bot.id === deps.currentBotId.value)
        if (found && !isPendingBot(found)) return deps.currentBotId.value
      }
      deps.currentBotId.value = pickFallbackBot(list)
      return deps.currentBotId.value
    } catch (error) {
      // Stale run (user scope changed mid-flight): resolve quietly. A live
      // failure must surface so bootstrap recovery retries it — swallowing it
      // here used to read as "account has no bots", letting initialize()
      // complete "successfully" with no WebSocket and no recovery (#1070).
      if (generation !== deps.userScopeGeneration()) return null
      console.error('Failed to fetch bots:', error)
      throw error
    }
  }

  /**
   * Reloads the bot list and moves the selection off the current bot when it
   * is gone or being deleted. With no usable bot left the selection is
   * cleared, which lets the bot-id watcher reset the store into the empty
   * state. A current bot that is still being created is kept.
   */
  async function refreshBots() {
    const generation = deps.userScopeGeneration()
    try {
      const list = await fetchBots()
      if (generation !== deps.userScopeGeneration()) return
      bots.value = list
      const currentId = (deps.currentBotId.value ?? '').trim()
      if (!currentId) return
      const current = list.find(bot => bot.id === currentId)
      if (current && current.status !== 'deleting') return
      const next = pickFallbackBot(list)
      if (next) await deps.selectBot(next)
      else deps.currentBotId.value = null
    } catch (error) {
      if (generation === deps.userScopeGeneration()) {
        console.error('Failed to refresh bots:', error)
      }
    }
  }

  return {
    bots,
    ensureBot,
    refreshBots,
    reset: () => { bots.value = [] },
  }
}
