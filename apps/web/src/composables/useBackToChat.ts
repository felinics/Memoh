import { computed } from 'vue'
import { storeToRefs } from 'pinia'
import { useChatSelectionStore } from '@/store/chat-selection'
import { useChatStore } from '@/store/chat-list'

/**
 * The route that leads back to the active chat from a non-chat surface
 * (settings). Falls back to the bare home route when no bot is selected, and
 * to the raw id as the slug while the bot list hasn't loaded the record yet.
 *
 * One home, consumed by the settings sidebar's back row AND the mobile
 * settings top bar — the two exits must never disagree about where "back to
 * chat" lands.
 */
export function useBackToChatRoute() {
  const selectionStore = useChatSelectionStore()
  const { currentBotId } = storeToRefs(selectionStore)
  const chatStore = useChatStore()
  const { bots } = storeToRefs(chatStore)

  return computed(() => {
    const botId = (currentBotId.value ?? '').trim()
    if (!botId) return { name: 'home' as const }
    const botName = bots.value.find(b => b.id === botId)?.name ?? botId
    return { name: 'bot' as const, params: { botName } }
  })
}
