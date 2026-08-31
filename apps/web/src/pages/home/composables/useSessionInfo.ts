import { computed, ref, watch, type Ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import { toast } from '@felinic/ui'
import { getBotsByBotIdSessionsBySessionIdStatus, postBotsByBotIdSessionsBySessionIdCompact } from '@memohai/sdk'
import type { HandlersSessionInfoResponse } from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useChatStore } from '@/store/chat-list'
import { useChatViewTarget } from './useChatViewContext'
import { computeContextComposition } from './context-categories'

interface UseSessionInfoOptions {
  botId?: Ref<string | null | undefined>
  sessionId?: Ref<string | null | undefined>
  visible?: Ref<boolean>
  overrideModelId?: Ref<string>
  // The session status only reports a context window once the backend can
  // resolve one for the model; until then we fall back to the selected model's
  // configured window so the ring shows real headroom instead of an empty band.
  fallbackContextWindow?: Ref<number | null | undefined>
}

export function useSessionInfo(options: UseSessionInfoOptions = {}) {
  const chatStore = useChatStore()
  const storeRefs = storeToRefs(chatStore)
  const viewTarget = useChatViewTarget()
  const currentBotId = options.botId ?? computed(() => viewTarget.value.botId || storeRefs.currentBotId.value)
  // The injected target already falls back to the global selection when there
  // is no ChatPane provider. Within a provided Draft, null is the real target
  // and must not inherit another pane's focused Session.
  const sessionId = options.sessionId ?? computed(() => viewTarget.value.sessionId)
  const visible = options.visible ?? ref(true)

  const { data: info } = useQuery({
    key: () => [
      'session-status',
      currentBotId.value ?? '',
      sessionId.value ?? '',
      options.overrideModelId?.value ?? '',
    ],
    query: async () => {
      const { data } = await getBotsByBotIdSessionsBySessionIdStatus({
        path: {
          bot_id: currentBotId.value!,
          session_id: sessionId.value!,
        },
        query: {
          model_id: options.overrideModelId?.value || undefined,
        },
        throwOnError: true,
      })
      return data as HandlersSessionInfoResponse
    },
    enabled: () => !!currentBotId.value && !!sessionId.value && visible.value,
    refetchOnWindowFocus: false,
  })

  const usedTokens = computed(() => info.value?.context_usage?.used_tokens ?? 0)
  // The fragment estimate is the basis the backend budgets and compacts on,
  // and the only one ACP sessions report; provider-reported usage stays the
  // fallback for a status that carries no breakdown.
  const composition = computed(() => {
    const raw = computeContextComposition(info.value?.context_usage)
    return raw && raw.categories.length > 0 ? raw : null
  })
  const estimatedTokens = computed(() => composition.value?.totalTokens ?? null)
  const contextWindow = computed(() => {
    const fromStatus = info.value?.context_usage?.context_window
    if (fromStatus != null && fromStatus > 0) return fromStatus
    const fallback = options.fallbackContextWindow?.value
    return fallback != null && fallback > 0 ? fallback : null
  })
  const contextPercent = computed(() => {
    if (contextWindow.value == null || contextWindow.value <= 0) return 0
    return ((estimatedTokens.value ?? usedTokens.value) / contextWindow.value) * 100
  })

  // Compaction lives here (not in a component) so every surface that offers
  // it — the session info panel's button and the composer's /compact slash —
  // runs the identical action: same API call, same toasts, same cache
  // invalidation of this composable's own query.
  const { t } = useI18n()
  const queryCache = useQueryCache()
  const isCompacting = computed(() => chatStore.isSessionCompacting(
    currentBotId.value ?? '', sessionId.value ?? '',
  ))

  async function triggerCompact() {
    const botId = currentBotId.value
    const sid = sessionId.value
    if (!botId || !sid) return
    const finish = chatStore.beginSessionCompaction(botId, sid)
    if (!finish) return

    try {
      await postBotsByBotIdSessionsBySessionIdCompact({
        path: { bot_id: botId, session_id: sid },
        throwOnError: true,
      })
      toast.success(t('chat.compactSuccess'))
      queryCache.invalidateQueries({ key: ['session-status', botId, sid] })
    }
    catch (error) {
      toast.error(resolveApiErrorMessage(error, t('chat.compactFailed')))
    }
    finally {
      finish()
    }
  }

  // A finished turn rewrites the context but nothing else refetches the
  // status, so the ring would keep showing the pre-turn percentage. The key is
  // the 3-element prefix (no `exact`), which matches every overrideModelId
  // variant of the finished Session — same reach as triggerCompact's.
  // Scoped to this instance's Session because invalidation is not deduped:
  // every instance that fires issues its own request for the same entry.
  watch(storeRefs.streamingSessionId, (now, prev) => {
    if (prev && prev !== now && prev === sessionId.value) {
      queryCache.invalidateQueries({ key: ['session-status', currentBotId.value ?? '', prev] })
    }
  })

  return {
    info,
    usedTokens,
    composition,
    contextWindow,
    contextPercent,
    currentBotId,
    sessionId,
    isCompacting,
    triggerCompact,
  }
}
