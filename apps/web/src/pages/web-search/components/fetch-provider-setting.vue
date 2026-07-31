<template>
  <SettingsShell width="narrow">
    <div class="space-y-6">
      <!-- Identity card mirrors the search provider detail, while Native keeps
           its managed/non-destructive behavior. -->
      <section class="flex items-center gap-3 rounded-[var(--radius-menu-shell)] border border-border bg-card px-4 py-3">
        <span class="flex size-9 shrink-0 items-center justify-center">
          <SearchProviderLogo
            :provider="curProvider?.provider || ''"
            size="md"
          />
        </span>
        <div class="min-w-0 flex-1">
          <h2 class="truncate text-sm font-semibold">
            {{ curProvider?.name }}
          </h2>
        </div>
        <div class="ml-auto flex items-center gap-2">
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
        </div>
      </section>

      <SettingsSection :title="$t('provider.configurationTitle')">
        <div
          v-if="isNative"
          class="px-4 py-3 text-xs text-muted-foreground"
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
              <SettingsRow :label="$t('common.name')">
                <FieldStack
                  class="w-80"
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

            <template v-if="form.values.provider === 'jina'">
              <JinaReaderSettings v-model="configProxy" />
            </template>
            <template v-else-if="form.values.provider === 'cloudflare_markdown'">
              <CloudflareMarkdownSettings v-model="configProxy" />
            </template>
            <div
              v-else-if="form.values.provider"
              class="px-4 py-3 text-xs text-muted-foreground"
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
import {
  Input,
  Button,
  FormControl,
  FormField,
  Switch,
} from '@felinic/ui'
import LoadingButton from '@/components/loading-button/index.vue'
import JinaReaderSettings from './jina-reader-settings.vue'
import CloudflareMarkdownSettings from './cloudflare-markdown-settings.vue'
import { Trash2 } from 'lucide-vue-next'
import SearchProviderLogo from '@/components/search-provider-logo/index.vue'
import { computed, inject, ref, watch } from 'vue'
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
import { normalizeProviderConfigFields } from '@/utils/provider-template'

const { t } = useI18n()
const curProvider = inject('curFetchProvider', ref<FetchprovidersGetResponse>())
const emit = defineEmits<{
  materialized: [provider: FetchprovidersGetResponse]
}>()
const curProviderId = computed(() => curProvider.value?.id)
const isNative = computed(() => curProvider.value?.provider === 'native')
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

const configFields = computed(() => {
  const meta = (metaData.value as FetchprovidersProviderMeta[] | undefined)
    ?.find(item => item.provider === curProvider.value?.provider)
  return normalizeProviderConfigFields(meta?.config_schema)
})

const providerSchema = toTypedSchema(z.object({
  name: z.string().min(1, t('webSearch.nameRequired')),
  provider: z.string().min(1),
}))

const form = useForm({
  validationSchema: providerSchema,
})

const configData = ref<Record<string, unknown>>({})

const configProxy = computed({
  get: () => configData.value,
  set: (val: Record<string, unknown>) => {
    configData.value = val
  },
})

watch(curProvider, (newVal) => {
  if (newVal) {
    form.setValues({
      name: newVal.name ?? '',
      provider: newVal.provider ?? '',
    })
    configData.value = { ...(newVal.config ?? {}) }
  }
}, { immediate: true })

async function handleToggleEnable(value: boolean) {
  if (!curProvider.value || isNative.value) return

  const prev = curProvider.value.enable ?? false
  curProvider.value = { ...curProvider.value, enable: value }

  enableLoading.value = true
  try {
    if (!curProviderId.value) {
      await materializeProvider({
        name: form.values.name,
        provider: form.values.provider as FetchprovidersCreateRequest['provider'],
        config: configData.value,
      }, value)
      return
    }
    await putFetchProvidersById({
      path: { id: curProviderId.value },
      body: { enable: value },
      throwOnError: true,
    })
    queryCache.invalidateQueries({ key: ['fetch-providers'] })
  } catch {
    curProvider.value = { ...curProvider.value, enable: prev }
    toast.error(t('common.saveFailed'))
  } finally {
    enableLoading.value = false
  }
}

const { mutate: submitUpdate, isLoading: editLoading } = useMutation({
  mutation: async (data: FetchprovidersUpdateRequest) => {
    if (!curProviderId.value) {
      return materializeProvider(data as FetchprovidersCreateRequest)
    }
    const { data: result } = await putFetchProvidersById({
      path: { id: curProviderId.value },
      body: data,
      throwOnError: true,
    })
    return result
  },
  onSettled: () => queryCache.invalidateQueries({ key: ['fetch-providers'] }),
})

async function materializeProvider(data: FetchprovidersCreateRequest, enable = false) {
  if (curProvider.value?.id) return curProvider.value
  if (materializePromise) return materializePromise

  materializePromise = (async () => {
    const { data: created } = await postFetchProviders({
      body: {
        name: data.name?.trim() || curProvider.value?.name || '',
        provider: data.provider ?? curProvider.value?.provider as FetchprovidersCreateRequest['provider'],
        config: data.config ?? configData.value,
      },
      throwOnError: true,
    })
    if (!created?.id) throw new Error('fetch provider creation returned no id')

    let result = created
    if (enable) {
      const response = await putFetchProvidersById({
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
    if (!curProviderId.value || isNative.value) return
    await deleteFetchProvidersById({ path: { id: curProviderId.value }, throwOnError: true })
  },
  onSettled: () => queryCache.invalidateQueries({ key: ['fetch-providers'] }),
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
    provider: values.provider as FetchprovidersUpdateRequest['provider'],
    config: configData.value,
  })
})
</script>
