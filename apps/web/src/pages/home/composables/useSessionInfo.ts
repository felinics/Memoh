import { computed, ref, type Ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import { toast } from '@felinic/ui'
import { getBotsByBotIdSessionsBySessionIdStatus, postBotsByBotIdSessionsBySessionIdCompact } from '@memohai/sdk'
import type { HandlersSessionInfoResponse } from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useChatStore } from '@/store/chat-list'
import { useChatViewTarget } from './useChatViewContext'
import { resolveSessionContextView } from './session-context-view'
import { installTurnEndInvalidation } from './turn-end-invalidation'

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

interface CompactionOptions {
  quietSuccess?: () => boolean
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
    query: async ({ signal }) => {
      const { data } = await getBotsByBotIdSessionsBySessionIdStatus({
        path: {
          bot_id: currentBotId.value!,
          session_id: sessionId.value!,
        },
        query: {
          model_id: options.overrideModelId?.value || undefined,
        },
        signal,
        throwOnError: true,
      })
      return data as HandlersSessionInfoResponse
    },
    enabled: () => !!currentBotId.value && !!sessionId.value && visible.value,
    refetchOnWindowFocus: false,
  })

  const usedTokens = computed(() => info.value?.context_usage?.used_tokens ?? 0)
  const contextView = computed(() => resolveSessionContextView(info.value?.context_usage, {
    fallbackWindow: options.fallbackContextWindow?.value,
  }))
  const composition = computed(() => contextView.value.composition)
  const estimatedTokens = computed(() => contextView.value.estimatedTokens)
  const contextWindow = computed(() => contextView.value.contextWindow)
  const outputReserve = computed(() => contextView.value.outputReserve)
  const autoCompactTokens = computed(() => contextView.value.autoCompactTokens)
  const compactionAvailable = computed(() => contextView.value.compactionAvailable)
  // Anything that owns context is compactable, whichever basis reported it.
  const contextTokens = computed(() => estimatedTokens.value ?? usedTokens.value)
  const contextPercent = computed(() => {
    if (contextWindow.value == null || contextWindow.value <= 0) return 0
    return ((estimatedTokens.value ?? usedTokens.value) / contextWindow.value) * 100
  })

  // Native and runtime-owned compaction share feedback and request lifetime;
  // callers supply only the transport when the runtime owns the operation.
  const { t } = useI18n()
  const queryCache = useQueryCache()
  const isCompacting = computed(() => chatStore.isSessionCompacting(
    currentBotId.value ?? '', sessionId.value ?? '',
  ))

  // `quietSuccess` lets a caller that shows its own success state in view
  // skip the toast; it is read when the request settles, not when it starts.
  // Failures always toast. Resolves true on success.
  async function runCompaction(execute: () => Promise<unknown>, opts: CompactionOptions = {}) {
    const botId = currentBotId.value
    const sid = sessionId.value
    if (!botId || !sid) return false
    const finish = chatStore.beginSessionCompaction(botId, sid)
    if (!finish) return false

    try {
      await execute()
      if (!opts.quietSuccess?.()) toast.success(t('chat.compactSuccess'))
      queryCache.invalidateQueries({ key: ['session-status', botId, sid] })
      return true
    }
    catch (error) {
      toast.error(resolveApiErrorMessage(error, t('chat.compactFailed')))
      return false
    }
    finally {
      finish()
    }
  }

  async function triggerCompact(opts: CompactionOptions = {}) {
    const botId = currentBotId.value
    const sid = sessionId.value
    if (!botId || !sid) return false
    return runCompaction(() => postBotsByBotIdSessionsBySessionIdCompact({
      path: { bot_id: botId, session_id: sid },
      throwOnError: true,
    }), opts)
  }

  installTurnEndInvalidation(storeRefs.streamingSessionIds, queryCache)

  return {
    info,
    usedTokens,
    composition,
    contextWindow,
    outputReserve,
    autoCompactTokens,
    compactionAvailable,
    contextTokens,
    contextPercent,
    currentBotId,
    sessionId,
    isCompacting,
    triggerCompact,
    runCompaction,
  }
}
