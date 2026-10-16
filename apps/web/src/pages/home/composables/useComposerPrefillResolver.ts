import { watch } from 'vue'
import type { ComposerPrefillRequest } from '@/store/composer-prefill'

export interface ComposerPrefillResolverDeps {
  pending: () => ComposerPrefillRequest | null
  currentBotId: () => string | null
  /** The dock is mounted and can open panels. */
  dockReady: () => boolean
  /** The active panel is a chat the user can type into. */
  activeChatWritable: () => boolean
  openDraftChat: () => void
}

/**
 * Make sure a pending composer prefill has somewhere to land. When the
 * request's bot is current and the dock is up but no writable chat is active
 * (read-only session, a terminal or file tab, an empty dock), open a new
 * draft chat; the chat pane that becomes active then consumes the request.
 * Each request opens at most one draft, so a draft that fails to activate
 * cannot cause a loop.
 */
export function useComposerPrefillResolver(deps: ComposerPrefillResolverDeps) {
  let handledRequestId: number | null = null
  watch(
    () => [deps.pending(), deps.currentBotId(), deps.dockReady(), deps.activeChatWritable()] as const,
    ([pending, botId, dockReady, writable]) => {
      if (!pending || pending.botId !== botId || !dockReady || writable) return
      if (handledRequestId === pending.id) return
      handledRequestId = pending.id
      deps.openDraftChat()
    },
    { immediate: true },
  )
}
