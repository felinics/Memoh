import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { toast } from '@felinic/ui'
import { CHAT_ROUTE_NAMES } from '@/constants/chat-routes'
import { useChatStore } from '@/store/chat-list'
import { useComposerPrefillStore, type ComposerPrefillOutcome } from '@/store/composer-prefill'

/** 'applied' — in the composer; 'cancelled' — the user kept their own text, or a newer request replaced it; 'no-bot' — nothing to prefill into. */
export type PrefillResult = ComposerPrefillOutcome | 'no-bot'

/**
 * The single entry point for "put this prompt in the chat box": usage
 * examples, the App "Try it" button and onboarding all go through here.
 */
export function useComposerPrefill() {
  const { t } = useI18n()
  const router = useRouter()
  const chatStore = useChatStore()
  const prefillStore = useComposerPrefillStore()

  /**
   * Put `text` into the composer of the active chat of `botId` (default: the
   * current bot, else the first bot), and never send it. Navigates to that
   * bot's chat page when needed; the home page then opens a new chat if no
   * writable chat is active (see useComposerPrefillResolver).
   *
   * Resolves only once the text has landed or been turned down: a composer
   * holding unsent text asks the user before replacing it, so this can wait
   * on that confirmation.
   *
   * An empty bot list is re-fetched once before concluding the user has no
   * bot, in which case a toast offers to create one and 'no-bot' is returned.
   */
  async function prefillComposer(text: string, options: { botId?: string } = {}): Promise<PrefillResult> {
    if (chatStore.bots.length === 0) await chatStore.refreshBots()
    const botId = options.botId
      || chatStore.currentBotId
      || chatStore.bots[0]?.id
      || ''
    if (!botId) {
      toast.info(t('chatExamples.noBot'), {
        action: { label: t('chatExamples.createBot'), onClick: () => void router.push({ name: 'bot-new' }) },
      })
      return 'no-bot'
    }

    const outcome = prefillStore.request(botId, text)
    const onChatPage = CHAT_ROUTE_NAMES.has(router.currentRoute.value.name as string)
    if (!onChatPage || botId !== chatStore.currentBotId) {
      await router.push({ name: 'bot', params: { botName: botId } })
    }
    return outcome
  }

  return { prefillComposer }
}
