<template>
  <SettingsShell width="narrow">
    <div class="space-y-4 sm:space-y-6">
      <ProviderIdentityCard
        :name="curProvider?.name"
      >
        <template #media>
          <ProviderIcon
            v-if="providerLogo(curProvider)"
            :icon="providerLogo(curProvider)!"
            :monochrome="isMonochromeLogo(providerLogo(curProvider))"
            size="1.5em"
          >
            <span class="text-xs font-medium text-muted-foreground">
              {{ avatarInitials(curProvider?.name, '?') }}
            </span>
          </ProviderIcon>
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
        :preview-error="isSourcePreview && !catalogLoading && !!catalogError"
        :ensure-provider-id="curProvider?.id ? undefined : ensureProviderId"
        :delete-model-loading="deleteModelLoading"
        @edit="handleEditModel"
        @delete="deleteModel"
        @created="noteModelAdded"
        @add-closed="importCatalogAfterAdd"
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
import { isMonochromeLogo, providerLogo, providerSource } from '@/constants/model-sources'
import { useModelsDevCatalog } from '@/composables/useModelsDevCatalog'
import { useProviderModelCatalog } from '@/composables/useProviderModelCatalog'

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

    // The provider exists from here on: holding it before the disable below
    // means a failed disable leaves no draft behind to be created twice.
    let result = created
    curProvider.value = result
    emit('materialized', result)
    if (!enable) {
      // Create takes no enable flag, so a disabled draft is created and then
      // switched off. If that fails the provider stays on, and the user is told.
      try {
        const response = await putProvidersById({
          path: { id: created.id },
          body: { enable: false },
          throwOnError: true,
        })
        result = response.data ?? { ...created, enable: false }
        curProvider.value = result
      } catch {
        toast.error(t('common.saveFailed'))
      }
    }

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
// form's current (possibly unsaved) values. A source's catalog needs no API
// key, so it comes along once the add dialog closes, whether the model was
// added or not. Models the user added there are left out of that import: they
// already exist, and the catalog would overwrite what the user just set. An
// endpoint is listed later, once the key is saved.
// The pending import is recorded when the create starts, so a dialog closed
// before the create settles still imports once it does. A create the switch
// already started imports the catalog itself, so joining it records nothing.
let catalogPendingAdd: { ready: Promise<ProvidersGetResponse | null>, added: string[] } | null = null

async function ensureProviderId(): Promise<string> {
  const provider = curProvider.value
  if (!provider) throw new Error('provider is missing')
  if (provider.id) return provider.id

  const joining = materializePromise !== null
  const creating = materializeProvider(
    providerForm.value?.draftPayload() ?? {
      name: provider.name,
      config: provider.config ?? {},
      metadata: provider.metadata ?? {},
    },
    provider.enable !== false,
    false,
  )
  if (!joining && providerSource(provider)) {
    catalogPendingAdd = { ready: creating.catch(() => null), added: [] }
  }
  const created = await creating
  if (!created.id) throw new Error('provider creation returned no id')
  return created.id
}

function noteModelAdded(modelId: string) {
  catalogPendingAdd?.added.push(modelId)
}

async function importCatalogAfterAdd() {
  const pending = catalogPendingAdd
  catalogPendingAdd = null
  const provider = await pending?.ready
  if (!pending || !provider?.id) return
  try {
    await importProviderModels(provider, { skipModelIds: pending.added })
  } catch {
    toast.error(t('models.importFailed'))
  }
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
      // Toggling a draft saves it. The provider is created from the form's
      // current values, not the parent snapshot, so an unsaved key or URL is
      // kept. A models.dev source imports its catalog without a key; an
      // endpoint is listed only when the form has what listing needs.
      const form = providerForm.value
      await materializeProvider(
        form?.draftPayload() ?? {
          name: curProvider.value.name,
          config: curProvider.value.config ?? {},
          metadata: curProvider.value.metadata ?? {},
        },
        value,
        !!providerSource(curProvider.value) || (form?.canListModels() ?? false),
      )
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

// A draft from a models.dev source previews the source's models, read-only;
// creating the provider imports them.
const isSourcePreview = computed(() => !curProviderId.value && !!providerSource(curProvider.value))
const { data: catalog, isLoading: catalogLoading, error: catalogError } = useModelsDevCatalog(
  () => providerSource(curProvider.value),
  () => curProvider.value?.client_type ?? '',
  () => isSourcePreview.value,
)
const previewModels = computed<ModelsGetResponse[]>(() =>
  (catalog.value?.models ?? [])
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
