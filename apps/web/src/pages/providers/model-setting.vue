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
            {{ avatarInitials(curProvider?.name, '?') }}
          </span>
        </span>
        <div class="min-w-0 flex-1">
          <h4 class="scroll-m-20 tracking-tight truncate">
            {{ curProvider?.name }}
          </h4>
        </div>
        <div class="ml-auto flex items-center gap-2">
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
        </div>
      </section>

      <ProviderForm
        :provider="curProvider"
        :edit-loading="editLoading"
        :ensure-provider="ensureOAuthProvider"
        @submit="changeProvider"
      />

      <ModelList
        :provider-id="curProvider?.id"
        :models="providerModels"
        :managed="isManagedModelCatalogClientType(curProvider?.client_type)"
        :preview="!curProvider?.id"
        :delete-model-loading="deleteModelLoading"
        @edit="handleEditModel"
        @delete="deleteModel"
      />
    </div>
  </SettingsShell>
</template>

<script setup lang="ts">
import { Button, ConfirmPopover, SettingsShell, Switch } from '@felinic/ui'
import { Trash2 } from 'lucide-vue-next'
import ProviderIcon from '@/components/provider-icon/index.vue'
import { avatarInitials } from '@/composables/useAvatarInitials'

import ProviderForm from './components/provider-form.vue'
import ModelList from './components/model-list.vue'
import { computed, provide, reactive, ref, toRef, watch } from 'vue'
import { useQuery, useMutation, useQueryCache } from '@pinia/colada'
import {
  deleteModelsById,
  deleteProvidersById,
  getProvidersByIdModels,
  postProvidersByIdImportModels,
  postProvidersFromTemplate,
  putProvidersById,
} from '@memohai/sdk'
import type { ModelsGetResponse, ProvidersGetResponse, ProvidersUpdateRequest } from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import { toast } from '@felinic/ui'
import { isManagedModelCatalogClientType } from '@/constants/client-types'
import { useProviderTemplateModels } from '@/composables/useProviderTemplateModels'

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
const curProviderTemplateId = computed(() => curProviderId.value
  ? undefined
  : curProvider.value?.provider_template_id)
const { models: templateModels } = useProviderTemplateModels(curProviderTemplateId)
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

const { mutate: changeProvider, isLoading: editLoading } = useMutation({
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
  onSettled: () => {
    invalidateProviderQueries()
    queryCache.invalidateQueries({ key: ['provider-templates', 'llm'] })
  },
})

async function materializeProvider(
  data: Record<string, unknown>,
  enable: boolean,
  importModels = true,
) {
  if (curProvider.value?.id) return curProvider.value
  if (materializePromise) return materializePromise

  const templateId = curProvider.value?.provider_template_id
  if (!templateId) throw new Error('provider template is missing')

  materializePromise = (async () => {
    const { data: created } = await postProvidersFromTemplate({
      body: {
        template_id: templateId,
        domain: 'llm',
        name: String(data.name ?? curProvider.value?.name ?? ''),
        config: (data.config as Record<string, unknown> | undefined) ?? {},
        metadata: (data.metadata as Record<string, unknown> | undefined) ?? {},
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
        await postProvidersByIdImportModels({
          path: { id: created.id },
          throwOnError: true,
        })
      } catch {
        toast.error(t('models.importFailed'))
      }
    }

    invalidateProviderQueries()
    queryCache.invalidateQueries({ key: ['provider-templates', 'llm'] })
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

async function handleToggleEnable(value: boolean) {
  if (!curProvider.value) return

  const prev = curProvider.value.enable ?? true
  curProvider.value = {
    ...curProvider.value,
    enable: value,
  }

  enableLoading.value = true
  try {
    if (!curProviderId.value) {
      await materializeProvider({
        name: curProvider.value.name,
        config: curProvider.value.config ?? {},
        metadata: curProvider.value.metadata ?? {},
      }, value)
      return
    }
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

const providerModels = computed<ModelsGetResponse[]>(() => {
  if (curProviderId.value) return modelDataList.value ?? []
  return templateModels.value.map(model => ({
    model_id: model.model_id,
    name: model.name,
    type: model.type as ModelsGetResponse['type'],
    config: model.config as ModelsGetResponse['config'],
    enable: true,
  }))
})

watch(curProvider, () => {
  queryCache.invalidateQueries({ key: ['provider-models'] })
}, { immediate: true })
</script>
