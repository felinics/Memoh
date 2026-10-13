<template>
  <SettingsShell width="narrow">
    <div class="space-y-4 sm:space-y-6">
      <ProviderIdentityCard
        :name="curProvider?.name"
      >
        <template #media>
          <ProviderIcon
            v-if="curProvider?.icon"
            :icon="curProvider.icon"
            :monochrome="isMonochromeLogo(curProvider.icon)"
            size="1.5em"
          />
          <span
            v-else
            class="text-xs font-medium text-muted-foreground"
          >
            {{ avatarInitials(curProvider?.name, '?') }}
          </span>
        </template>
        <template #actions>
          <ConfirmPopover
            v-if="curProvider?.id"
            :message="$t('provider.deleteConfirm')"
            :cancel-text="$t('common.cancel')"
            :confirm-text="$t('common.confirm')"
            :loading="deleteLoading"
            @confirm="deleteProvider"
          >
            <template #trigger>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                class="text-muted-foreground hover:text-destructive"
                :aria-label="$t('common.delete')"
              >
                <Trash2 class="size-4" />
              </Button>
            </template>
          </ConfirmPopover>
          <Switch
            :model-value="curProvider?.enable ?? true"
            :disabled="enableLoading"
            :aria-label="$t('provider.enable')"
            @update:model-value="handleToggleEnable"
          />
        </template>
      </ProviderIdentityCard>

      <ProviderForm
        ref="providerForm"
        :provider="curProvider"
        :edit-loading="editLoading"
        :ensure-provider="ensureOAuthProvider"
        :save-provider="saveProvider"
      />

      <ModelList
        :provider-id="curProvider?.id"
        :models="providerModels"
        :managed="isManagedModelCatalogClientType(curProvider?.client_type)"
        :client-type="curProvider?.client_type"
        :source="providerSource(curProvider)"
        :preview="isSourcePreview"
        :preview-loading="isSourcePreview && catalogLoading"
        :ensure-provider-id="curProvider?.id ? undefined : ensureProviderId"
        :delete-model-loading="deleteModelLoading"
        @edit="handleEditModel"
        @delete="deleteModel"
      />
    </div>
  </SettingsShell>
</template>

<script setup lang="ts">
import ProviderIdentityCard from '@/components/provider-identity-card/index.vue'
import { Button, ConfirmPopover, SettingsShell, Switch } from '@felinic/ui'
import { Trash2 } from 'lucide-vue-next'
import ProviderIcon from '@/components/provider-icon/index.vue'
import { avatarInitials } from '@/composables/useAvatarInitials'

import ProviderForm from './components/provider-form.vue'
import ModelList from './components/model-list.vue'
import { computed, provide, reactive, ref, toRef, useTemplateRef, watch } from 'vue'
import { useQuery, useMutation, useQueryCache } from '@pinia/colada'
import {
  deleteModelsById,
  deleteProvidersById,
  getProvidersByIdModels,
  postProviders,
  putProvidersById,
} from '@memohai/sdk'
import type { ModelsGetResponse, ProvidersGetResponse, ProvidersUpdateRequest } from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import { toast } from '@felinic/ui'
import { isManagedModelCatalogClientType } from '@/constants/client-types'
import { isMonochromeLogo, providerSource } from '@/constants/model-sources'
import { useModelsDevCatalog } from '@/composables/useModelsDevCatalog'
import { useProviderModelCatalog } from '@/composables/useProviderModelCatalog'
import { catalogModelsFor } from '@/lib/models-dev'

// ---- Model 编辑状态（provide 给 CreateModel） ----
const openModel = reactive<{
  state: boolean
  title: 'title' | 'edit'
  curState: ModelsGetResponse | null
}>({
  state: false,
  title: 'title',
  curState: null,
})

provide('openModel', toRef(openModel, 'state'))
provide('openModelTitle', toRef(openModel, 'title'))
provide('openModelState', toRef(openModel, 'curState'))

function handleEditModel(model: ModelsGetResponse) {
  openModel.state = true
  openModel.title = 'edit'
  openModel.curState = { ...model }
}

// ---- 当前 Provider（父级 v-model:provider 下发，子写回自动回传） ----
const curProvider = defineModel<ProvidersGetResponse>('provider')
const emit = defineEmits<{
  materialized: [provider: ProvidersGetResponse]
}>()
const curProviderId = computed(() => curProvider.value?.id)
const enableLoading = ref(false)
const { t } = useI18n()

// ---- API Hooks ----
const queryCache = useQueryCache()
let materializePromise: Promise<ProvidersGetResponse> | null = null

function invalidateProviderQueries() {
  queryCache.invalidateQueries({ key: ['providers'] })
  queryCache.invalidateQueries({ key: ['models'] })
}

function invalidateModelQueries() {
  queryCache.invalidateQueries({ key: ['provider-models'] })
  queryCache.invalidateQueries({ key: ['models'] })
}

const { mutate: deleteProvider, isLoading: deleteLoading } = useMutation({
  mutation: async () => {
    if (!curProviderId.value) return
    await deleteProvidersById({ path: { id: curProviderId.value }, throwOnError: true })
  },
  onSettled: invalidateProviderQueries,
})

// mutateAsync (not mutate) because the form's autosave queue must await each
// save to know whether to roll the field back; onSettled invalidation keeps
// working regardless of which caller fired it.
const { mutateAsync: saveProvider, isLoading: editLoading } = useMutation({
  mutation: async (data: Record<string, unknown>) => {
    if (!curProviderId.value) {
      return materializeProvider(data, data.enable !== false)
    }
    const { data: result } = await putProvidersById({
      path: { id: curProviderId.value },
      body: data as ProvidersUpdateRequest,
      throwOnError: true,
    })
    return result
  },
  onSettled: invalidateProviderQueries,
})

const { importProviderModels } = useProviderModelCatalog()

async function materializeProvider(
  data: Record<string, unknown>,
  enable: boolean,
  importModels = true,
) {
  if (curProvider.value?.id) return curProvider.value
  if (materializePromise) return materializePromise

  const draft = curProvider.value
  if (!draft) throw new Error('provider is missing')

  materializePromise = (async () => {
    const { data: created } = await postProviders({
      body: {
        name: String(data.name ?? draft.name ?? ''),
        client_type: String(data.client_type ?? draft.client_type ?? ''),
        config: (data.config as Record<string, unknown> | undefined) ?? draft.config ?? {},
        // The draft's metadata records the gallery entry and source.
        metadata: { ...(draft.metadata ?? {}), ...((data.metadata as Record<string, unknown> | undefined) ?? {}) },
        ...(draft.icon && { icon: draft.icon }),
      },
      throwOnError: true,
    })
    if (!created?.id) throw new Error('provider creation returned no id')

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

    if (importModels) {
      try {
        await importProviderModels(result)
      } catch {
        toast.error(t('models.importFailed'))
      }
    }

    invalidateProviderQueries()
    return result
  })()

  try {
    return await materializePromise
  } finally {
    materializePromise = null
  }
}

async function ensureOAuthProvider(): Promise<ProvidersGetResponse> {
  const provider = curProvider.value
  if (!provider) throw new Error('provider is missing')
  if (provider.id) return provider

  return materializeProvider({
    name: provider.name,
    config: provider.config ?? {},
    metadata: provider.metadata ?? {},
  }, provider.enable !== false, false)
}

const providerForm = useTemplateRef('providerForm')

// Adding a model to a draft creates the provider first, from the
// form's current (possibly unsaved) values. Models are not imported here:
// without an API key the endpoint cannot be listed.
async function ensureProviderId(): Promise<string> {
  const provider = curProvider.value
  if (!provider) throw new Error('provider is missing')
  if (provider.id) return provider.id

  // A source's catalog needs no API key, so its models come along; listing an
  // endpoint's models has to wait for the key.
  const created = await materializeProvider(
    providerForm.value?.draftPayload() ?? {
      name: provider.name,
      config: provider.config ?? {},
      metadata: provider.metadata ?? {},
    },
    provider.enable !== false,
    !!providerSource(provider),
  )
  if (!created.id) throw new Error('provider creation returned no id')
  return created.id
}

async function handleToggleEnable(value: boolean) {
  if (!curProvider.value) return

  const prev = curProvider.value.enable ?? true
  curProvider.value = {
    ...curProvider.value,
    enable: value,
  }

  // 草稿的启用状态与表单字段同属一次手动保存，不能在这里提前物化；
  // 否则会使用父组件快照创建 provider，覆盖表单里尚未保存的 key/URL。
  if (!curProviderId.value) return

  enableLoading.value = true
  try {
    await putProvidersById({
      path: { id: curProviderId.value },
      body: { enable: value },
      throwOnError: true,
    })
    invalidateProviderQueries()
  } catch {
    curProvider.value = {
      ...curProvider.value,
      enable: prev,
    }
    toast.error(t('common.saveFailed'))
  } finally {
    enableLoading.value = false
  }
}

const { mutate: deleteModel, isLoading: deleteModelLoading } = useMutation({
  mutation: async (modelID: string) => {
    if (!modelID) return
    await deleteModelsById({ path: { id: modelID }, throwOnError: true })
  },
  onSettled: invalidateModelQueries,
})

const { data: modelDataList } = useQuery({
  key: () => ['provider-models', curProviderId.value ?? ''],
  query: async () => {
    if (!curProviderId.value) return []
    const { data } = await getProvidersByIdModels({
      path: { id: curProviderId.value },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!curProviderId.value,
})

// A draft from a models.dev source previews the source's models, read-only;
// creating the provider imports them.
const isSourcePreview = computed(() => !curProviderId.value && !!providerSource(curProvider.value))
const { data: catalog, isLoading: catalogLoading } = useModelsDevCatalog(() => isSourcePreview.value)
const previewModels = computed<ModelsGetResponse[]>(() =>
  catalogModelsFor(catalog.value, providerSource(curProvider.value), curProvider.value?.client_type ?? '')
    .map(model => ({
      model_id: model.model_id,
      name: model.name,
      type: model.type,
      enable: false,
      config: model.config,
    })))

const providerModels = computed<ModelsGetResponse[]>(() =>
  isSourcePreview.value ? previewModels.value : (modelDataList.value ?? []))

watch(curProvider, () => {
  queryCache.invalidateQueries({ key: ['provider-models'] })
}, { immediate: true })
</script>
