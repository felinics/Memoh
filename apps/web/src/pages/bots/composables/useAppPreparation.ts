import { computed, onBeforeUnmount, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { prepareAppOperation, type AppPreparation, type AppPrepareRequest } from '@/composables/api/useApps'
import { resolveApiErrorMessage } from '@/utils/api-error'

export interface AppPreparationTarget {
  botId: string
  request: AppPrepareRequest
}

export interface PreparedAppOperation extends AppPreparationTarget {
  result: AppPreparation
}

/** A confirmation belongs to the exact bot and selection that was reviewed. */
export function useAppPreparation(target: () => AppPreparationTarget | null) {
  const { t } = useI18n()
  const prepared = shallowRef<PreparedAppOperation | null>(null)
  const preparing = shallowRef(false)
  const error = shallowRef('')
  const targetKey = computed(() => JSON.stringify(target()))
  let request: AbortController | undefined

  function invalidate() {
    request?.abort()
    request = undefined
    prepared.value = null
    preparing.value = false
    error.value = ''
  }

  async function prepare() {
    if (preparing.value) return
    const current = target()
    if (!current) return
    invalidate()
    const key = targetKey.value
    const snapshot: AppPreparationTarget = {
      botId: current.botId,
      request: current.request.action === 'update'
        ? { ...current.request, dependencies: [...current.request.dependencies] }
        : { ...current.request },
    }
    const controller = new AbortController()
    request = controller
    preparing.value = true
    try {
      const result = await prepareAppOperation(snapshot.botId, snapshot.request, controller.signal)
      if (controller.signal.aborted || key !== targetKey.value || request !== controller) return
      prepared.value = { ...snapshot, result }
    } catch (cause) {
      if (controller.signal.aborted || key !== targetKey.value || request !== controller) return
      error.value = resolveApiErrorMessage(cause, t('apps.prepare.failed'))
    } finally {
      if (request === controller) preparing.value = false
    }
  }

  watch(targetKey, invalidate, { flush: 'sync' })
  onBeforeUnmount(invalidate)

  return { prepared, preparing, error, prepare, invalidate }
}
