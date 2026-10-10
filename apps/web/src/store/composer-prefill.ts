import { defineStore } from 'pinia'
import { ref } from 'vue'
import { onAuthSessionCleared } from '@/lib/auth-session'

/** How a prefill ended: written into a composer, or not (declined by the user, replaced or dropped). */
export type ComposerPrefillOutcome = 'applied' | 'cancelled'

export interface ComposerPrefillRequest {
  id: number
  botId: string
  text: string
  /** Report the outcome back to the requester; only the first call counts. */
  settle: (outcome: ComposerPrefillOutcome) => void
}

/**
 * One-shot hand-off of text for a chat composer. A request is addressed to a
 * bot, not to a pane: whichever chat pane of that bot becomes the active,
 * writable one takes it and must settle it. A newer request replaces an
 * untaken one, which then settles as cancelled. Callers should go through
 * `useComposerPrefill`, which also handles navigation and the no-bot case.
 */
export const useComposerPrefillStore = defineStore('composer-prefill', () => {
  const pending = ref<ComposerPrefillRequest | null>(null)
  let nextId = 0

  /** Queue `text` for `botId`; resolves once a pane applies it or it is cancelled. */
  function request(botId: string, text: string): Promise<ComposerPrefillOutcome> {
    pending.value?.settle('cancelled')
    nextId += 1
    return new Promise((resolve) => {
      let settled = false
      pending.value = {
        id: nextId,
        botId,
        text,
        settle: (outcome) => {
          if (settled) return
          settled = true
          resolve(outcome)
        },
      }
    })
  }

  /** Take the pending request when it is addressed to `botId`; the taker must settle it. */
  function take(botId: string): ComposerPrefillRequest | null {
    const current = pending.value
    if (!current || current.botId !== botId) return null
    pending.value = null
    return current
  }

  function clear() {
    pending.value?.settle('cancelled')
    pending.value = null
  }

  onAuthSessionCleared(clear)

  return { pending, request, take, clear }
})
