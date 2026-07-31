<template>
  <SettingsShell width="narrow">
    <div class="space-y-6">
      <!-- Identity card: logo + name on the left, delete + enable on the right —
           the same header shape as the provider detail, so every backend reads
           the same way. -->
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
        </div>
      </section>

      <form @submit="editProvider">
        <SettingsSection :title="$t('provider.configurationTitle')">
          <div>
            <FormField
              v-slot="{ componentField }"
              name="name"
            >
              <SettingsRow :label="$t('common.name')">
                <FieldStack class="w-80">
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

            <!-- Backend-specific fields, each rendered as a row in the same card. -->
            <template v-if="form.values.provider === 'brave'">
              <BraveSettings v-model="configProxy" />
            </template>
            <template v-else-if="form.values.provider === 'bing'">
              <BingSettings v-model="configProxy" />
            </template>
            <template v-else-if="form.values.provider === 'google'">
              <GoogleSettings v-model="configProxy" />
            </template>
            <template v-else-if="form.values.provider === 'tavily'">
              <TavilySettings v-model="configProxy" />
            </template>
            <template v-else-if="form.values.provider === 'sogou'">
              <SogouSettings v-model="configProxy" />
            </template>
            <template v-else-if="form.values.provider === 'serper'">
              <SerperSettings v-model="configProxy" />
            </template>
            <template v-else-if="form.values.provider === 'searxng'">
              <SearxngSettings v-model="configProxy" />
            </template>
            <template v-else-if="form.values.provider === 'jina'">
              <JinaSettings v-model="configProxy" />
            </template>
            <template v-else-if="form.values.provider === 'exa'">
              <ExaSettings v-model="configProxy" />
            </template>
            <template v-else-if="form.values.provider === 'bocha'">
              <BochaSettings v-model="configProxy" />
            </template>
            <template v-else-if="form.values.provider === 'duckduckgo'">
              <DuckduckgoSettings v-model="configProxy" />
            </template>
            <template v-else-if="form.values.provider === 'yandex'">
              <YandexSettings v-model="configProxy" />
            </template>
            <div
              v-else-if="form.values.provider"
              class="px-4 py-3 text-xs text-muted-foreground"
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
import {
  Input,
  Button,
  FormControl,
  FormField,
  Switch,
} from '@felinic/ui'
import LoadingButton from '@/components/loading-button/index.vue'
import BraveSettings from './brave-settings.vue'
import BingSettings from './bing-settings.vue'
import GoogleSettings from './google-settings.vue'
import TavilySettings from './tavily-settings.vue'
import SogouSettings from './sogou-settings.vue'
import SerperSettings from './serper-settings.vue'
import SearxngSettings from './searxng-settings.vue'
import JinaSettings from './jina-settings.vue'
import ExaSettings from './exa-settings.vue'
import BochaSettings from './bocha-settings.vue'
import DuckduckgoSettings from './duckduckgo-settings.vue'
import YandexSettings from './yandex-settings.vue'
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
import { normalizeProviderConfigFields } from '@/utils/provider-template'

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

const configFields = computed(() => {
  const meta = (metaData.value as SearchprovidersProviderMeta[] | undefined)
    ?.find(item => item.provider === curProvider.value?.provider)
  return normalizeProviderConfigFields(meta?.config_schema)
})

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
      return
    }
    await putSearchProvidersById({
      path: { id: curProviderId.value },
      body: { enable: value },
      throwOnError: true,
    })
    queryCache.invalidateQueries({ key: ['search-providers'] })
  } catch {
    curProvider.value = { ...curProvider.value, enable: prev }
    toast.error(t('common.saveFailed'))
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
