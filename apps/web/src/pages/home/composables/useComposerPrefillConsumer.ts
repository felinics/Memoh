import { getCurrentInstance, onActivated, onDeactivated, readonly, ref, watch } from 'vue'
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
 * A pane that KeepAlive has deactivated never takes one: its props froze at
 * the moment it was hidden, so it may still believe it is the active pane.
 *
 * Only the user's own unsent text is protected: the composer may be
 * replaced without asking when it is blank, already holds the requested
 * prompt, or still holds exactly what this pane last prefilled (so picking
 * one suggestion after another just swaps them). Anything else is text the
 * user wrote or edited, and they confirm first. Declining keeps their text
 * and settles the request as cancelled, so the caller of prefillComposer
 * learns the outcome.
 *
 * Returns `lastPrefilled`, the text this pane last wrote, so the pane can
 * tell when its composer still holds no text of the user's own.
 */
export function useComposerPrefillConsumer(deps: ComposerPrefillConsumerDeps) {
  const store = useComposerPrefillStore()
  const lastPrefilled = ref<string | null>(null)
  const attached = ref(true)
  if (getCurrentInstance()) {
    onActivated(() => { attached.value = true })
    onDeactivated(() => { attached.value = false })
  }

  function holdsUserText(current: string, request: ComposerPrefillRequest) {
    return current.trim() !== '' && current !== request.text && current !== lastPrefilled.value
  }

  async function consume(request: ComposerPrefillRequest) {
    if (holdsUserText(deps.currentText(), request) && !(await deps.confirmReplace())) {
      request.settle('cancelled')
      return
    }
    deps.apply(request.text)
    lastPrefilled.value = request.text
    request.settle('applied')
  }

  watch(
    () => [store.pending?.id, attached.value, deps.active(), deps.writable(), deps.ready(), deps.botId()] as const,
    () => {
      if (!attached.value || !deps.active() || !deps.writable() || !deps.ready()) return
      const request = store.take(deps.botId())
      if (request) void consume(request)
    },
    { immediate: true },
  )

  return { lastPrefilled: readonly(lastPrefilled) }
}
