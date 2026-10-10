import { watch } from 'vue'
import { useComposerPrefillStore } from '@/store/composer-prefill'

export interface ComposerPrefillConsumerDeps {
  botId: () => string
  /** This pane is the active panel of the dock. */
  active: () => boolean
  /** The pane's session accepts input (not read-only). */
  writable: () => boolean
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
    () => [store.pending?.id, deps.active(), deps.writable(), deps.botId()] as const,
    () => {
      if (!deps.active() || !deps.writable()) return
      const text = store.take(deps.botId())
      if (text !== null) deps.apply(text)
    },
    { immediate: true },
  )
}
