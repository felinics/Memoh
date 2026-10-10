import { defineStore } from 'pinia'
import { ref } from 'vue'
import { onAuthSessionCleared } from '@/lib/auth-session'

export interface ComposerPrefillRequest {
  id: number
  botId: string
  text: string
}

/**
 * One-shot hand-off of text for a chat composer. A request is addressed to a
 * bot, not to a pane: whichever chat pane of that bot becomes the active,
 * writable one consumes it. Callers should go through `useComposerPrefill`,
 * which also handles navigation and the no-bot case.
 */
export const useComposerPrefillStore = defineStore('composer-prefill', () => {
  const pending = ref<ComposerPrefillRequest | null>(null)
  let nextId = 0

  function request(botId: string, text: string) {
    nextId += 1
    pending.value = { id: nextId, botId, text }
  }

  /** Consume the pending text when it is addressed to `botId`; otherwise leave it. */
  function take(botId: string): string | null {
    const current = pending.value
    if (!current || current.botId !== botId) return null
    pending.value = null
    return current.text
  }

  function clear() {
    pending.value = null
  }

  onAuthSessionCleared(clear)

  return { pending, request, take, clear }
})
