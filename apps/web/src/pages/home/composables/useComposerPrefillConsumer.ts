import { watch } from 'vue'
import { useComposerPrefillStore, type ComposerPrefillRequest } from '@/store/composer-prefill'

export interface ComposerPrefillConsumerDeps {
  botId: () => string
  /** This pane is the active panel of the dock. */
  active: () => boolean
  /** The pane's session accepts input (not read-only). */
  writable: () => boolean
  /**
   * The current bot has finished loading. While a bot switch is loading, the
   * previous bot's panes are still mounted and must not take the request.
   */
  ready: () => boolean
  /** The composer's current, unsent text. */
  currentText: () => string
  /** Ask the user whether to replace their unsent text; resolves true to replace. */
  confirmReplace: () => Promise<boolean>
  /** Write the text into the composer. */
  apply: (text: string) => void
}

/**
 * Let a chat pane consume a pending composer prefill once it is the active,
 * writable pane of the requested bot. Requests queued before the pane mounted
 * are picked up immediately, so a freshly opened draft chat receives them.
 *
 * Unsent text is never overwritten silently: when the composer holds
 * something other than whitespace (and not already the same prompt), the
 * user confirms first. Declining keeps their text and settles the request as
 * cancelled, so the caller of prefillComposer learns the outcome.
 */
export function useComposerPrefillConsumer(deps: ComposerPrefillConsumerDeps) {
  const store = useComposerPrefillStore()

  async function consume(request: ComposerPrefillRequest) {
    const current = deps.currentText()
    if (current.trim() && current !== request.text && !(await deps.confirmReplace())) {
      request.settle('cancelled')
      return
    }
    deps.apply(request.text)
    request.settle('applied')
  }

  watch(
    () => [store.pending?.id, deps.active(), deps.writable(), deps.ready(), deps.botId()] as const,
    () => {
      if (!deps.active() || !deps.writable() || !deps.ready()) return
      const request = store.take(deps.botId())
      if (request) void consume(request)
    },
    { immediate: true },
  )
}
