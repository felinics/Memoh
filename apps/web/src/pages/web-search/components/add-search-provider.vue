<template>
  <FormDialogShell
    v-model:open="open"
    :title="$t('webSearch.addSearch')"
    :cancel-text="$t('common.cancel')"
    :submit-text="$t('common.save')"
    :submit-disabled="form.meta.value.valid === false || isLoading || !selectedMeta"
    :loading="isLoading"
    max-width-class="sm:max-w-xl"
    @submit="handleCreate"
  >
    <template #trigger>
      <span
        v-if="hideTrigger"
        class="hidden"
      />
      <Button
        v-else
        variant="outline"
      >
        <Plus class="size-4" />
        {{ $t('webSearch.addSearch') }}
      </Button>
    </template>

    <template #body>
      <FormStack class="mt-4">
        <FieldStack :label="$t('webSearch.provider')">
          <div class="flex h-9 items-center gap-2 rounded-md border border-input bg-muted px-3 text-sm">
            <SearchProviderLogo
              :provider="initialProvider"
              size="xs"
            />
            <span>{{ providerDisplayName }}</span>
          </div>
        </FieldStack>

        <FormField
          v-slot="{ componentField }"
          name="name"
        >
          <FieldStack :label="$t('common.name')">
            <FormControl>
              <Input
                :placeholder="$t('common.namePlaceholder')"
                v-bind="componentField"
              />
            </FormControl>
          </FieldStack>
        </FormField>

        <FieldStack
          v-for="field in configFields"
          :key="field.key"
          :help="field.description"
        >
          <template #label>
            <Label>
              {{ field.title }}
              <span
                v-if="field.required"
                class="text-destructive"
              >*</span>
            </Label>
          </template>

          <Switch
            v-if="field.type === 'bool' || field.type === 'boolean'"
            :model-value="configData[field.key] === true"
            @update:model-value="value => configData[field.key] = value"
          />
          <Select
            v-else-if="field.enum.length > 0"
            :model-value="String(configData[field.key] ?? '')"
            @update:model-value="value => configData[field.key] = value"
          >
            <SelectTrigger class="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem
                v-for="option in field.enum"
                :key="option"
                :value="option"
              >
                {{ option }}
              </SelectItem>
            </SelectContent>
          </Select>
          <Input
            v-else
            :model-value="String(configData[field.key] ?? '')"
            :type="field.secret ? 'password' : field.type === 'number' || field.type === 'integer' ? 'number' : 'text'"
            :placeholder="field.example === undefined ? '' : String(field.example)"
            @update:model-value="value => updateConfig(field.key, field.type, value)"
          />
        </FieldStack>
      </FormStack>
    </template>
  </FormDialogShell>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import {
  Button,
  FormControl,
  FormField,
  Input,
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Switch,
  toast,
} from '@felinic/ui'
import { Plus } from 'lucide-vue-next'
import { toTypedSchema } from '@vee-validate/zod'
import { useForm } from 'vee-validate'
import z from 'zod'
import { useMutation, useQuery, useQueryCache } from '@pinia/colada'
import { getSearchProvidersMeta, postSearchProviders } from '@memohai/sdk'
import type { SearchprovidersCreateRequest, SearchprovidersProviderMeta, SearchprovidersProviderName } from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import { FieldStack, FormDialogShell, FormStack } from '@felinic/ui'
import SearchProviderLogo from '@/components/search-provider-logo/index.vue'
import { useDialogMutation } from '@/composables/useDialogMutation'
import { normalizeProviderConfigFields } from '@/utils/provider-template'

const open = defineModel<boolean>('open')
const props = withDefaults(defineProps<{
  hideTrigger?: boolean
  initialProvider?: string
}>(), {
  hideTrigger: false,
  initialProvider: '',
})

const { t } = useI18n()
const { run } = useDialogMutation()
const queryCache = useQueryCache()
const configData = reactive<Record<string, unknown>>({})

const { data: providerMetaData } = useQuery({
  key: () => ['search-providers-meta'],
  query: async () => {
    const { data } = await getSearchProvidersMeta({ throwOnError: true })
    return data
  },
})

const providerMetas = computed<SearchprovidersProviderMeta[]>(() =>
  Array.isArray(providerMetaData.value) ? providerMetaData.value : [],
)
const selectedMeta = computed(() => providerMetas.value.find(meta => meta.provider === props.initialProvider))
const providerDisplayName = computed(() =>
  selectedMeta.value?.display_name ?? t(`webSearch.providerNames.${props.initialProvider}`, props.initialProvider),
)
const configFields = computed(() => normalizeProviderConfigFields(selectedMeta.value?.config_schema))

const schema = toTypedSchema(z.object({
  name: z.string().min(1, t('webSearch.nameRequired')),
}))
const form = useForm({ validationSchema: schema })

function replaceConfig(config: Record<string, unknown>) {
  Object.keys(configData).forEach(key => delete configData[key])
  Object.assign(configData, config)
}

function defaultConfig() {
  return Object.fromEntries(configFields.value
    .filter(field => !field.secret && field.example !== undefined)
    .map(field => [field.key, field.example]))
}

function resetForm() {
  form.resetForm({ values: { name: providerDisplayName.value } })
  replaceConfig(defaultConfig())
}

function updateConfig(key: string, type: string, value: string | number) {
  if ((type === 'number' || type === 'integer') && value !== '') {
    configData[key] = Number(value)
    return
  }
  configData[key] = value
}

watch(open, (isOpen) => {
  if (isOpen) resetForm()
})
watch(selectedMeta, (meta, previous) => {
  if (open.value && meta && meta !== previous) resetForm()
})

const { mutateAsync: createProvider, isLoading } = useMutation({
  mutation: async (value: { name: string }) => {
    const body: SearchprovidersCreateRequest = {
      name: value.name.trim(),
      provider: props.initialProvider as SearchprovidersProviderName,
      config: { ...configData },
    }
    const { data } = await postSearchProviders({ body, throwOnError: true })
    return data
  },
  onSettled: () => queryCache.invalidateQueries({ key: ['search-providers'] }),
})

const handleCreate = form.handleSubmit(async (value) => {
  const missing = configFields.value.find((field) => {
    if (!field.required) return false
    const fieldValue = configData[field.key]
    if (field.type === 'bool' || field.type === 'boolean') return fieldValue === undefined || fieldValue === null
    return !String(fieldValue ?? '').trim()
  })
  if (missing) {
    toast.error(t('provider.requiredField', { field: missing.title }))
    return
  }
  await run(
    () => createProvider(value),
    {
      fallbackMessage: t('common.saveFailed'),
      onSuccess: () => {
        toast.success(t('webSearch.saveSuccess'))
        open.value = false
      },
    },
  )
})
</script>
