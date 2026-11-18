<template>
  <SettingsShell width="narrow">
    <div class="space-y-4 sm:space-y-6">
      <!-- Identity card: same header shape as every provider detail page. -->
      <ProviderIdentityCard :name="curProvider.name">
        <template #media>
          <ProviderIcon
            v-if="curProvider.icon"
            :icon="curProvider.icon"
            size="1.5em"
          />
          <span
            v-else
            class="text-xs font-medium text-muted-foreground"
          >
            {{ avatarInitials(curProvider.name, '?') }}
          </span>
        </template>
        <template #actions>
          <span class="text-xs text-muted-foreground">
            {{ t('common.enable') }}
          </span>
          <Switch
            :model-value="curProvider.enable ?? false"
            :disabled="enableLoading || saveLoading"
            :aria-label="t('common.enable')"
            @update:model-value="handleToggleEnable"
          />
        </template>
      </ProviderIdentityCard>

      <!-- Provider configuration card -->
      <form @submit.prevent="handleSaveProvider">
        <SettingsSection
          class="provider-configuration"
          :title="t('provider.configurationTitle')"
        >
          <div>
            <SettingsRow
              stack="sm"
              :label="t('common.name')"
            >
              <Input
                :id="`${domain}-provider-name`"
                v-model="providerName"
                type="text"
                class="w-full sm:w-80"
                :placeholder="t('common.namePlaceholder')"
              />
            </SettingsRow>

            <SchemaField
              v-for="field in orderedProviderFields"
              :key="`${formEpoch}:${field.key}`"
              :field="field"
              :field-key="field.key"
              :id-prefix="`${domain}-provider`"
              :model-value="providerConfig[field.key]"
              @update:model-value="(val) => { providerConfig[field.key] = val }"
            />
          </div>

          <template #footer>
            <LoadingButton
              type="submit"
              size="sm"
              :loading="saveLoading"
              :disabled="enableLoading"
            >
              {{ t('provider.saveChanges') }}
            </LoadingButton>
          </template>
        </SettingsSection>
      </form>

      <!-- Models: a list; editing a model opens a dialog rather than an inline accordion. -->
      <SettingsSection :title="t(spec.modelsTitleKey)">
        <template
          v-if="curProviderId"
          #actions
        >
          <LoadingButton
            type="button"
            variant="outline"
            size="sm"
            :loading="importLoading"
            @click="handleImportModels"
          >
            {{ t(spec.importModelsKey) }}
          </LoadingButton>
          <CreateModel
            :id="curProviderId"
            :default-type="domain"
            hide-type
            :type-options="[spec.modelTypeOption]"
            :invalidate-keys="[spec.providerModelsKey, spec.modelsKey]"
          />
        </template>

        <div
          v-if="providerModels.length === 0"
          class="py-10 text-center text-xs text-muted-foreground"
        >
          {{ t(spec.noModelsKey) }}
        </div>

        <template v-else>
          <ModelListRow
            v-for="(model, index) in providerModels"
            :key="model.id || model.model_id"
            :label="model.name || model.model_id || ''"
            :meta="model.name && model.name !== model.model_id ? model.model_id : ''"
            :last="index === providerModels.length - 1"
            :readonly="!model.id"
            @click="openModelEditor(model)"
          />
        </template>
      </SettingsSection>

      <Dialog
        v-model:open="modelDialogOpen"
        @update:open="(open) => { if (!open) editingModel = null }"
      >
        <DialogContent class="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>
              {{ editingModel?.name || editingModel?.model_id }}
            </DialogTitle>
          </DialogHeader>
          <ModelConfigEditor
            v-if="editingModel"
            :key="editingModel.id"
            :model-id="editingModel.id ?? ''"
            :config="editingModel.config || {}"
            :schema="getModelSchema(editingModel.model_id ?? '')"
            :mode="spec.editorMode"
            :on-test="(payload, cfg) => spec.testModel(editingModel?.id ?? '', payload, cfg)"
            :on-save="(cfg) => handleSaveModel(editingModel?.id ?? '', cfg)"
          />
        </DialogContent>
      </Dialog>
    </div>
  </SettingsShell>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  Input,
  ModelListRow,
  SettingsRow,
  SettingsSection,
  SettingsShell,
  Switch,
  toast,
} from '@felinic/ui'
import {
  getSpeechProvidersById,
  getSpeechProvidersByIdModels,
  getSpeechProvidersMeta,
  getTranscriptionProvidersById,
  getTranscriptionProvidersByIdModels,
  getTranscriptionProvidersMeta,
  postProvidersFromTemplate,
  postSpeechModelsByIdTest,
  postSpeechProvidersByIdImportModels,
  postTranscriptionModelsByIdTest,
  postTranscriptionProvidersByIdImportModels,
  putProvidersById,
  putSpeechModelsById,
  putTranscriptionModelsById,
} from '@memohai/sdk'
import type {
  AudioConfigSchema,
  AudioImportModelsResponse,
  AudioModelInfo,
  AudioProviderMetaResponse,
  AudioSpeechModelResponse,
  AudioSpeechProviderResponse,
  AudioTestTranscriptionResponse,
  AudioTranscriptionModelResponse,
  ProvidersGetResponse,
} from '@memohai/sdk'
import SchemaField from '@/components/config-schema-form/schema-field.vue'
import CreateModel from '@/components/create-model/index.vue'
import LoadingButton from '@/components/loading-button/index.vue'
import ProviderIcon from '@/components/provider-icon/index.vue'
import ProviderIdentityCard from '@/components/provider-identity-card/index.vue'
import { avatarInitials } from '@/composables/useAvatarInitials'
import { useProviderTemplateModels } from '@/composables/useProviderTemplateModels'
import { useServerSyncedRecord, useServerSyncedScalar } from '@/composables/use-server-synced-form'
import { resolveApiErrorMessage } from '@/utils/api-error'
import ModelConfigEditor from './model-config-editor.vue'
import type { AudioFieldWithKey } from './model-config-fields.vue'

export type AudioDomain = 'speech' | 'transcription'
export type TemplateAudioProvider = AudioSpeechProviderResponse & {
  provider_template_id?: string
}

type AudioModelRecord = AudioSpeechModelResponse | AudioTranscriptionModelResponse

interface AudioDomainSpec {
  providersKey: string
  providerDetailKey: string
  metaKey: string
  providerModelsKey: string
  modelsKey: string
  modelsTitleKey: string
  noModelsKey: string
  importModelsKey: string
  importSuccessKey: string
  importFailedKey: string
  saveSuccessKey: string
  modelTypeOption: { value: string, label: string }
  editorMode: 'synthesis' | 'transcription'
  getProviderDetail: (id: string) => Promise<AudioSpeechProviderResponse | null>
  getMeta: () => Promise<AudioProviderMetaResponse[]>
  getProviderModels: (id: string) => Promise<AudioModelRecord[]>
  importModels: (id: string) => Promise<AudioImportModelsResponse | undefined>
  updateModel: (id: string, body: { name?: string, config?: Record<string, unknown> }) => Promise<unknown>
  testModel: (modelId: string, payload: string | File, config: Record<string, unknown>) => Promise<Blob | AudioTestTranscriptionResponse>
  pickModelList: (meta: AudioProviderMetaResponse) => AudioModelInfo[]
  pickDefaultModelId: (meta: AudioProviderMetaResponse) => string | undefined
}

const DOMAIN_SPECS: Record<AudioDomain, AudioDomainSpec> = {
  speech: {
    providersKey: 'speech-providers',
    providerDetailKey: 'speech-provider-detail',
    metaKey: 'speech-providers-meta',
    providerModelsKey: 'speech-provider-models',
    modelsKey: 'speech-models',
    modelsTitleKey: 'speech.synthesis.models',
    noModelsKey: 'speech.noModels',
    importModelsKey: 'speech.importModels',
    importSuccessKey: 'speech.importSuccess',
    importFailedKey: 'speech.importFailed',
    saveSuccessKey: 'speech.saveSuccess',
    modelTypeOption: { value: 'speech', label: 'Speech' },
    editorMode: 'synthesis',
    async getProviderDetail(id) {
      const { data } = await getSpeechProvidersById({ path: { id }, throwOnError: true })
      return data ?? null
    },
    async getMeta() {
      const { data } = await getSpeechProvidersMeta({ throwOnError: true })
      return data ?? []
    },
    async getProviderModels(id) {
      const { data } = await getSpeechProvidersByIdModels({ path: { id }, throwOnError: true })
      return data ?? []
    },
    async importModels(id) {
      const { data } = await postSpeechProvidersByIdImportModels({ path: { id }, throwOnError: true })
      return data
    },
    updateModel(id, body) {
      return putSpeechModelsById({ path: { id }, body, throwOnError: true })
    },
    async testModel(modelId, payload, config) {
      const { data } = await postSpeechModelsByIdTest({
        path: { id: modelId },
        body: { text: payload as string, config },
        parseAs: 'blob',
        throwOnError: true,
      })
      return data as Blob
    },
    pickModelList: meta => meta.synthesis_models ?? meta.models ?? [],
    pickDefaultModelId: meta => meta.default_synthesis_model ?? meta.default_model,
  },
  transcription: {
    providersKey: 'transcription-providers',
    providerDetailKey: 'transcription-provider-detail',
    metaKey: 'transcription-providers-meta',
    providerModelsKey: 'transcription-provider-models',
    modelsKey: 'transcription-models',
    modelsTitleKey: 'transcription.models',
    noModelsKey: 'transcription.noModels',
    importModelsKey: 'transcription.importModels',
    importSuccessKey: 'transcription.importSuccess',
    importFailedKey: 'transcription.importFailed',
    saveSuccessKey: 'transcription.saveSuccess',
    modelTypeOption: { value: 'transcription', label: 'Transcription' },
    editorMode: 'transcription',
    async getProviderDetail(id) {
      const { data } = await getTranscriptionProvidersById({ path: { id }, throwOnError: true })
      return data ?? null
    },
    async getMeta() {
      const { data } = await getTranscriptionProvidersMeta({ throwOnError: true })
      return data ?? []
    },
    async getProviderModels(id) {
      const { data } = await getTranscriptionProvidersByIdModels({ path: { id }, throwOnError: true })
      return data ?? []
    },
    async importModels(id) {
      const { data } = await postTranscriptionProvidersByIdImportModels({ path: { id }, throwOnError: true })
      return data
    },
    updateModel(id, body) {
      return putTranscriptionModelsById({ path: { id }, body, throwOnError: true })
    },
    async testModel(modelId, payload, config) {
      const { data } = await postTranscriptionModelsByIdTest({
        path: { id: modelId },
        body: {
          file: payload as File,
          config: JSON.stringify(config),
        },
        throwOnError: true,
      })
      return (data ?? {}) as AudioTestTranscriptionResponse
    },
    pickModelList: meta => meta.transcription_models ?? meta.models ?? [],
    pickDefaultModelId: meta => meta.default_transcription_model ?? meta.default_model,
  },
}

const props = defineProps<{
  domain: AudioDomain
  provider: TemplateAudioProvider
}>()

const emit = defineEmits<{
  'update:provider': [provider: TemplateAudioProvider]
  materialized: [provider: ProvidersGetResponse]
}>()

const { t } = useI18n()
const queryCache = useQueryCache()
const spec = computed(() => DOMAIN_SPECS[props.domain])

const curProvider = computed({
  get: () => props.provider,
  set: value => emit('update:provider', value),
})
const curProviderId = computed(() => curProvider.value.id)
const curProviderTemplateId = computed(() =>
  curProviderId.value ? undefined : curProvider.value.provider_template_id,
)

const { models: templateModels } = useProviderTemplateModels(curProviderTemplateId)

const providerName = ref('')
const providerConfig = reactive<Record<string, unknown>>({})
const modelDialogOpen = ref(false)
const editingModel = ref<AudioModelRecord | null>(null)
const enableLoading = ref(false)
const saveLoading = ref(false)
const importLoading = ref(false)
let materializePromise: Promise<ProvidersGetResponse> | null = null

const { data: providerDetail } = useQuery({
  key: () => [spec.value.providerDetailKey, curProviderId.value ?? ''],
  query: async () => {
    if (!curProviderId.value) return null
    return spec.value.getProviderDetail(curProviderId.value)
  },
})

const { data: metaList } = useQuery({
  key: () => [spec.value.metaKey],
  query: () => spec.value.getMeta(),
})

const currentMeta = computed(() => {
  const clientType = curProvider.value.client_type
  if (!clientType) return null
  return (metaList.value ?? []).find(meta => meta.provider === clientType) ?? null
})

const orderedProviderFields = computed<AudioFieldWithKey[]>(() => {
  const fields = (currentMeta.value?.config_schema?.fields ?? []).filter((field): field is AudioFieldWithKey =>
    typeof field.key === 'string' && field.key.length > 0,
  )
  return [...fields].sort((a, b) => (a.order ?? 0) - (b.order ?? 0))
})

const { data: providerModelsData } = useQuery({
  key: () => [spec.value.providerModelsKey, curProviderId.value ?? ''],
  query: async () => {
    if (!curProviderId.value) return []
    return spec.value.getProviderModels(curProviderId.value)
  },
})

const providerModels = computed<AudioModelRecord[]>(() => {
  if (curProviderId.value) return providerModelsData.value ?? []
  return templateModels.value.map(model => ({
    model_id: model.model_id,
    name: model.name,
    provider_type: curProvider.value.client_type,
    config: model.config,
  }))
})

// providerName/providerConfig are shared between the user's draft and the
// server snapshot that colada's refetchOnWindowFocus can replace at any
// moment — the composables reconcile them (hard reset on provider switch,
// per-field guard on background refresh). Watching both providerDetail and
// curProvider ensures template-to-template and uncached switches still reset.
const formEpoch = ref(0)

function activeIdentity(
  detail: AudioSpeechProviderResponse | null | undefined = providerDetail.value,
  provider: TemplateAudioProvider = curProvider.value,
): string {
  const id = detail?.id ?? provider.id
  if (id) return `id:${id}`
  if (provider.provider_template_id) return `template:${provider.provider_template_id}`
  return `client:${provider.client_type ?? ''}`
}

const { markClean: markNameClean } = useServerSyncedScalar(providerName, {
  source: () => ({ detail: providerDetail.value, provider: curProvider.value }),
  identity: ({ detail, provider }) => activeIdentity(detail, provider),
  server: ({ detail, provider }) => detail?.name ?? provider.name ?? '',
  deep: true,
})
const { markClean: markConfigClean } = useServerSyncedRecord(providerConfig, {
  source: () => ({ detail: providerDetail.value, provider: curProvider.value }),
  identity: ({ detail, provider }) => activeIdentity(detail, provider),
  server: ({ detail, provider }) => ({ ...(detail?.config ?? provider.config ?? {}) }),
  deep: true,
  onSync: (hardReset) => {
    if (hardReset) {
      formEpoch.value++
      modelDialogOpen.value = false
      editingModel.value = null
    }
  },
})

function getModelSchema(modelID: string): AudioConfigSchema | null {
  const meta = currentMeta.value
  if (!meta) return null
  const models = spec.value.pickModelList(meta)
  const exact = models.find(item => item.id === modelID)
  const defaultId = spec.value.pickDefaultModelId(meta)
  const fallback = exact ?? (defaultId ? models.find(item => item.id === defaultId) : undefined) ?? models[0]
  return fallback?.config_schema ?? fallback?.capabilities?.config_schema ?? null
}

function openModelEditor(model: AudioModelRecord) {
  if (!model.id) return
  editingModel.value = model
  modelDialogOpen.value = true
}

function showErrorToast(error: unknown, fallbackKey: string) {
  const msg = resolveApiErrorMessage(error, t(fallbackKey))
  if (msg) toast.error(msg)
}

async function handleToggleEnable(value: boolean) {
  const prev = curProvider.value.enable ?? false
  const targetIdentity = activeIdentity()
  const providerId = curProviderId.value
  const savedName = providerName.value.trim() || curProvider.value.name || ''
  const savedConfig = sanitizeConfig(providerConfig)
  curProvider.value = { ...curProvider.value, enable: value }

  enableLoading.value = true
  try {
    if (!providerId) {
      await materializeProvider(value)
      return
    }
    await putProvidersById({
      path: { id: providerId },
      body: {
        name: savedName,
        client_type: curProvider.value.client_type,
        enable: value,
        config: savedConfig,
      },
      throwOnError: true,
    })
    if (activeIdentity() === targetIdentity) {
      markNameClean(savedName)
      markConfigClean(savedConfig)
    }
    queryCache.invalidateQueries({ key: [spec.value.providersKey] })
    queryCache.invalidateQueries({ key: [spec.value.providerDetailKey, providerId] })
  } catch (error) {
    if (activeIdentity() === targetIdentity) {
      curProvider.value = { ...curProvider.value, enable: prev }
    }
    showErrorToast(error, 'common.saveFailed')
  } finally {
    enableLoading.value = false
  }
}

async function handleSaveProvider() {
  const targetIdentity = activeIdentity()
  const providerId = curProviderId.value
  const savedName = providerName.value.trim() || curProvider.value.name || ''
  const savedConfig = sanitizeConfig(providerConfig)

  saveLoading.value = true
  try {
    if (!providerId) {
      await materializeProvider(false)
      toast.success(t(spec.value.saveSuccessKey))
      return
    }
    await putProvidersById({
      path: { id: providerId },
      body: {
        name: savedName,
        client_type: curProvider.value.client_type,
        enable: curProvider.value.enable,
        config: savedConfig,
      },
      throwOnError: true,
    })
    if (activeIdentity() === targetIdentity) {
      markNameClean(savedName)
      markConfigClean(savedConfig)
    }
    toast.success(t(spec.value.saveSuccessKey))
    queryCache.invalidateQueries({ key: [spec.value.providersKey] })
    queryCache.invalidateQueries({ key: [spec.value.providerDetailKey, providerId] })
  } catch (error) {
    showErrorToast(error, 'common.saveFailed')
  } finally {
    saveLoading.value = false
  }
}

async function materializeProvider(enable: boolean) {
  if (curProvider.value.id) return curProvider.value as ProvidersGetResponse
  if (materializePromise) return materializePromise
  const templateId = curProvider.value.provider_template_id
  if (!templateId) throw new Error(`${props.domain} provider template is missing`)

  const activeSpec = spec.value
  const activeDomain = props.domain
  const targetIdentity = activeIdentity()
  const savedName = providerName.value.trim() || curProvider.value.name || ''
  const savedConfig = sanitizeConfig(providerConfig)

  materializePromise = (async () => {
    const { data: created } = await postProvidersFromTemplate({
      body: {
        template_id: templateId,
        domain: activeDomain,
        name: savedName,
        config: savedConfig,
      },
      throwOnError: true,
    })
    if (!created?.id) throw new Error(`${activeDomain} provider creation returned no id`)

    let result = created
    if (!enable) {
      const response = await putProvidersById({
        path: { id: created.id },
        body: { enable: false },
        throwOnError: true,
      })
      result = response.data ?? { ...created, enable: false }
    }

    if (activeIdentity() === targetIdentity) {
      const nextIdentity = `id:${created.id}`
      markNameClean(savedName, nextIdentity)
      markConfigClean(savedConfig, nextIdentity)
      curProvider.value = result
      emit('materialized', result)
    }
    queryCache.invalidateQueries({ key: [activeSpec.providersKey] })
    queryCache.invalidateQueries({ key: ['provider-templates', activeDomain] })

    try {
      await activeSpec.importModels(created.id)
      queryCache.invalidateQueries({ key: [activeSpec.providerModelsKey, created.id] })
      queryCache.invalidateQueries({ key: [activeSpec.modelsKey] })
    } catch (error) {
      showErrorToast(error, activeSpec.importFailedKey)
    }
    return result
  })()

  try {
    return await materializePromise
  } finally {
    materializePromise = null
  }
}

async function handleSaveModel(modelId: string, config: Record<string, unknown>) {
  const model = providerModels.value.find(item => item.id === modelId)
  if (!model) return
  try {
    await spec.value.updateModel(modelId, {
      name: model.name ?? model.model_id ?? modelId,
      config,
    })
    toast.success(t(spec.value.saveSuccessKey))
    queryCache.invalidateQueries({ key: [spec.value.providerModelsKey, curProviderId.value ?? ''] })
    queryCache.invalidateQueries({ key: [spec.value.modelsKey] })
  } catch (error) {
    showErrorToast(error, 'common.saveFailed')
  }
}

async function handleImportModels() {
  if (!curProviderId.value) return
  importLoading.value = true
  try {
    const data = await spec.value.importModels(curProviderId.value)
    toast.success(t(spec.value.importSuccessKey, {
      created: data?.created ?? 0,
      skipped: data?.skipped ?? 0,
    }))
    queryCache.invalidateQueries({ key: [spec.value.providerModelsKey, curProviderId.value ?? ''] })
    queryCache.invalidateQueries({ key: [spec.value.modelsKey] })
    queryCache.invalidateQueries({ key: [spec.value.metaKey] })
  } catch (error) {
    showErrorToast(error, spec.value.importFailedKey)
  } finally {
    importLoading.value = false
  }
}

function sanitizeConfig(input: Record<string, unknown>) {
  const result: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(input)) {
    if (value === '' || value == null) continue
    result[key] = value
  }
  return result
}
</script>
