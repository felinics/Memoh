<template>
  <SettingsShell width="narrow">
    <div class="space-y-6">
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

      <form @submit.prevent="handleSaveProvider">
        <SettingsSection :title="$t('provider.configurationTitle')">
          <div>
            <SettingsRow :label="$t('common.name')">
              <Input
                id="video-provider-name"
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
              <PasswordInput
                v-if="field.type === 'secret'"
                :id="`video-provider-${field.key}`"
                v-model="providerConfig[field.key] as string"
                class="w-80"
                :placeholder="field.example ? String(field.example) : ''"
              />
              <Switch
                v-else-if="field.type === 'bool'"
                :model-value="!!providerConfig[field.key]"
                @update:model-value="(val) => providerConfig[field.key] = !!val"
              />
              <Input
                v-else-if="field.type === 'number'"
                :id="`video-provider-${field.key}`"
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
                :id="`video-provider-${field.key}`"
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

      <section class="space-y-2.5">
        <div class="flex min-h-7 items-center justify-between gap-2 px-2">
          <h2 class="text-sm font-medium text-muted-foreground">
            {{ $t('video.models') }}
          </h2>
          <div
            v-if="curProviderId"
            class="flex items-center gap-2"
          >
            <LoadingButton
              type="button"
              variant="outline"
              size="sm"
              :loading="importLoading"
              @click="handleImportModels"
            >
              {{ $t('video.importModels') }}
            </LoadingButton>
            <CreateModel
              :id="curProviderId"
              default-type="video"
              hide-type
              :type-options="videoTypeOptions"
              :invalidate-keys="['video-provider-models', 'video-models']"
            />
          </div>
        </div>

        <div class="overflow-hidden rounded-[var(--radius-menu-shell)] border border-border bg-card">
          <div
            v-if="providerModels.length === 0"
            class="px-4 py-10 text-center text-xs text-muted-foreground"
          >
            {{ $t('video.noModels') }}
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
        </div>
      </section>

      <Dialog v-model:open="modelDialogOpen">
        <DialogContent class="sm:max-w-md">
          <form @submit.prevent="handleSaveModel">
            <DialogHeader>
              <DialogTitle>{{ $t('models.editModel') }}</DialogTitle>
            </DialogHeader>

            <FormStack class="mt-4">
              <FieldStack
                :label="$t('models.displayName')"
                for="video-model-name"
              >
                <Input
                  id="video-model-name"
                  v-model="modelForm.name"
                  type="text"
                  :placeholder="$t('models.displayNamePlaceholder')"
                />
              </FieldStack>

              <FieldStack
                :label="$t('models.model')"
                for="video-model-id"
              >
                <Input
                  id="video-model-id"
                  :model-value="editingModel?.model_id ?? ''"
                  type="text"
                  disabled
                />
              </FieldStack>
            </FormStack>

            <DialogFooter class="mt-4">
              <DialogClose as-child>
                <Button
                  type="button"
                  variant="outline"
                >
                  {{ $t('common.cancel') }}
                </Button>
              </DialogClose>
              <Button
                type="submit"
                :disabled="modelSaveLoading || !editingModel?.id"
              >
                <Spinner
                  v-if="modelSaveLoading"
                  class="mr-1 size-4"
                />
                {{ $t('common.confirm') }}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  </SettingsShell>
</template>

<script setup lang="ts">
import {
  Button,
  Dialog,
  DialogClose,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Spinner,
  Switch,
} from '@felinic/ui'
import { computed, inject, reactive, ref, watch } from 'vue'
import { FieldStack, FormStack, ModelListRow, SettingsRow, SettingsSection, SettingsShell, toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import { getVideoProvidersById, getVideoProvidersByIdModels, getVideoProvidersMeta, postProvidersFromTemplate, postVideoProvidersByIdImportModels, putProvidersById, putVideoModelsById } from '@memohai/sdk'
import type { ProvidersGetResponse, VideoModelResponse, VideoProviderResponse } from '@memohai/sdk'
import LoadingButton from '@/components/loading-button/index.vue'
import ProviderIcon from '@/components/provider-icon/index.vue'
import CreateModel from '@/components/create-model/index.vue'
import PasswordInput from '@/components/password-input/index.vue'
import { useProviderTemplateModels } from '@/composables/useProviderTemplateModels'

interface VideoFieldSchema {
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

interface VideoConfigSchema {
  fields?: VideoFieldSchema[]
}

interface VideoProviderMeta {
  provider: string
  display_name: string
  description?: string
  config_schema?: VideoConfigSchema
}

function getInitials(name: string | undefined) {
  const label = name?.trim() ?? ''
  return label ? label.slice(0, 2).toUpperCase() : '?'
}

const { t } = useI18n()
type TemplateVideoProvider = VideoProviderResponse & { provider_template_id?: string }
const curProvider = inject('curVideoProvider', ref<TemplateVideoProvider>())
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
const enableLoading = ref(false)
const saveLoading = ref(false)
const importLoading = ref(false)
const modelDialogOpen = ref(false)
const modelSaveLoading = ref(false)
const editingModel = ref<VideoModelResponse | null>(null)
const modelForm = reactive({
  name: '',
})
const queryCache = useQueryCache()
let materializePromise: Promise<ProvidersGetResponse> | null = null
const videoTypeOptions = [
  { value: 'video', label: 'Video' },
]

const { data: providerDetail } = useQuery({
  key: () => ['video-provider-detail', curProviderId.value ?? ''],
  query: async () => {
    if (!curProviderId.value) return null
    const { data } = await getVideoProvidersById({
      path: { id: curProviderId.value },
      throwOnError: true,
    })
    return data ?? null
  },
})

const { data: metaList } = useQuery({
  key: () => ['video-providers-meta'],
  query: async () => {
    const { data } = await getVideoProvidersMeta({ throwOnError: true })
    return (data ?? []) as VideoProviderMeta[]
  },
})

const currentMeta = computed(() => {
  if (!metaList.value || !curProvider.value?.client_type) return null
  return (metaList.value as VideoProviderMeta[]).find(m => m.provider === curProvider.value?.client_type) ?? null
})

const orderedProviderFields = computed(() => {
  const fields = currentMeta.value?.config_schema?.fields ?? []
  return [...fields].sort((a, b) => (a.order ?? 0) - (b.order ?? 0))
})

const { data: providerVideoModels } = useQuery({
  key: () => ['video-provider-models', curProviderId.value ?? ''],
  query: async () => {
    if (!curProviderId.value) return []
    const { data } = await getVideoProvidersByIdModels({
      path: { id: curProviderId.value },
      throwOnError: true,
    })
    return data ?? []
  },
})

const providerModels = computed<VideoModelResponse[]>(() => {
  if (curProviderId.value) {
    return (providerVideoModels.value as VideoModelResponse[] | undefined) ?? []
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
    queryCache.invalidateQueries({ key: ['video-providers'] })
    queryCache.invalidateQueries({ key: ['video-provider-detail', curProviderId.value] })
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
      toast.success(t('video.saveSuccess'))
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
    toast.success(t('video.saveSuccess'))
    queryCache.invalidateQueries({ key: ['video-providers'] })
    queryCache.invalidateQueries({ key: ['video-provider-detail', curProviderId.value] })
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
  if (!templateId) throw new Error('video provider template is missing')

  materializePromise = (async () => {
    const { data: created } = await postProvidersFromTemplate({
      body: {
        template_id: templateId,
        domain: 'video',
        name: providerName.value.trim() || curProvider.value?.name || '',
        config: sanitizeConfig(providerConfig),
      },
      throwOnError: true,
    })
    if (!created?.id) throw new Error('video provider creation returned no id')

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
    queryCache.invalidateQueries({ key: ['video-providers'] })
    queryCache.invalidateQueries({ key: ['provider-templates', 'video'] })

    try {
      await postVideoProvidersByIdImportModels({
        path: { id: created.id },
        throwOnError: true,
      })
      queryCache.invalidateQueries({ key: ['video-provider-models', created.id] })
      queryCache.invalidateQueries({ key: ['video-models'] })
    } catch {
      toast.error(t('video.importFailed'))
    }
    return result
  })()

  try {
    return await materializePromise
  } finally {
    materializePromise = null
  }
}

async function handleImportModels() {
  if (!curProviderId.value) return
  importLoading.value = true
  try {
    const { data } = await postVideoProvidersByIdImportModels({
      path: { id: curProviderId.value },
      throwOnError: true,
    })
    toast.success(t('video.importSuccess', {
      created: data?.created ?? 0,
      skipped: data?.skipped ?? 0,
    }))
    queryCache.invalidateQueries({ key: ['video-provider-models', curProviderId.value ?? ''] })
    queryCache.invalidateQueries({ key: ['video-models'] })
    queryCache.invalidateQueries({ key: ['video-providers-meta'] })
  } catch {
    toast.error(t('video.importFailed'))
  } finally {
    importLoading.value = false
  }
}

function openModelEditor(model: VideoModelResponse) {
  if (!model.id) return
  editingModel.value = model
  modelForm.name = model.name && model.name !== model.model_id ? model.name : ''
  modelDialogOpen.value = true
}

async function handleSaveModel() {
  const model = editingModel.value
  if (!model?.id) return

  modelSaveLoading.value = true
  try {
    await putVideoModelsById({
      path: { id: model.id },
      body: {
        name: modelForm.name.trim(),
        config: model.config ?? {},
      },
      throwOnError: true,
    })
    toast.success(t('video.modelSaveSuccess'))
    queryCache.invalidateQueries({ key: ['video-provider-models', curProviderId.value ?? ''] })
    queryCache.invalidateQueries({ key: ['video-models'] })
    modelDialogOpen.value = false
  } catch {
    toast.error(t('common.saveFailed'))
  } finally {
    modelSaveLoading.value = false
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
