import { ref, type Ref } from 'vue'
import { fetchBots, type Bot } from '@/composables/api/useChat'
import { isPendingBot } from '../chat-list.normalize'

export function createChatBots(deps: {
  currentBotId: Ref<string | null>
  userScopeGeneration: () => number
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
      const ready = list.find(bot => !isPendingBot(bot))
      deps.currentBotId.value = (ready?.id ?? list[0]?.id ?? '').trim() || null
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

  async function refreshBots() {
    const generation = deps.userScopeGeneration()
    try {
      const list = await fetchBots()
      if (generation === deps.userScopeGeneration()) bots.value = list
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
