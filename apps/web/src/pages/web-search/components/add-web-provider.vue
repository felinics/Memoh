<template>
  <section>
    <FormDialogShell
      v-model:open="open"
      :title="$t('webSearch.add')"
      :cancel-text="$t('common.cancel')"
      :submit-text="$t('webSearch.add')"
      :submit-disabled="(form.meta.value.valid === false) || isLoading"
      :loading="isLoading"
      @submit="handleCreate"
    >
      <template #trigger>
        <span
          v-if="hideTrigger"
          class="hidden"
        />
        <Button
          v-else
          class="w-full shadow-none! text-muted-foreground h-9 px-3 rounded-md border-border bg-background hover:bg-accent"
          variant="outline"
        >
          <Plus class="mr-1 size-4" /> {{ $t('webSearch.add') }}
        </Button>
      </template>
      <template #body>
        <div class="flex-col gap-3 flex mt-4">
          <FormField
            v-slot="{ componentField }"
            name="name"
          >
            <FieldStack
              :label="$t('common.name')"
              for="web-provider-create-name"
            >
              <FormControl>
                <Input
                  id="web-provider-create-name"
                  type="text"
                  :placeholder="$t('common.namePlaceholder')"
                  v-bind="componentField"
                  :aria-label="$t('common.name')"
                />
              </FormControl>
            </FieldStack>
          </FormField>
          <FormField
            v-slot="{ componentField }"
            name="target"
          >
            <FieldStack
              :label="$t('webSearch.provider')"
              for="web-provider-create-type"
            >
              <FormControl>
                <Select v-bind="componentField">
                  <SelectTrigger
                    id="web-provider-create-type"
                    class="w-full"
                    :aria-label="$t('webSearch.provider')"
                  >
                    <SelectValue :placeholder="$t('common.typePlaceholder')" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup
                      v-for="group in providerGroups"
                      :key="group.kind"
                    >
                      <SelectLabel>{{ $t(group.labelKey) }}</SelectLabel>
                      <SelectItem
                        v-for="option in group.options"
                        :key="option.value"
                        :value="option.value"
                      >
                        <span class="flex items-center gap-2">
                          <SearchProviderLogo
                            :provider="option.provider"
                            size="xs"
                          />
                          <span>{{ $t(option.labelKey, option.provider) }}</span>
                        </span>
                      </SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </FormControl>
            </FieldStack>
          </FormField>
        </div>
      </template>
    </FormDialogShell>
  </section>
</template>

<script setup lang="ts">
import {
  Button,
  Input,
  FormField,
  FormControl,
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
} from '@felinic/ui'
import { toTypedSchema } from '@vee-validate/zod'
import z from 'zod'
import { useForm } from 'vee-validate'
import { useMutation, useQueryCache } from '@pinia/colada'
import { postFetchProviders, postSearchProviders } from '@memohai/sdk'
import type { FetchprovidersCreateRequest, SearchprovidersCreateRequest } from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import { Plus } from 'lucide-vue-next'
import { FieldStack, FormDialogShell } from '@felinic/ui'
import { useDialogMutation } from '@/composables/useDialogMutation'
import SearchProviderLogo from '@/components/search-provider-logo/index.vue'

const SEARCH_PROVIDER_TYPES = ['brave', 'bing', 'google', 'tavily', 'sogou', 'serper', 'searxng', 'jina', 'exa', 'bocha', 'duckduckgo', 'yandex'] as const
const FETCH_PROVIDER_TYPES = ['jina', 'cloudflare_markdown'] as const

type ProviderKind = 'search' | 'fetch'

interface ProviderOption {
  kind: ProviderKind
  provider: string
  value: string
  labelKey: string
}

const providerGroups: Array<{ kind: ProviderKind; labelKey: string; options: ProviderOption[] }> = [
  {
    kind: 'search',
    labelKey: 'webSearch.searchProviders',
    options: SEARCH_PROVIDER_TYPES.map(provider => ({
      kind: 'search',
      provider,
      value: `search:${provider}`,
      labelKey: `webSearch.providerNames.${provider}`,
    })),
  },
  {
    kind: 'fetch',
    labelKey: 'webSearch.fetchProviders',
    options: FETCH_PROVIDER_TYPES.map(provider => ({
      kind: 'fetch',
      provider,
      value: `fetch:${provider}`,
      labelKey: `webSearch.fetchProviderNames.${provider}`,
    })),
  },
]

const providerOptions = providerGroups.flatMap(group => group.options)

const open = defineModel<boolean>('open')
withDefaults(defineProps<{
  hideTrigger?: boolean
}>(), {
  hideTrigger: false,
})
const { t } = useI18n()
const { run } = useDialogMutation()

const queryCache = useQueryCache()
const { mutateAsync: createProviderMutation, isLoading } = useMutation({
  mutation: async (data: { name: string; target: string }) => {
    const option = providerOptions.find(item => item.value === data.target)
    if (!option) {
      throw new Error('Unknown web provider type')
    }

    const body = { name: data.name, provider: option.provider, config: {} }
    if (option.kind === 'fetch') {
      const { data: result } = await postFetchProviders({ body: body as FetchprovidersCreateRequest, throwOnError: true })
      return result
    }

    const { data: result } = await postSearchProviders({ body: body as SearchprovidersCreateRequest, throwOnError: true })
    return result
  },
  onSettled: () => {
    queryCache.invalidateQueries({ key: ['search-providers'] })
    queryCache.invalidateQueries({ key: ['fetch-providers'] })
  },
})

const providerSchema = toTypedSchema(z.object({
  name: z.string().min(1, t('webSearch.nameRequired')),
  target: z.string().min(1, t('webSearch.providerRequired')),
}))

const form = useForm({
  validationSchema: providerSchema,
})

const handleCreate = form.handleSubmit(async (value) => {
  await run(
    () => createProviderMutation(value),
    {
      fallbackMessage: t('common.saveFailed'),
      onSuccess: () => {
        open.value = false
      },
    },
  )
})
</script>
