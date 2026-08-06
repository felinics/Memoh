import { ref, computed, watch, provide, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMutation, useQuery, useQueryCache } from '@pinia/colada'
import {
  postProviders,
  postProvidersByIdImportModels,
  postProvidersByIdTest,
  deleteModelsById,
  getProvidersByIdModels,
  getProvidersNameByName,
  putModelsById,
  putProvidersById,
  type ProvidersCreateRequest,
  type ModelsGetResponse,
} from '@memohai/sdk'
import { MANUAL_LLM_CLIENT_TYPE_LIST } from '@/constants/client-types'
import type { ProviderPreset } from '@/constants/provider-presets'

export function useProviderSetup(options: {
  selectedPreset: () => ProviderPreset | null
  onProviderReady: () => void
}) {
  const { t } = useI18n()
  const queryCache = useQueryCache()

  const formValues = ref({
    name: '',
    api_key: '',
    base_url: '',
    client_type: 'openai-completions',
  })

  const formError = ref('')
  const createdProviderId = ref<string | null>(null)
  const errorState = ref<'http' | 'unreachable' | 'authError' | 'noModels' | null>(null)
  const errorDetail = ref('')
  const manualMode = ref(false)
  const saving = ref(false)
  const suppressDirtyReset = ref(false)

  const openModelState = ref(false)
  const openModelTitle = ref<'edit' | 'title'>('title')
  const openModelEdit = ref<ModelsGetResponse | null>(null)

  provide('openModel', openModelState)
  provide('openModelTitle', openModelTitle)
  provide('openModelState', openModelEdit)

  const { state: providerModelsState, refresh: refreshProviderModels } = useQuery({
    key: () => ['provider-models', createdProviderId.value ?? 'none'],
    query: async () => {
      if (!createdProviderId.value) return [] as ModelsGetResponse[]
      const { data } = await getProvidersByIdModels({
        path: { id: createdProviderId.value },
        throwOnError: true,
      })
      return data ?? []
    },
    enabled: () => !!createdProviderId.value && manualMode.value,
  })

  const providerModels = computed<ModelsGetResponse[]>(() => providerModelsState.value.data ?? [])

  const availableClientTypes = computed(() =>
    MANUAL_LLM_CLIENT_TYPE_LIST,
  )

  const baseUrlPlaceholder = computed(() => {
    switch (formValues.value.client_type) {
      case 'anthropic-messages':
        return 'https://api.anthropic.com'
      case 'google-generative-ai':
        return 'https://generativelanguage.googleapis.com/v1beta'
      default:
        return 'https://api.example.com/v1'
    }
  })

  const { mutateAsync: createProvider } = useMutation({
    mutation: async (payload: ProvidersCreateRequest) => {
      const { data } = await postProviders({ body: payload, throwOnError: true })
      return data
    },
    onSettled: () => {
      queryCache.invalidateQueries({ key: ['providers'] })
    },
  })

  const { mutateAsync: importModels, isLoading: importing } = useMutation({
    mutation: async (providerId: string) => {
      const { data } = await postProvidersByIdImportModels({
        path: { id: providerId },
        throwOnError: true,
      })
      return data
    },
    onSettled: () => {
      queryCache.invalidateQueries({ key: ['models'] })
      queryCache.invalidateQueries({ key: ['provider-models'] })
    },
  })

  const { mutateAsync: deleteModel, isLoading: deleteModelLoading } = useMutation({
    mutation: async (id: string) => {
      await deleteModelsById({ path: { id }, throwOnError: true })
    },
    onSettled: () => {
      queryCache.invalidateQueries({ key: ['models'] })
      queryCache.invalidateQueries({ key: ['provider-models'] })
    },
  })

  const submitting = computed(() => saving.value || importing.value)

  const formCtaLabel = computed(() => {
    if (saving.value) return t('onboarding.provider.form.importing')
    if (importing.value) return t('onboarding.provider.form.importing')
    return t('onboarding.next')
  })

  const formCtaDisabled = computed(() => {
    if (saving.value) return true
    if (importing.value) return true
    if (manualMode.value) return providerModels.value.length === 0
    if (errorState.value) return true
    return false
  })

  function resetFormState() {
    createdProviderId.value = null
    errorState.value = null
    errorDetail.value = ''
    manualMode.value = false
    saving.value = false
    openModelState.value = false
    openModelTitle.value = 'title'
    openModelEdit.value = null
  }

  function initFormValues(preset: ProviderPreset | null) {
    suppressDirtyReset.value = true
    formValues.value = preset
      ? { name: preset.name, api_key: '', base_url: preset.baseUrl, client_type: preset.clientType }
      : { name: '', api_key: '', base_url: '', client_type: 'openai-completions' }
    formError.value = ''
    resetFormState()
  }

  function clearSuppressDirtyReset() {
    suppressDirtyReset.value = false
  }

  async function ensureProviderCreated(): Promise<string | null> {
    const name = formValues.value.name.trim()
    const apiKey = formValues.value.api_key.trim()
    const baseUrl = formValues.value.base_url.trim()
    if (!name || !apiKey || !baseUrl) {
      formError.value = t('onboarding.provider.form.requiredError')
      return null
    }
    formError.value = ''

    try {
      if (createdProviderId.value) {
        // Include client_type so a corrected protocol selection on retry takes
        // effect: a custom provider first created with the wrong client_type
        // would otherwise keep the stale value here and keep probing/importing
        // with the wrong protocol, leaving the user unable to recover without
        // starting over. The backend re-normalizes config for the new protocol.
        await putProvidersById({
          path: { id: createdProviderId.value },
          body: { name, client_type: formValues.value.client_type, config: { base_url: baseUrl, api_key: apiKey }, enable: true },
          throwOnError: true,
        })
        return createdProviderId.value
      }

      const preset = options.selectedPreset()
      const lookupName = preset?.registryName ?? name
      const intendedClientType = preset?.clientType ?? formValues.value.client_type
      const { data: existing } = await getProvidersNameByName({
        path: { name: lookupName },
      })

      if (existing?.id) {
        // Reuse (dedup) onto an existing provider only when the protocol matches.
        // A differing — or missing — client_type means overwriting credentials
        // would land them on the wrong/undefined API format and break every later
        // call, so refuse and ask the user to rename. intendedClientType is always
        // concrete (formValues defaults to a real type), so a blank existing
        // client_type is correctly treated as a mismatch here.
        if (existing.client_type !== intendedClientType) {
          formError.value = t('onboarding.provider.form.typeConflict')
          return null
        }
        await putProvidersById({
          path: { id: existing.id },
          body: { config: { base_url: baseUrl, api_key: apiKey }, enable: true },
          throwOnError: true,
        })
        createdProviderId.value = existing.id
        return existing.id
      }

      const result = await createProvider({
        name,
        client_type: formValues.value.client_type,
        config: { base_url: baseUrl, api_key: apiKey },
      } as ProvidersCreateRequest)
      if (!result?.id) {
        errorState.value = 'http'
        return null
      }
      createdProviderId.value = result.id
      return result.id
    }
    catch (e) {
      formError.value = (e as Error).message || t('onboarding.provider.form.saveFailed')
      return null
    }
  }

  async function runImport(providerId: string) {
    errorState.value = null
    errorDetail.value = ''

    try {
      const { data: testResult } = await postProvidersByIdTest({
        path: { id: providerId },
        throwOnError: true,
      })
      if (testResult?.status === 'auth_error') {
        errorState.value = 'authError'
        errorDetail.value = testResult?.message ?? ''
        return
      }
      if (testResult?.status === 'error') {
        errorState.value = testResult?.reachable ? 'http' : 'unreachable'
        errorDetail.value = testResult?.message ?? ''
        return
      }
    } catch {
      errorState.value = 'unreachable'
      return
    }

    let importFailed = false
    try {
      await importModels(providerId)
    } catch {
      importFailed = true
    }

    try {
      const { data: models } = await getProvidersByIdModels({
        path: { id: providerId },
      })
      if (models && models.length > 0) {
        // Imports land disabled per LobeHub-style policy; onboarding needs at
        // least one enabled chat model so the next bot step has something to
        // select. Activate the first chat model we see so the auto-advance
        // path stays usable; the user can fine-tune the rest later.
        await ensureFirstChatModelEnabled(models)
        options.onProviderReady()
        return
      }
    } catch {
      errorState.value = 'http'
      return
    }

    errorState.value = importFailed ? 'http' : 'noModels'
  }

  async function ensureFirstChatModelEnabled(models: ModelsGetResponse[]) {
    if (models.some((m) => m.type === 'chat' && m.enable)) return
    const target = models.find((m) => m.type === 'chat')
    if (!target?.id) return
    try {
      await putModelsById({
        path: { id: target.id },
        body: {
          model_id: target.model_id,
          name: target.name,
          provider_id: target.provider_id,
          type: target.type,
          config: target.config,
          enable: true,
        },
        throwOnError: true,
      })
      queryCache.invalidateQueries({ key: ['provider-models'] })
      queryCache.invalidateQueries({ key: ['models'] })
    } catch {
      // Non-fatal: the user will still see the model list and can enable
      // models manually if the auto-activate failed.
    }
  }

  async function saveAndNext() {
    if (saving.value) return
    if (manualMode.value) {
      if (providerModels.value.length === 0) return
      options.onProviderReady()
      return
    }

    saving.value = true
    try {
      const providerId = await ensureProviderCreated()
      if (!providerId) return
      await runImport(providerId)
    } finally {
      saving.value = false
    }
  }

  async function onRetry() {
    if (!createdProviderId.value) {
      await saveAndNext()
      return
    }
    await runImport(createdProviderId.value)
  }

  async function onEnterManual() {
    if (!createdProviderId.value) return
    errorState.value = null
    manualMode.value = true
    await refreshProviderModels()
    await nextTick()
    openModelEdit.value = null
    openModelTitle.value = 'title'
    openModelState.value = true
  }

  function handleEditModel(model: ModelsGetResponse) {
    openModelEdit.value = { ...model }
    openModelTitle.value = 'edit'
    openModelState.value = true
  }

  function openAddDialog() {
    openModelEdit.value = null
    openModelTitle.value = 'title'
    openModelState.value = true
  }

  async function handleDeleteModel(id: string) {
    if (!id) return
    await deleteModel(id)
    await refreshProviderModels()
  }

  watch(
    () => [formValues.value.name, formValues.value.api_key, formValues.value.base_url, formValues.value.client_type],
    () => {
      if (suppressDirtyReset.value) return
      if (manualMode.value) return
      if (formError.value) formError.value = ''
      if (errorState.value) {
        errorState.value = null
        errorDetail.value = ''
      }
    },
  )

  return {
    formValues,
    formError,
    createdProviderId,
    errorState,
    errorDetail,
    manualMode,
    importing,
    submitting,
    deleteModelLoading,
    providerModels,
    availableClientTypes,
    baseUrlPlaceholder,
    formCtaLabel,
    formCtaDisabled,
    resetFormState,
    initFormValues,
    clearSuppressDirtyReset,
    saveAndNext,
    onRetry,
    onEnterManual,
    openAddDialog,
    handleEditModel,
    handleDeleteModel,
  }
}
