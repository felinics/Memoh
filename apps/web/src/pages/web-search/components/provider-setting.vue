<template>
  <SettingsShell width="narrow">
    <div class="space-y-4 sm:space-y-6">
      <!-- Identity card: logo + name on the left, delete + enable on the right —
           the same header shape as the provider detail, so every backend reads
           the same way. -->
      <ProviderIdentityCard
        :name="curProvider?.name"
        media="bare"
      >
        <template #media>
          <SearchProviderLogo
            :provider="curProvider?.provider || ''"
            size="md"
          />
        </template>
        <template #actions>
          <ConfirmPopover
            v-if="curProvider?.id"
            :message="$t('webSearch.deleteConfirm')"
            :cancel-text="$t('common.cancel')"
            :confirm-text="$t('common.confirm')"
            :loading="deleteLoading"
            variant="destructive"
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
            :model-value="curProvider?.id ? (curProvider.enable ?? true) : (curProvider?.enable ?? false)"
            :disabled="enableLoading || editLoading"
            :aria-label="$t('common.enable')"
            @update:model-value="handleToggleEnable"
          />
        </template>
      </ProviderIdentityCard>

      <form @submit="editProvider">
        <SettingsSection
          :title="$t('provider.configurationTitle')"
          class="provider-configuration"
        >
          <div>
            <FormField
              v-slot="{ componentField }"
              name="name"
            >
              <SettingsRow
                :label="$t('common.name')"
                stack="sm"
              >
                <FieldStack class="w-full sm:w-80">
                  <FormControl>
                    <Input
                      type="text"
                      :placeholder="$t('common.namePlaceholder')"
                      :aria-label="$t('common.name')"
                      v-bind="componentField"
                    />
                  </FormControl>
                </FieldStack>
              </SettingsRow>
            </FormField>

            <!-- Backend-specific fields declared by /search-providers/meta. -->
            <SchemaField
              v-for="field in configFields"
              :key="`${formEpoch}:${field.key}`"
              :field="field"
              :field-key="field.key"
              id-prefix="search-provider"
              :model-value="configData[field.key]"
              @update:model-value="(val) => setConfigField(field.key, val)"
            />
            <div
              v-if="metaData && !currentMeta && form.values.provider"
              class="py-3 text-xs text-muted-foreground"
            >
              {{ $t('webSearch.unsupportedProvider') }}
            </div>
          </div>

          <template #footer>
            <LoadingButton
              type="submit"
              size="sm"
              :loading="editLoading"
              :disabled="enableLoading"
            >
              {{ $t('provider.saveChanges') }}
            </LoadingButton>
          </template>
        </SettingsSection>
      </form>
    </div>
  </SettingsShell>
</template>

<script setup lang="ts">
import ProviderIdentityCard from '@/components/provider-identity-card/index.vue'
import SchemaField from '@/components/config-schema-form/schema-field.vue'
import {
  Input,
  Button,
  FormControl,
  FormField,
  Switch,
} from '@felinic/ui'
import LoadingButton from '@/components/loading-button/index.vue'
import { Trash2 } from 'lucide-vue-next'
import SearchProviderLogo from '@/components/search-provider-logo/index.vue'
import { computed, inject, reactive, ref, watch } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import z from 'zod'
import { useForm } from 'vee-validate'
import { useMutation, useQuery, useQueryCache } from '@pinia/colada'
import { deleteSearchProvidersById, getSearchProvidersMeta, postSearchProviders, putSearchProvidersById } from '@memohai/sdk'
import type {
  SearchprovidersCreateRequest,
  SearchprovidersGetResponse,
  SearchprovidersProviderMeta,
  SearchprovidersUpdateRequest,
} from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import { ConfirmPopover, FieldStack, SettingsRow, SettingsSection, SettingsShell, toast } from '@felinic/ui'
import { normalizeProviderConfigFields, providerConfigDefaults } from '@/utils/provider-template'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useServerSyncedRecord, useServerSyncedScalar } from '@/composables/use-server-synced-form'

const { t } = useI18n()
const curProvider = inject('curSearchProvider', ref<SearchprovidersGetResponse>())
const emit = defineEmits<{
  materialized: [provider: SearchprovidersGetResponse]
}>()
const curProviderId = computed(() => curProvider.value?.id)
const enableLoading = ref(false)

const queryCache = useQueryCache()
let materializePromise: Promise<SearchprovidersGetResponse> | null = null

const { data: metaData } = useQuery({
  key: () => ['search-providers-meta'],
  query: async () => {
    const { data } = await getSearchProvidersMeta({ throwOnError: true })
    return data
  },
})

// ---- form ----
const providerSchema = toTypedSchema(z.object({
  name: z.string().min(1, t('webSearch.nameRequired')),
  provider: z.string().min(1),
}))

const form = useForm({
  validationSchema: providerSchema,
})

const activeProvider = computed(() => curProvider.value?.provider || form.values.provider || '')

const currentMeta = computed(() =>
  (metaData.value as SearchprovidersProviderMeta[] | undefined)
    ?.find(item => item.provider === activeProvider.value) ?? null,
)

const configFields = computed(() => normalizeProviderConfigFields(currentMeta.value?.config_schema))

// Store config separately since it varies by provider type
const configData = reactive<Record<string, unknown>>({})

function cloneConfigRecord(raw: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [key, val] of Object.entries(raw)) {
    if (val && typeof val === 'object' && !Array.isArray(val)) {
      out[key] = { ...(val as Record<string, unknown>) }
    } else if (Array.isArray(val)) {
      out[key] = [...val]
    } else {
      out[key] = val
    }
  }
  return out
}

function setConfigField(key: string, value: unknown) {
  if (value === undefined) {
    delete configData[key]
    return
  }
  configData[key] = value
}

const providerName = computed({
  get: () => form.values.name ?? '',
  set: (value: string) => form.setFieldValue('name', value),
})

// The list query hands back a new provider object on every refetch (window
// focus, the enable switch, a save), and the page re-selects it. Distinguish
// unmaterialized placeholders (`draft:<type>`) from persisted records
// (`id:<uuid>`) so initial server hydration hard-resets the placeholder while
// same-record refreshes only update fields the user has not touched. During the
// leave transition (`undefined`), keep current values and identity intact.
let lastActiveIdentity = ''
function providerIdentity(provider: SearchprovidersGetResponse | undefined): string {
  if (!provider) return lastActiveIdentity
  lastActiveIdentity = provider.id ? `id:${provider.id}` : `draft:${provider.provider ?? ''}`
  return lastActiveIdentity
}

const formEpoch = ref(0)

const { markClean: markNameClean } = useServerSyncedScalar(providerName, {
  source: () => curProvider.value,
  identity: providerIdentity,
  server: provider => (provider ? provider.name ?? '' : providerName.value),
})

const { markClean: markConfigClean } = useServerSyncedRecord(configData, {
  source: () => (curProvider.value
    ? { provider: curProvider.value, schema: currentMeta.value?.config_schema }
    : undefined),
  identity: source => providerIdentity(source?.provider),
  server: source => (source
    ? cloneConfigRecord({
        ...providerConfigDefaults(source.schema),
        ...(source.provider.config ?? {}),
      })
    : cloneConfigRecord(configData)),
  onSync: (hardReset) => {
    if (hardReset && curProvider.value) formEpoch.value++
  },
})

// Not editable in this form, so it simply follows the selection.
watch(() => curProvider.value?.provider, (provider) => {
  if (provider) form.setFieldValue('provider', provider)
}, { immediate: true })

function findMissingRequiredField() {
  return configFields.value.find((field) => {
    if (!field.required) return false
    const value = configData[field.key]
    if (field.type === 'bool' || field.type === 'boolean') return value === undefined || value === null
    return !String(value ?? '').trim()
  })
}

async function handleToggleEnable(value: boolean) {
  if (!curProvider.value) return

  const targetId = curProviderId.value
  if (!targetId && value) {
    const missing = findMissingRequiredField()
    if (missing) {
      toast.error(t('provider.requiredField', { field: missing.title }))
      return
    }
  }

  const targetIdentity = providerIdentity(curProvider.value)
  let materialized: SearchprovidersGetResponse | undefined
  let prev = curProvider.value.id ? (curProvider.value.enable ?? true) : (curProvider.value.enable ?? false)
  curProvider.value = { ...curProvider.value, enable: value }

  enableLoading.value = true
  try {
    if (!targetId) {
      const created = await materializeProvider({
        name: form.values.name,
        provider: (activeProvider.value || form.values.provider) as SearchprovidersCreateRequest['provider'],
        config: cloneConfigRecord(configData),
      })
      materialized = created
      prev = created.enable ?? false
      if (prev !== value && created.id) {
        const response = await putSearchProvidersById({
          path: { id: created.id },
          body: { enable: value },
          throwOnError: true,
        })
        const updated = response.data ?? { ...created, enable: value }
        if (curProvider.value?.id === created.id) {
          curProvider.value = updated
        }
        emit('materialized', updated)
      }
      toast.success(t('webSearch.saveSuccess'))
      return
    }
    await putSearchProvidersById({
      path: { id: targetId },
      body: { enable: value },
      throwOnError: true,
    })
    toast.success(t('webSearch.saveSuccess'))
  } catch (error) {
    if (materialized?.id && curProvider.value?.id === materialized.id) {
      const restored = { ...materialized, enable: prev }
      curProvider.value = restored
      emit('materialized', restored)
    } else if (curProvider.value && providerIdentity(curProvider.value) === targetIdentity) {
      curProvider.value = { ...curProvider.value, enable: prev }
    }
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  } finally {
    enableLoading.value = false
    if (targetId || materialized?.id) {
      queryCache.invalidateQueries({ key: ['search-providers'] })
    }
  }
}

// ---- mutations ----
const { mutate: submitUpdate, isLoading: editLoading } = useMutation({
  mutation: async (data: SearchprovidersUpdateRequest) => {
    const targetIdentity = providerIdentity(curProvider.value)
    const targetId = curProviderId.value
    const submittedName = data.name ?? ''
    const submittedConfig = cloneConfigRecord({
      ...providerConfigDefaults(currentMeta.value?.config_schema),
      ...(data.config ?? {}),
    })
    if (!targetId) {
      const joinedInFlight = Boolean(materializePromise)
      const created = await materializeProvider(data as SearchprovidersCreateRequest)
      if (!joinedInFlight || !created.id) {
        return created
      }
      const { data: result } = await putSearchProvidersById({
        path: { id: created.id },
        body: data,
        throwOnError: true,
      })
      const updated = result ?? { ...created, ...data, id: created.id }
      if (curProvider.value?.id === created.id) {
        markNameClean(submittedName)
        markConfigClean(submittedConfig)
        curProvider.value = updated
      }
      emit('materialized', updated)
      return updated
    }
    const { data: result } = await putSearchProvidersById({
      path: { id: targetId },
      body: data,
      throwOnError: true,
    })
    if (providerIdentity(curProvider.value) === targetIdentity) {
      markNameClean(submittedName)
      markConfigClean(submittedConfig)
      if (result) {
        curProvider.value = result
      }
    }
    if (result) {
      emit('materialized', result)
    }
    return result
  },
  onSuccess: () => {
    toast.success(t('webSearch.saveSuccess'))
  },
  onError: (error) => {
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  },
  onSettled: () => queryCache.invalidateQueries({ key: ['search-providers'] }),
})

async function materializeProvider(data: SearchprovidersCreateRequest) {
  if (curProvider.value?.id) return curProvider.value
  if (materializePromise) return materializePromise

  const targetProvider = data.provider ?? curProvider.value?.provider as SearchprovidersCreateRequest['provider']
  const fallbackName = curProvider.value?.name || ''
  const submittedName = data.name?.trim() || fallbackName
  const submittedConfig = cloneConfigRecord(data.config ?? configData)
  const baselineConfig = cloneConfigRecord({
    ...providerConfigDefaults(currentMeta.value?.config_schema),
    ...submittedConfig,
  })
  materializePromise = (async () => {
    const { data: created } = await postSearchProviders({
      body: {
        name: submittedName,
        provider: targetProvider,
        config: submittedConfig,
      },
      throwOnError: true,
    })
    if (!created?.id) throw new Error('search provider creation returned no id')

    if (curProvider.value?.provider === created.provider && !curProvider.value?.id) {
      markNameClean(submittedName, `id:${created.id}`)
      markConfigClean(baselineConfig, `id:${created.id}`)
      curProvider.value = created
    }
    emit('materialized', created)
    return created
  })()

  try {
    return await materializePromise
  } finally {
    materializePromise = null
  }
}

const { mutate: deleteProvider, isLoading: deleteLoading } = useMutation({
  mutation: async () => {
    const targetId = curProviderId.value
    if (!targetId) return null
    const targetProvider = curProvider.value?.provider
    await deleteSearchProvidersById({ path: { id: targetId }, throwOnError: true })
    return { id: targetId, provider: targetProvider }
  },
  onSuccess: (deleted) => {
    if (!deleted) return
    const currentList = queryCache.getQueryData<SearchprovidersGetResponse[]>(['search-providers'])
    if (Array.isArray(currentList)) {
      queryCache.setQueryData(
        ['search-providers'],
        currentList.filter(item => item.id !== deleted.id),
      )
    }
    const meta = (metaData.value as SearchprovidersProviderMeta[] | undefined)
      ?.find(item => item.provider === deleted.provider)
    const resetEntry: SearchprovidersGetResponse = {
      name: meta?.display_name ?? (deleted.provider ? t(`webSearch.providerNames.${deleted.provider}`, deleted.provider) : ''),
      provider: deleted.provider,
      enable: false,
      config: providerConfigDefaults(meta?.config_schema),
    }
    if (curProvider.value?.id === deleted.id) {
      curProvider.value = resetEntry
    }
    emit('materialized', resetEntry)
    toast.success(t('webSearch.deleteSuccess'))
  },
  onError: (error) => {
    toast.error(resolveApiErrorMessage(error, t('webSearch.deleteFailed')))
  },
  onSettled: () => queryCache.invalidateQueries({ key: ['search-providers'] }),
})

const editProvider = form.handleSubmit(async (values) => {
  const missing = findMissingRequiredField()
  if (missing) {
    toast.error(t('provider.requiredField', { field: missing.title }))
    return
  }
  submitUpdate({
    name: values.name,
    provider: values.provider as SearchprovidersUpdateRequest['provider'],
    config: cloneConfigRecord(configData),
  })
})
</script>
