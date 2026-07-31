<template>
  <SettingsShell width="narrow">
    <div class="space-y-6">
      <!-- Identity card: same header shape as the provider detail. -->
      <section class="flex items-center gap-3 rounded-[var(--radius-menu-shell)] border border-border bg-card px-4 py-3">
        <span class="flex size-9 shrink-0 items-center justify-center rounded-full bg-muted">
          <ProviderIcon
            v-if="curProvider?.icon"
            :icon="curProvider.icon"
            size="1.5em"
          />
          <span
            v-else
            class="text-xs font-medium text-muted-foreground"
          >
            {{ getInitials(curProvider?.name) }}
          </span>
        </span>
        <div class="min-w-0 flex-1">
          <h2 class="truncate text-sm font-semibold">
            {{ curProvider?.name }}
          </h2>
        </div>
        <div class="ml-auto flex shrink-0 items-center gap-2">
          <span class="text-xs text-muted-foreground">
            {{ $t('common.enable') }}
          </span>
          <Switch
            :model-value="curProvider?.enable ?? false"
            :disabled="enableLoading"
            :aria-label="$t('common.enable')"
            @update:model-value="handleToggleEnable"
          />
        </div>
      </section>

      <!-- Provider configuration card -->
      <form @submit.prevent="handleSaveProvider">
        <SettingsSection :title="$t('provider.configurationTitle')">
          <div>
            <SettingsRow :label="$t('common.name')">
              <Input
                id="speech-provider-name"
                v-model="providerName"
                type="text"
                class="w-80"
                :placeholder="$t('common.namePlaceholder')"
              />
            </SettingsRow>

            <SettingsRow
              v-for="field in orderedProviderFields"
              :key="field.key"
              :label="field.title || field.key"
              :description="field.description"
            >
              <div
                v-if="field.type === 'secret'"
                class="relative w-80"
              >
                <Input
                  :id="`speech-provider-${field.key}`"
                  v-model="providerConfig[field.key] as string"
                  :type="visibleSecrets[field.key] ? 'text' : 'password'"
                  class="pr-9"
                  :placeholder="field.example ? String(field.example) : ''"
                />
                <button
                  type="button"
                  class="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                  @click="visibleSecrets[field.key] = !visibleSecrets[field.key]"
                >
                  <component
                    :is="visibleSecrets[field.key] ? EyeOff : Eye"
                    class="size-3.5"
                  />
                </button>
              </div>
              <Switch
                v-else-if="field.type === 'bool'"
                :model-value="!!providerConfig[field.key]"
                @update:model-value="(val) => providerConfig[field.key] = !!val"
              />
              <Input
                v-else-if="field.type === 'number'"
                :id="`speech-provider-${field.key}`"
                v-model.number="providerConfig[field.key] as number"
                type="number"
                class="w-80"
                :placeholder="field.example ? String(field.example) : ''"
              />
              <Select
                v-else-if="field.type === 'enum' && field.enum"
                :model-value="String(providerConfig[field.key] ?? '')"
                @update:model-value="(val) => providerConfig[field.key] = val"
              >
                <SelectTrigger class="w-80">
                  <SelectValue :placeholder="field.title || field.key" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem
                    v-for="opt in field.enum"
                    :key="opt"
                    :value="opt"
                  >
                    {{ opt }}
                  </SelectItem>
                </SelectContent>
              </Select>
              <Input
                v-else
                :id="`speech-provider-${field.key}`"
                v-model="providerConfig[field.key] as string"
                type="text"
                class="w-80"
                :placeholder="field.example ? String(field.example) : ''"
              />
            </SettingsRow>
          </div>

          <template #footer>
            <LoadingButton
              type="submit"
              size="sm"
              :loading="saveLoading"
            >
              {{ $t('provider.saveChanges') }}
            </LoadingButton>
          </template>
        </SettingsSection>
      </form>

      <!-- Synthesis models: a list; editing a model opens a dialog (the same
           shape as the provider's model management), not an inline accordion. -->
      <SettingsSection :title="$t('speech.synthesis.models')">
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
            {{ $t('speech.importModels') }}
          </LoadingButton>
          <CreateModel
            :id="curProviderId"
            default-type="speech"
            hide-type
            :type-options="speechTypeOptions"
            :invalidate-keys="['speech-provider-models', 'speech-models']"
          />
        </template>

        <div
          v-if="providerModels.length === 0"
          class="px-4 py-10 text-center text-xs text-muted-foreground"
        >
          {{ $t('speech.noModels') }}
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

      <!-- Editing opens the config editor in a dialog rather than expanding the
           row in place. -->
      <Dialog v-model:open="modelDialogOpen">
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
            :model-name="editingModel.model_id ?? ''"
            :config="editingModel.config || {}"
            :schema="getModelSchema(editingModel.model_id ?? '')"
            :on-test="(text, cfg) => handleTestModel(editingModel?.id ?? '', text as string, cfg)"
            @save="(cfg) => handleSaveModel(editingModel?.id ?? '', cfg)"
          />
        </DialogContent>
      </Dialog>
    </div>
  </SettingsShell>
</template>

<script setup lang="ts">
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Switch,
} from '@felinic/ui'
import ModelConfigEditor from './model-config-editor.vue'
import { Eye, EyeOff } from 'lucide-vue-next'
import { computed, inject, reactive, ref, watch } from 'vue'
import { ModelListRow, SettingsRow, SettingsSection, SettingsShell, toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import { getSpeechProvidersById, getSpeechProvidersByIdModels, getSpeechProvidersMeta, postProvidersFromTemplate, postSpeechModelsByIdTest, postSpeechProvidersByIdImportModels, putProvidersById, putSpeechModelsById } from '@memohai/sdk'
import type { AudioSpeechModelResponse, AudioSpeechProviderResponse, ProvidersGetResponse } from '@memohai/sdk'
import LoadingButton from '@/components/loading-button/index.vue'
import ProviderIcon from '@/components/provider-icon/index.vue'
import CreateModel from '@/components/create-model/index.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useProviderTemplateModels } from '@/composables/useProviderTemplateModels'

interface SpeechFieldSchema {
  key: string
  type: string
  title?: string
  description?: string
  required?: boolean
  advanced?: boolean
  enum?: string[]
  example?: unknown
  order?: number
}

interface SpeechConfigSchema {
  fields?: SpeechFieldSchema[]
}

interface SpeechModelMeta {
  id: string
  name: string
  description?: string
  config_schema?: SpeechConfigSchema
  capabilities?: {
    config_schema?: SpeechConfigSchema
  }
}

interface SpeechProviderMeta {
  provider: string
  display_name: string
  description?: string
  config_schema?: SpeechConfigSchema
  default_model?: string
  models?: SpeechModelMeta[]
  default_synthesis_model?: string
  synthesis_models?: SpeechModelMeta[]
}

function getInitials(name: string | undefined) {
  const label = name?.trim() ?? ''
  return label ? label.slice(0, 2).toUpperCase() : '?'
}

const { t } = useI18n()
type TemplateSpeechProvider = AudioSpeechProviderResponse & { provider_template_id?: string }
const curProvider = inject('curTtsProvider', ref<TemplateSpeechProvider>())
const emit = defineEmits<{
  materialized: [provider: ProvidersGetResponse]
}>()
const curProviderId = computed(() => curProvider.value?.id)
const curProviderTemplateId = computed(() => curProviderId.value
  ? undefined
  : curProvider.value?.provider_template_id)
const { models: templateModels } = useProviderTemplateModels(curProviderTemplateId)
const providerName = ref('')
const providerConfig = reactive<Record<string, unknown>>({})
const visibleSecrets = reactive<Record<string, boolean>>({})
const modelDialogOpen = ref(false)
const editingModel = ref<AudioSpeechModelResponse | null>(null)
const enableLoading = ref(false)
const saveLoading = ref(false)
const importLoading = ref(false)
const queryCache = useQueryCache()
let materializePromise: Promise<ProvidersGetResponse> | null = null
const speechTypeOptions = [
  { value: 'speech', label: 'Speech' },
]

const { data: providerDetail } = useQuery({
  key: () => ['speech-provider-detail', curProviderId.value ?? ''],
  query: async () => {
    if (!curProviderId.value) return null
    const { data } = await getSpeechProvidersById({
      path: { id: curProviderId.value },
      throwOnError: true,
    })
    return data ?? null
  },
})

const { data: metaList } = useQuery({
  key: () => ['speech-providers-meta'],
  query: async () => {
    const { data } = await getSpeechProvidersMeta({ throwOnError: true })
    return (data ?? []) as SpeechProviderMeta[]
  },
})

const currentMeta = computed(() => {
  if (!metaList.value || !curProvider.value?.client_type) return null
  return (metaList.value as SpeechProviderMeta[]).find(m => m.provider === curProvider.value?.client_type) ?? null
})

const orderedProviderFields = computed(() => {
  const fields = currentMeta.value?.config_schema?.fields ?? []
  return [...fields].sort((a, b) => (a.order ?? 0) - (b.order ?? 0))
})

const { data: providerSpeechModels } = useQuery({
  key: () => ['speech-provider-models', curProviderId.value ?? ''],
  query: async () => {
    if (!curProviderId.value) return []
    const { data } = await getSpeechProvidersByIdModels({
      path: { id: curProviderId.value },
      throwOnError: true,
    })
    return data ?? []
  },
})

const providerModels = computed<AudioSpeechModelResponse[]>(() => {
  if (curProviderId.value) {
    return (providerSpeechModels.value as AudioSpeechModelResponse[] | undefined) ?? []
  }
  return templateModels.value.map(model => ({
    model_id: model.model_id,
    name: model.name,
    provider_type: curProvider.value?.client_type,
    config: model.config,
  }))
})

watch(() => providerDetail.value, (provider) => {
  providerName.value = provider?.name ?? curProvider.value?.name ?? ''
  Object.keys(providerConfig).forEach((key) => delete providerConfig[key])
  Object.assign(providerConfig, { ...(provider?.config ?? curProvider.value?.config ?? {}) })
}, { immediate: true, deep: true })

function getModelMeta(modelID: string): SpeechModelMeta | null {
  const models = currentMeta.value?.synthesis_models ?? currentMeta.value?.models ?? []
  const exact = models.find(m => m.id === modelID)
  if (exact) return exact
  const defaultModel = currentMeta.value?.default_synthesis_model ?? currentMeta.value?.default_model
  if (defaultModel) return models.find(m => m.id === defaultModel) ?? null
  return models[0] ?? null
}

function getModelSchema(modelID: string): SpeechConfigSchema | null {
  const meta = getModelMeta(modelID)
  return meta?.config_schema ?? meta?.capabilities?.config_schema ?? null
}

function openModelEditor(model: AudioSpeechModelResponse) {
  if (!model.id) return
  editingModel.value = model
  modelDialogOpen.value = true
}

async function handleToggleEnable(value: boolean) {
  if (!curProvider.value) return
  const prev = curProvider.value.enable ?? false
  curProvider.value = { ...curProvider.value, enable: value }

  enableLoading.value = true
  try {
    if (!curProviderId.value) {
      await materializeProvider(value)
      return
    }
    await putProvidersById({
      path: { id: curProviderId.value },
      body: {
        name: providerName.value.trim() || curProvider.value.name,
        client_type: curProvider.value.client_type,
        enable: value,
        config: sanitizeConfig(providerConfig),
      },
      throwOnError: true,
    })
    queryCache.invalidateQueries({ key: ['speech-providers'] })
    queryCache.invalidateQueries({ key: ['speech-provider-detail', curProviderId.value] })
  } catch {
    curProvider.value = { ...curProvider.value, enable: prev }
    toast.error(t('common.saveFailed'))
  } finally {
    enableLoading.value = false
  }
}

async function handleSaveProvider() {
  if (!curProvider.value) return
  saveLoading.value = true
  try {
    if (!curProviderId.value) {
      await materializeProvider(false)
      toast.success(t('speech.saveSuccess'))
      return
    }
    await putProvidersById({
      path: { id: curProviderId.value },
      body: {
        name: providerName.value.trim() || curProvider.value.name,
        client_type: curProvider.value.client_type,
        enable: curProvider.value.enable,
        config: sanitizeConfig(providerConfig),
      },
      throwOnError: true,
    })
    toast.success(t('speech.saveSuccess'))
    queryCache.invalidateQueries({ key: ['speech-providers'] })
    queryCache.invalidateQueries({ key: ['speech-provider-detail', curProviderId.value] })
  } catch {
    toast.error(t('common.saveFailed'))
  } finally {
    saveLoading.value = false
  }
}

async function materializeProvider(enable: boolean) {
  if (curProvider.value?.id) return curProvider.value as ProvidersGetResponse
  if (materializePromise) return materializePromise
  const templateId = curProvider.value?.provider_template_id
  if (!templateId) throw new Error('speech provider template is missing')

  materializePromise = (async () => {
    const { data: created } = await postProvidersFromTemplate({
      body: {
        template_id: templateId,
        domain: 'speech',
        name: providerName.value.trim() || curProvider.value?.name || '',
        config: sanitizeConfig(providerConfig),
      },
      throwOnError: true,
    })
    if (!created?.id) throw new Error('speech provider creation returned no id')

    let result = created
    if (!enable) {
      const response = await putProvidersById({
        path: { id: created.id },
        body: { enable: false },
        throwOnError: true,
      })
      result = response.data ?? { ...created, enable: false }
    }

    curProvider.value = result
    emit('materialized', result)
    queryCache.invalidateQueries({ key: ['speech-providers'] })
    queryCache.invalidateQueries({ key: ['provider-templates', 'speech'] })

    try {
      await postSpeechProvidersByIdImportModels({
        path: { id: created.id },
        throwOnError: true,
      })
      queryCache.invalidateQueries({ key: ['speech-provider-models', created.id] })
      queryCache.invalidateQueries({ key: ['speech-models'] })
    } catch {
      toast.error(t('speech.importFailed'))
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
    await putSpeechModelsById({
      path: { id: modelId },
      body: {
        name: model.name ?? model.model_id,
        config,
      },
      throwOnError: true,
    })
    toast.success(t('speech.saveSuccess'))
    queryCache.invalidateQueries({ key: ['speech-provider-models', curProviderId.value ?? ''] })
    queryCache.invalidateQueries({ key: ['speech-models'] })
  } catch {
    toast.error(t('common.saveFailed'))
  }
}

async function handleImportModels() {
  if (!curProviderId.value) return
  importLoading.value = true
  try {
    const { data } = await postSpeechProvidersByIdImportModels({
      path: { id: curProviderId.value },
      throwOnError: true,
    })
    toast.success(t('speech.importSuccess', {
      created: data?.created ?? 0,
      skipped: data?.skipped ?? 0,
    }))
    queryCache.invalidateQueries({ key: ['speech-provider-models', curProviderId.value ?? ''] })
    queryCache.invalidateQueries({ key: ['speech-models'] })
    queryCache.invalidateQueries({ key: ['speech-providers-meta'] })
  } catch {
    toast.error(t('speech.importFailed'))
  } finally {
    importLoading.value = false
  }
}

async function handleTestModel(modelId: string, text: string, config: Record<string, unknown>) {
  try {
    const { data } = await postSpeechModelsByIdTest({
      path: { id: modelId },
      body: { text, config },
      parseAs: 'blob',
      throwOnError: true,
    })
    return data as Blob
  } catch (error) {
    throw new Error(resolveApiErrorMessage(error, t('speech.test.failed')))
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
