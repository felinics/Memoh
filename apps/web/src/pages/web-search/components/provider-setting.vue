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
            :model-value="curProvider?.enable ?? true"
            :disabled="enableLoading"
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
              :key="field.key"
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
import { computed, inject, ref, watch } from 'vue'
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

const currentMeta = computed(() =>
  (metaData.value as SearchprovidersProviderMeta[] | undefined)
    ?.find(item => item.provider === curProvider.value?.provider) ?? null,
)

const configFields = computed(() => normalizeProviderConfigFields(currentMeta.value?.config_schema))

// ---- form ----
const providerSchema = toTypedSchema(z.object({
  name: z.string().min(1, t('webSearch.nameRequired')),
  provider: z.string().min(1),
}))

const form = useForm({
  validationSchema: providerSchema,
})

// Store config separately since it varies by provider type
const configData = ref<Record<string, unknown>>({})
let hydratedIdentity = ''

function providerIdentity(provider: SearchprovidersGetResponse | undefined): string {
  if (!provider) return ''
  return provider.id ? `id:${provider.id}` : `draft:${provider.provider ?? ''}`
}

function setConfigField(key: string, value: unknown) {
  if (value === undefined) {
    const next = { ...configData.value }
    delete next[key]
    configData.value = next
    return
  }
  configData.value = { ...configData.value, [key]: value }
}

watch([curProvider, currentMeta], ([newVal, meta]) => {
  if (!newVal) return
  const nextIdentity = providerIdentity(newVal)
  const defaults = providerConfigDefaults(meta?.config_schema)
  if (nextIdentity !== hydratedIdentity) {
    hydratedIdentity = nextIdentity
    form.setValues({
      name: newVal.name ?? '',
      provider: newVal.provider ?? '',
    })
    configData.value = { ...defaults, ...(newVal.config ?? {}) }
    return
  }
  // If schema metadata arrives after initial mount, backfill unset optional defaults
  // without clobbering any field already present in the draft.
  for (const [key, fallback] of Object.entries(defaults)) {
    if (configData.value[key] === undefined && newVal.config?.[key] === undefined) {
      configData.value = { ...configData.value, [key]: fallback }
    }
  }
}, { immediate: true })

async function handleToggleEnable(value: boolean) {
  if (!curProvider.value) return

  const prev = curProvider.value.enable ?? true
  curProvider.value = { ...curProvider.value, enable: value }

  enableLoading.value = true
  try {
    if (!curProviderId.value) {
      await materializeProvider({
        name: form.values.name,
        provider: form.values.provider as SearchprovidersCreateRequest['provider'],
        config: configData.value,
      }, value)
      toast.success(t('webSearch.saveSuccess'))
      return
    }
    await putSearchProvidersById({
      path: { id: curProviderId.value },
      body: { enable: value },
      throwOnError: true,
    })
    queryCache.invalidateQueries({ key: ['search-providers'] })
    toast.success(t('webSearch.saveSuccess'))
  } catch (error) {
    curProvider.value = { ...curProvider.value, enable: prev }
    toast.error(resolveApiErrorMessage(error, t('common.saveFailed')))
  } finally {
    enableLoading.value = false
  }
}

// ---- mutations ----
const { mutate: submitUpdate, isLoading: editLoading } = useMutation({
  mutation: async (data: SearchprovidersUpdateRequest) => {
    if (!curProviderId.value) {
      return materializeProvider(data as SearchprovidersCreateRequest)
    }
    const { data: result } = await putSearchProvidersById({
      path: { id: curProviderId.value },
      body: data,
      throwOnError: true,
    })
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

async function materializeProvider(data: SearchprovidersCreateRequest, enable = false) {
  if (curProvider.value?.id) return curProvider.value
  if (materializePromise) return materializePromise

  materializePromise = (async () => {
    const { data: created } = await postSearchProviders({
      body: {
        name: data.name?.trim() || curProvider.value?.name || '',
        provider: data.provider ?? curProvider.value?.provider as SearchprovidersCreateRequest['provider'],
        config: data.config ?? configData.value,
      },
      throwOnError: true,
    })
    if (!created?.id) throw new Error('search provider creation returned no id')

    let result = created
    if (enable) {
      const response = await putSearchProvidersById({
        path: { id: created.id },
        body: { enable: true },
        throwOnError: true,
      })
      result = response.data ?? { ...created, enable: true }
    }

    curProvider.value = result
    emit('materialized', result)
    return result
  })()

  try {
    return await materializePromise
  } finally {
    materializePromise = null
  }
}

const { mutate: deleteProvider, isLoading: deleteLoading } = useMutation({
  mutation: async () => {
    if (!curProviderId.value) return
    await deleteSearchProvidersById({ path: { id: curProviderId.value }, throwOnError: true })
  },
  onSuccess: () => {
    toast.success(t('webSearch.deleteSuccess'))
  },
  onError: (error) => {
    toast.error(resolveApiErrorMessage(error, t('webSearch.deleteFailed')))
  },
  onSettled: () => queryCache.invalidateQueries({ key: ['search-providers'] }),
})

const editProvider = form.handleSubmit(async (values) => {
  const missing = configFields.value.find((field) => {
    if (!field.required) return false
    const value = configData.value[field.key]
    if (field.type === 'bool' || field.type === 'boolean') return value === undefined || value === null
    return !String(value ?? '').trim()
  })
  if (missing) {
    toast.error(t('provider.requiredField', { field: missing.title }))
    return
  }
  submitUpdate({
    name: values.name,
    provider: values.provider as SearchprovidersUpdateRequest['provider'],
    config: configData.value,
  })
})
</script>
