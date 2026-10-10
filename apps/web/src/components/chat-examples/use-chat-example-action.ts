import { computed, toValue, type MaybeRefOrGetter } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useBotCapabilitiesQuery, useChatExamplesQuery } from '@/composables/api/useChatExamples'
import { useComposerPrefill } from '@/composables/useComposerPrefill'
import { CAPABILITY_SETTINGS_TAB } from '@/lib/chat-examples/capabilities'
import { missingCapabilities, localizeText, selectChatExamples, type SelectChatExamplesOptions } from '@/lib/chat-examples/select'
import type { BotCapability, ChatExample } from '@/lib/chat-examples/types'
import { useChatStore } from '@/store/chat-list'

/**
 * What happens when a user picks an example. With a missing capability that
 * has a settings home, open that bot settings tab so the user can turn it
 * on; otherwise prefill the localized prompt into the bot's chat.
 */
export function useChatExampleAction(botId: MaybeRefOrGetter<string>) {
  const router = useRouter()
  const { locale } = useI18n()
  const { prefillComposer } = useComposerPrefill()
  const chatStore = useChatStore()

  async function run(example: ChatExample, missing: readonly BotCapability[]) {
    const id = toValue(botId)
    const tab = missing.map(capability => CAPABILITY_SETTINGS_TAB[capability]).find(Boolean)
    if (id && tab) {
      // Settings routes are keyed by the bot's name slug, like the sidebar's Bot settings entry.
      const botName = chatStore.bots.find(bot => bot.id === id)?.name || id
      await router.push({ name: 'bot-detail', params: { botName }, query: { tab } })
      return
    }
    await prefillComposer(localizeText(example.prompt, locale.value), id ? { botId: id } : {})
  }

  return { run }
}

export interface ChatExampleEntry {
  example: ChatExample
  missing: BotCapability[]
}

/**
 * Examples for one surface, paired with the capabilities the bot lacks for
 * each. With a bot, the value is null until its capabilities are known:
 * rendering first and re-sorting when they arrive would move cards under
 * the user's pointer. A failed probe still settles (as unknown capabilities).
 * An empty array means settled with nothing to show.
 */
export function useChatExampleEntries(
  botId: MaybeRefOrGetter<string>,
  options: MaybeRefOrGetter<Omit<SelectChatExamplesOptions, 'capabilities'>>,
) {
  const { data: examples } = useChatExamplesQuery()
  const { data: capabilities, status } = useBotCapabilitiesQuery(botId)
  return computed<ChatExampleEntry[] | null>(() => {
    if (toValue(botId) && status.value === 'pending') return null
    const snapshot = capabilities.value ?? {}
    return selectChatExamples(examples.value ?? [], { ...toValue(options), capabilities: snapshot })
      .map(example => ({ example, missing: missingCapabilities(example, snapshot) }))
  })
}
