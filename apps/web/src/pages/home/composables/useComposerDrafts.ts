import { computed, watch, type Ref } from 'vue'
import { useStorage } from '@vueuse/core'

// Per-tab composer drafts, persisted in localStorage so unsent text survives
// reloads. The draft key is bot + dockview tab id — NOT the session id — so a
// draft→real session promotion keeps the same draft slot (the tab id is the
// stable identity across that transition).

export interface ComposerDraftsDeps {
  currentBotId: Ref<string | null>
  tabId: () => string
  inputText: Ref<string>
}

export function useComposerDrafts({ currentBotId, tabId, inputText }: ComposerDraftsDeps) {
  const inputDrafts = useStorage<Record<string, string>>('chat-input-drafts', {})

  const inputDraftKey = computed(() => {
    const botId = (currentBotId.value ?? '').trim()
    const tab = tabId().trim()
    if (!botId || !tab) return ''
    return `${botId}:${tab}`
  })

  function saveInputDraft(key: string, text: string) {
    if (!key) return
    const next = { ...inputDrafts.value }
    if (text) {
      next[key] = text
    } else {
      delete next[key]
    }
    inputDrafts.value = next
  }

  function clearAllDrafts() {
    inputDrafts.value = {}
  }

  watch(inputDraftKey, (nextKey, previousKey) => {
    if (previousKey) {
      saveInputDraft(previousKey, inputText.value)
    }
    inputText.value = nextKey ? inputDrafts.value[nextKey] ?? '' : ''
  }, { immediate: true })

  watch(inputText, (text) => {
    saveInputDraft(inputDraftKey.value, text)
  })

  return {
    inputDraftKey,
    saveInputDraft,
    clearAllDrafts,
  }
}
