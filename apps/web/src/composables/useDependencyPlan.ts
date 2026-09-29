import { computed, ref, shallowRef, toValue, watch, type MaybeRefOrGetter } from 'vue'
import {
  postBotsByBotIdAppsPrepare,
  postBotsByBotIdDependenciesPlan,
  type AppsPrepareRequest,
  type WorkspacedepsPlan,
  type WorkspacedepsPlanRoot,
} from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import { resolveApiErrorMessage } from '@/utils/api-error'

type Request = { app: AppsPrepareRequest } | { roots: WorkspacedepsPlanRoot[] }

// The request key fences stale responses, including closing/reopening a dialog
// and editing a version while the previous preview is still loading.
export function useDependencyPlan(botId: MaybeRefOrGetter<string>, request: MaybeRefOrGetter<Request | null>) {
  const { t } = useI18n()
  const plan = shallowRef<WorkspacedepsPlan | null>(null)
  const revision = ref('')
  const loading = ref(false)
  const error = ref('')
  const refresh = ref(0)
  let generation = 0
  watch(() => JSON.stringify([toValue(botId), toValue(request), refresh.value]), async () => {
    const token = ++generation
    plan.value = null
    error.value = ''
    revision.value = ''
    const bot = toValue(botId)
    const req = toValue(request)
    loading.value = !!bot && !!req
    if (!bot || !req) return
    try {
      if ('app' in req) {
        const { data } = await postBotsByBotIdAppsPrepare({ path: { bot_id: bot }, body: req.app, throwOnError: true })
        if (token !== generation) return
        plan.value = data.plan ?? null
        revision.value = data.revision ?? ''
      } else {
        const { data } = await postBotsByBotIdDependenciesPlan({ path: { bot_id: bot }, body: req, throwOnError: true })
        if (token !== generation) return
        plan.value = data
      }
    } catch (cause) {
      if (token === generation) error.value = resolveApiErrorMessage(cause, t('dependenciesPlan.failed'))
    } finally {
      if (token === generation) loading.value = false
    }
  }, { immediate: true })
  const ready = computed(() => !!plan.value && !loading.value && !error.value)
  return { plan, revision, loading, error, ready, retry: () => { refresh.value += 1 } }
}
