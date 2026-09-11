import { computed, onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQueryCache } from '@pinia/colada'
import { getBotsByBotIdApps } from '@memohai/sdk'
import { invalidateBotApps, type AppItem } from '@/composables/api/useApps'
import type { AppOperation } from '@/store/app-operations'
import { resolveApiErrorMessage } from '@/utils/api-error'

/** Resolves the installation created by the stream and keeps authorization in that flow. */
export function useAppInstallAuthorization(operation: Ref<AppOperation | null>, open: Ref<boolean>) {
  const { t } = useI18n()
  const queryCache = useQueryCache()
  const installation = ref<AppItem | null>(null)
  const loading = ref(false)
  const error = ref('')
  let request: AbortController | undefined

  const pending = computed(() => (installation.value?.connectors ?? []).filter(connector =>
    connector.status === 'needs_auth' || connector.connector?.status !== 'active',
  ))
  const current = computed(() => pending.value[0])
  const hasConnectors = computed(() => operation.value?.action !== 'remove'
    && !!operation.value?.steps.some(step => step.kind === 'connector'))
  const needsSetup = computed(() => hasConnectors.value && operation.value?.status === 'done'
    && (loading.value || !!error.value || pending.value.length > 0))

  async function refresh() {
    const active = operation.value
    if (!open.value || !active || active.status !== 'done' || !hasConnectors.value) return
    request?.abort()
    const controller = new AbortController()
    request = controller
    loading.value = true
    error.value = ''
    try {
      const { data } = await getBotsByBotIdApps({
        path: { bot_id: active.botId },
        query: { workspace_target_id: active.targetId || undefined },
        signal: controller.signal,
        throwOnError: true,
      })
      if (controller.signal.aborted) return
      const item = data.items?.find(item => item.registry_id === active.registryId && item.app_id === active.appId)
      if (!item?.installation_id) {
        error.value = t('apps.progress.authorizationLoadFailed')
        return
      }
      installation.value = item
      active.installationId = item.installation_id
      for (const step of active.steps) {
        if (step.kind !== 'connector') continue
        const connector = item.connectors?.find(connector => connector.type === step.id)
        if (connector) step.status = connector.connector?.status === 'active' ? 'linked' : 'needs_auth'
      }
      // OAuth start links a connection before it is active. Only report success
      // after the authorization form has observed an active connection.
      active.result = pending.value.some(connector => connector.required) ? 'partial' : (item.status ?? active.result)
    } catch (cause) {
      if (!controller.signal.aborted) error.value = resolveApiErrorMessage(cause, t('apps.progress.authorizationLoadFailed'))
    } finally {
      if (request === controller) loading.value = false
    }
  }

  watch([open, () => operation.value, () => operation.value?.status], () => {
    request?.abort()
    installation.value = null
    error.value = ''
    loading.value = false
    void refresh()
  }, { immediate: true })
  onBeforeUnmount(() => request?.abort())

  async function authorized() {
    await refresh()
    const botId = operation.value?.botId
    if (!botId) return
    await Promise.all([
      invalidateBotApps(queryCache, botId),
      queryCache.invalidateQueries({ key: ['bot-connectors', botId] }),
    ])
  }

  return { installation, loading, error, current, needsSetup, refresh, authorized }
}
