<template>
  <SettingsShell width="narrow">
    <div class="space-y-4 sm:space-y-6">
      <!-- Identity card mirrors the search provider detail, while Native keeps
           its managed/non-destructive behavior. -->
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
            v-if="!isNative && curProvider?.id"
            :message="$t('webSearch.deleteFetchConfirm')"
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
            :model-value="curProvider?.enable ?? false"
            :disabled="isNative || enableLoading"
            :aria-label="$t('common.enable')"
            @update:model-value="handleToggleEnable"
          />
        </template>
      </ProviderIdentityCard>

      <SettingsSection
        :title="$t('provider.configurationTitle')"
        class="provider-configuration"
      >
        <div
          v-if="isNative"
          class="py-3 text-xs text-muted-foreground"
        >
          {{ $t('webSearch.nativeManaged') }}
        </div>

        <form
          v-else
          id="fetch-provider-form"
          @submit="editProvider"
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
                <FieldStack
                  class="w-full sm:w-80"
                  for="fetch-provider-name"
                >
                  <FormControl>
                    <Input
                      id="fetch-provider-name"
                      type="text"
                      :placeholder="$t('common.namePlaceholder')"
                      :aria-label="$t('common.name')"
                      v-bind="componentField"
                    />
                  </FormControl>
                </FieldStack>
              </SettingsRow>
            </FormField>

            <SchemaField
              v-for="field in configFields"
              :key="`${formEpoch}:${field.key}`"
              :field="field"
              :field-key="field.key"
              id-prefix="fetch-provider"
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
        </form>

        <template
          v-if="!isNative"
          #footer
        >
          <LoadingButton
            type="submit"
            form="fetch-provider-form"
            size="sm"
            :loading="editLoading"
          >
            {{ $t('provider.saveChanges') }}
          </LoadingButton>
        </template>
      </SettingsSection>
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
import { deleteFetchProvidersById, getFetchProvidersMeta, postFetchProviders, putFetchProvidersById } from '@memohai/sdk'
import type {
  FetchprovidersCreateRequest,
  FetchprovidersGetResponse,
  FetchprovidersProviderMeta,
  FetchprovidersUpdateRequest,
} from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import { ConfirmPopover, FieldStack, SettingsRow, SettingsSection, SettingsShell, toast } from '@felinic/ui'
import { normalizeProviderConfigFields, providerConfigDefaults } from '@/utils/provider-template'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useServerSyncedRecord, useServerSyncedScalar } from '@/composables/use-server-synced-form'

const { t } = useI18n()
const curProvider = inject('curFetchProvider', ref<FetchprovidersGetResponse>())
const emit = defineEmits<{
  materialized: [provider: FetchprovidersGetResponse]
}>()
const curProviderId = computed(() => curProvider.value?.id)
const isNative = computed(() => (curProvider.value?.provider || form.values.provider) === 'native')
const enableLoading = ref(false)

const queryCache = useQueryCache()
let materializePromise: Promise<FetchprovidersGetResponse> | null = null

const { data: metaData } = useQuery({
  key: () => ['fetch-providers-meta'],
  query: async () => {
    const { data } = await getFetchProvidersMeta({ throwOnError: true })
    return data
  },
})

const providerSchema = toTypedSchema(z.object({
  name: z.string().min(1, t('webSearch.nameRequired')),
  provider: z.string().min(1),
}))

const form = useForm({
  validationSchema: providerSchema,
})

const activeProvider = computed(() => curProvider.value?.provider || form.values.provider || '')

const currentMeta = computed(() =>
  (metaData.value as FetchprovidersProviderMeta[] | undefined)
    ?.find(item => item.provider === activeProvider.value) ?? null,
)

const configFields = computed(() => normalizeProviderConfigFields(currentMeta.value?.config_schema))

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
function providerIdentity(provider: FetchprovidersGetResponse | undefined): string {
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
  if (!curProvider.value || isNative.value) return

  const targetId = curProviderId.value
  if (!targetId && value) {
    const missing = findMissingRequiredField()
    if (missing) {
      toast.error(t('provider.requiredField', { field: missing.title }))
      return
    }
  }

  const targetIdentity = providerIdentity(curProvider.value)
  let materialized: FetchprovidersGetResponse | undefined
  let prev = curProvider.value.enable ?? false
  curProvider.value = { ...curProvider.value, enable: value }

  enableLoading.value = true
  try {
    if (!targetId) {
      const created = await materializeProvider({
        name: form.values.name,
        provider: (activeProvider.value || form.values.provider) as FetchprovidersCreateRequest['provider'],
        config: cloneConfigRecord(configData),
      })
      materialized = created
      prev = created.enable ?? false
      if (prev !== value && created.id) {
        const response = await putFetchProvidersById({
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
      toast.success(t('webSearch.saveFetchSuccess'))
      return
    }
    await putFetchProvidersById({
      path: { id: targetId },
      body: { enable: value },
      throwOnError: true,
    })
    toast.success(t('webSearch.saveFetchSuccess'))
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
      queryCache.invalidateQueries({ key: ['fetch-providers'] })
    }
  }
}

const { mutate: submitUpdate, isLoading: editLoading } = useMutation({
  mutation: async (data: FetchprovidersUpdateRequest) => {
    const targetIdentity = providerIdentity(curProvider.value)
    const targetId = curProviderId.value
    if (!targetId) {
      return materializeProvider(data as FetchprovidersCreateRequest)
    }
    const { data: result } = await putFetchProvidersById({
      path: { id: targetId },
      body: data,
      throwOnError: true,
    })
    if (providerIdentity(curProvider.value) === targetIdentity) {
      markNameClean()
      markConfigClean()
    }
    return result
  },
  onSuccess: () => {
    toast.success(t('webSearch.saveFetchSuccess'))
  },
  onError: (error) => {
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  },
  onSettled: () => queryCache.invalidateQueries({ key: ['fetch-providers'] }),
})

async function materializeProvider(data: FetchprovidersCreateRequest) {
  if (curProvider.value?.id) return curProvider.value
  if (materializePromise) return materializePromise

  const targetProvider = data.provider ?? curProvider.value?.provider as FetchprovidersCreateRequest['provider']
  const fallbackName = curProvider.value?.name || ''
  materializePromise = (async () => {
    const { data: created } = await postFetchProviders({
      body: {
        name: data.name?.trim() || fallbackName,
        provider: targetProvider,
        config: data.config ?? cloneConfigRecord(configData),
      },
      throwOnError: true,
    })
    if (!created?.id) throw new Error('fetch provider creation returned no id')

    if (curProvider.value?.provider === created.provider && !curProvider.value?.id) {
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
    if (!curProviderId.value || isNative.value) return
    await deleteFetchProvidersById({ path: { id: curProviderId.value }, throwOnError: true })
  },
  onSuccess: () => {
    toast.success(t('webSearch.deleteFetchSuccess'))
  },
  onError: (error) => {
    toast.error(resolveApiErrorMessage(error, t('webSearch.deleteFetchFailed')))
  },
  onSettled: () => queryCache.invalidateQueries({ key: ['fetch-providers'] }),
})

const editProvider = form.handleSubmit(async (values) => {
  const missing = findMissingRequiredField()
  if (missing) {
    toast.error(t('provider.requiredField', { field: missing.title }))
    return
  }
  submitUpdate({
    name: values.name,
    provider: values.provider as FetchprovidersUpdateRequest['provider'],
    config: cloneConfigRecord(configData),
  })
})
</script>
