import { watch } from 'vue'
import { useComposerPrefillStore } from '@/store/composer-prefill'

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
  /** Write the text into the composer; it replaces any unsent text. */
  apply: (text: string) => void
}

/**
 * Let a chat pane consume a pending composer prefill once it is the active,
 * writable pane of the requested bot. Requests queued before the pane mounted
 * are picked up immediately, so a freshly opened draft chat receives them.
 */
export function useComposerPrefillConsumer(deps: ComposerPrefillConsumerDeps) {
  const store = useComposerPrefillStore()
  watch(
    () => [store.pending?.id, deps.active(), deps.writable(), deps.ready(), deps.botId()] as const,
    () => {
      if (!deps.active() || !deps.writable() || !deps.ready()) return
      const text = store.take(deps.botId())
      if (text !== null) deps.apply(text)
    },
    { immediate: true },
  )
}
