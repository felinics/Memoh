<template>
  <section class="ml-auto">
    <FormDialogShell
      v-model:open="open"
      :title="title === 'edit' ? $t('models.editModel') : $t('models.addModel')"
      :cancel-text="$t('common.cancel')"
      :submit-text="title === 'edit' ? $t('common.confirm') : $t('models.addModel')"
      :submit-disabled="!canSubmit"
      :loading="isLoading"
      @submit="addModel"
    >
      <template #trigger>
        <Button
          variant="default"
          :size="size"
        >
          {{ $t('models.addModel') }}
        </Button>
      </template>
      <template #body>
        <div class="flex flex-col gap-3 mt-4">
          <!-- Type -->
          <FormField
            v-if="!hideType"
            v-slot="{ componentField }"
            name="type"
          >
            <FieldStack :label="$t('common.type')">
              <FormControl>
                <Select v-bind="componentField">
                  <SelectTrigger
                    class="w-full"
                    :aria-label="$t('common.type')"
                  >
                    <SelectValue :placeholder="$t('common.typePlaceholder')" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem
                        v-for="opt in typeOptions"
                        :key="opt.value"
                        :value="opt.value"
                      >
                        {{ opt.label }}
                      </SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </FormControl>
            </FieldStack>
          </FormField>

          <!-- Model -->
          <FormField
            v-slot="{ componentField }"
            name="model_id"
          >
            <FieldStack
              :label="$t('models.model')"
              for="create-model-model-id"
            >
              <FormControl>
                <Input
                  id="create-model-model-id"
                  type="text"
                  :placeholder="$t('models.modelPlaceholder')"
                  v-bind="componentField"
                />
              </FormControl>
            </FieldStack>
          </FormField>

          <!-- Display Name: optional, empty by default. We deliberately do NOT
               mirror model_id into it — a name that just repeats the id is noise
               on the list. Only a name the user actually types is kept. -->
          <FormField
            v-slot="{ componentField }"
            name="name"
          >
            <FieldStack for="create-model-name">
              <template #label>
                <Label for="create-model-name">
                  {{ $t('models.displayName') }}
                  <span class="text-muted-foreground text-xs ml-1">({{ $t('common.optional') }})</span>
                </Label>
              </template>
              <FormControl>
                <Input
                  id="create-model-name"
                  type="text"
                  :placeholder="$t('models.displayNamePlaceholder')"
                  v-bind="componentField"
                />
              </FormControl>
            </FieldStack>
          </FormField>

          <FormField
            v-slot="{ componentField }"
            name="description"
          >
            <FieldStack for="create-model-description">
              <template #label>
                <Label for="create-model-description">
                  {{ $t('models.description') }}
                  <span class="text-muted-foreground text-xs ml-1">({{ $t('common.optional') }})</span>
                </Label>
              </template>
              <FormControl>
                <Textarea
                  id="create-model-description"
                  :placeholder="$t('models.descriptionPlaceholder')"
                  v-bind="componentField"
                />
              </FormControl>
            </FieldStack>
          </FormField>

          <!-- Dimensions (embedding only) -->
          <FormField
            v-if="selectedType === 'embedding'"
            v-slot="{ componentField }"
            name="dimensions"
          >
            <FieldStack
              :label="$t('models.dimensions')"
              for="create-model-dimensions"
            >
              <FormControl>
                <Input
                  id="create-model-dimensions"
                  type="number"
                  :placeholder="$t('models.dimensionsPlaceholder')"
                  v-bind="componentField"
                />
              </FormControl>
            </FieldStack>
          </FormField>

          <!-- Compatibilities (chat only) -->
          <FieldStack
            v-if="selectedType === 'chat'"
            :label="$t('models.compatibilities')"
          >
            <div class="flex flex-wrap gap-3">
              <label
                v-for="opt in COMPATIBILITY_OPTIONS"
                :key="opt.value"
                class="flex items-center gap-1.5 text-xs"
              >
                <Checkbox
                  :model-value="selectedCompat.includes(opt.value)"
                  @update:model-value="(val: boolean) => toggleCompat(opt.value, val)"
                />
                {{ $t(`models.compatibility.${opt.value}`) }}
              </label>
            </div>
          </FieldStack>

          <!-- Supported efforts (chat + reasoning only). Hand-added models get
               no catalog fill, so this is the only place a BYOK user can say
               which tiers the model accepts and whether it can be turned off. -->
          <FieldStack
            v-if="selectedType === 'chat' && selectedCompat.includes('reasoning') && !alwaysThinking"
            :label="$t('models.reasoningEfforts')"
          >
            <Select
              v-model="selectedEfforts"
              multiple
            >
              <SelectTrigger
                class="w-full"
                :aria-label="$t('models.reasoningEfforts')"
              >
                <SelectValue :placeholder="$t('models.reasoningEffortsPlaceholder')" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem
                    v-for="effort in DECLARABLE_EFFORTS"
                    :key="effort"
                    :value="effort"
                  >
                    {{ $t(EFFORT_LABELS[effort] ?? effort) }}
                  </SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </FieldStack>

          <!-- Context Window (optional) -->
          <FormField
            v-if="selectedType === 'chat'"
            v-slot="{ componentField }"
            name="context_window"
          >
            <FieldStack for="create-model-context-window">
              <template #label>
                <Label for="create-model-context-window">
                  {{ $t('models.contextWindow') }}
                  <span class="text-muted-foreground text-xs ml-1">({{ $t('common.optional') }})</span>
                </Label>
              </template>
              <FormControl>
                <Input
                  id="create-model-context-window"
                  type="number"
                  :placeholder="$t('models.contextWindowPlaceholder')"
                  v-bind="componentField"
                />
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
  Input,
  Button,
  FormField,
  FormControl,
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Checkbox,
  Label,
  Textarea,
} from '@felinic/ui'
import type { ButtonVariants } from '@felinic/ui'
import { useForm } from 'vee-validate'
import { inject, computed, watch, nextTick, type Ref, ref } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import z from 'zod'
import { useMutation, useQueryCache } from '@pinia/colada'
import { postModels, putModelsById, putModelsModelByModelId } from '@memohai/sdk'
import type { ModelsGetResponse, ModelsAddRequest, ModelsUpdateRequest } from '@memohai/sdk'
import { useI18n } from 'vue-i18n'
import { FieldStack, FormDialogShell } from '@felinic/ui'
import { COMPATIBILITY_OPTIONS } from '@/constants/compatibilities'
import { useDialogMutation } from '@/composables/useDialogMutation'
import { buildModelConfig, DECLARABLE_EFFORTS } from './model-config'
import { EFFORT_LABELS } from '@/pages/bots/components/reasoning-effort'

interface ModelTypeOption {
  value: string
  label: string
}

const selectedCompat = ref<string[]>([])
// The server serves an undeclared effort list as low/medium/high, so that is
// what the picker starts from: leaving it untouched keeps the old behavior.
const DEFAULT_EFFORTS = ['low', 'medium', 'high']
const selectedEfforts = ref<string[]>([...DEFAULT_EFFORTS])
// Whether the model being edited already declared an effort list. Without one,
// an untouched default selection is saved as "still undeclared" rather than
// pinning low/medium/high onto a row the user never configured.
const storedEffortsDeclared = ref(false)
const { t } = useI18n()
const { run } = useDialogMutation()

const formSchema = toTypedSchema(z.object({
  type: z.string().min(1, t('models.typeRequired')),
  model_id: z.string().min(1, t('models.modelIdRequired')),
  name: z.string().optional(),
  description: z.string().optional(),
  dimensions: z.coerce.number().min(1, t('models.dimensionsMin')).optional(),
  context_window: z.coerce.number().min(1, t('models.contextWindowMin')).optional(),
}))

const props = withDefaults(defineProps<{
  id: string
  typeOptions?: ModelTypeOption[]
  defaultType?: string
  hideType?: boolean
  invalidateKeys?: string[]
  size?: ButtonVariants['size']
}>(), {
  typeOptions: () => [
    { value: 'chat', label: 'Chat' },
    { value: 'embedding', label: 'Embedding' },
  ],
  defaultType: 'chat',
  hideType: false,
  invalidateKeys: () => ['provider-models', 'models'],
  size: 'default',
})

const form = useForm({
  validationSchema: formSchema,
  initialValues: {
    type: props.defaultType,
  },
})

const selectedType = computed(() => form.values.type || props.defaultType)

const open = inject<Ref<boolean>>('openModel', ref(false))
const title = inject<Ref<'edit' | 'title'>>('openModelTitle', ref('title'))
const editInfo = inject<Ref<ModelsGetResponse | null>>('openModelState', ref(null))

// The server sends no reasoning control at all for an always-thinking model
// (reasoning.ResolveConfig), so an effort picker there would have no effect.
// Snapshotted on open: editInfo is cleared on close, while the exit animation
// is still rendering the form.
const alwaysThinking = ref(false)
const storedEfforts = ref<string[]>([])

function effortsToSave(): string[] {
  if (alwaysThinking.value) return storedEfforts.value
  const selected = selectedEfforts.value
  const isDefault = selected.length === DEFAULT_EFFORTS.length
    && DEFAULT_EFFORTS.every(effort => selected.includes(effort))
  if (!storedEffortsDeclared.value && isDefault) return []
  return selected
}

const canSubmit = computed(() => {
  if (title.value === 'edit') return true
  const { type, model_id } = form.values
  if (!type || !model_id) return false
  return true
})

function toggleCompat(cap: string, checked: boolean) {
  if (checked) {
    selectedCompat.value = [...selectedCompat.value, cap]
  } else {
    selectedCompat.value = selectedCompat.value.filter(c => c !== cap)
  }
}

const queryCache = useQueryCache()
function invalidateModelQueries() {
  for (const key of props.invalidateKeys) {
    queryCache.invalidateQueries({ key: [key] })
  }
}

const { mutateAsync: createModel, isLoading: createLoading } = useMutation({
  mutation: async (data: Record<string, unknown>) => {
    const { data: result } = await postModels({ body: data as ModelsAddRequest, throwOnError: true })
    return result
  },
  onSettled: invalidateModelQueries,
})
const { mutateAsync: updateModel, isLoading: updateLoading } = useMutation({
  mutation: async ({ id, data }: { id: string; data: Record<string, unknown> }) => {
    const { data: result } = await putModelsById({
      path: { id },
      body: data as ModelsUpdateRequest,
      throwOnError: true,
    })
    return result
  },
  onSettled: invalidateModelQueries,
})
const { mutateAsync: updateModelByLegacyModelID, isLoading: updateLegacyLoading } = useMutation({
  mutation: async ({ modelId, data }: { modelId: string; data: Record<string, unknown> }) => {
    const { data: result } = await putModelsModelByModelId({
      path: { modelId },
      body: data as ModelsUpdateRequest,
      throwOnError: true,
    })
    return result
  },
  onSettled: invalidateModelQueries,
})
const isLoading = computed(() => createLoading.value || updateLoading.value || updateLegacyLoading.value)

async function addModel() {
  const isEdit = title.value === 'edit' && !!editInfo?.value
  const fallback = editInfo?.value

  const type = form.values.type || (isEdit ? fallback!.type : 'chat')
  const model_id = form.values.model_id || (isEdit ? fallback!.model_id : '')
  const name = form.values.name ?? (isEdit ? fallback!.name : '')

  if (!type || !model_id) return

  // A cleared number input holds '' rather than undefined. Treat it as empty so
  // the server detects dimensions instead of rejecting a string for an int.
  const dimensions = typeof form.values.dimensions === 'number' ? form.values.dimensions : undefined

  const config = buildModelConfig({
    type,
    description: form.values.description,
    dimensions: dimensions ?? (isEdit ? fallback!.config?.dimensions : undefined),
    contextWindow: form.values.context_window ?? (isEdit ? fallback!.config?.context_window : undefined),
    compatibilities: selectedCompat.value,
    reasoningEfforts: effortsToSave(),
    existing: isEdit ? fallback!.config : undefined,
  })

  const payload: Record<string, unknown> = {
    type,
    model_id,
    provider_id: props.id,
    config,
  }

  // Keep a name only when it carries information beyond the id. Persisting a
  // name equal to model_id is what made the list show the same string twice.
  if (name && name.trim() && name.trim() !== model_id) {
    payload.name = name.trim()
  }

  await run(
    () => {
      if (isEdit) {
        const modelUUID = fallback?.id
        if (modelUUID) {
          return updateModel({ id: modelUUID, data: payload as ModelsUpdateRequest })
        }
        return updateModelByLegacyModelID({ modelId: fallback!.model_id, data: payload as ModelsUpdateRequest })
      }
      return createModel(payload)
    },
    {
      fallbackMessage: t('common.saveFailed'),
      onSuccess: () => {
        open.value = false
      },
    },
  )
}

watch(open, async () => {
  if (!open.value) {
    // Keep the mode until the next open: the dialog is still playing its exit
    // animation here, and flipping it now flashes "Add model" on an edit dialog.
    editInfo.value = null
    return
  }

  // Openers set the edit state together with opening; the built-in trigger
  // opens without it, which means add. Runs before render, so no stale frame.
  if (!editInfo.value) title.value = 'title'

  await nextTick()

  if (editInfo?.value) {
    const { type, model_id, name, config } = editInfo.value
    form.resetForm({
      values: {
        type: type || 'chat',
        model_id,
        // Older models stored name === model_id. Don't surface that as a
        // "custom" name in the field — treat it as empty so editing starts clean.
        name: name && name !== model_id ? name : '',
        description: config?.description ?? '',
        dimensions: config?.dimensions,
        context_window: config?.context_window,
      },
    })
    selectedCompat.value = config?.compatibilities ?? []
    alwaysThinking.value = config?.thinking_mode === 'always'
    storedEfforts.value = [...(config?.reasoning_efforts ?? [])]
    storedEffortsDeclared.value = !!config?.reasoning_efforts?.length
    selectedEfforts.value = config?.reasoning_efforts?.length
      ? [...config.reasoning_efforts]
      : [...DEFAULT_EFFORTS]
  } else {
    form.resetForm({
      values: {
        type: props.defaultType,
        model_id: '',
        name: '',
        description: '',
        dimensions: undefined,
        context_window: undefined,
      },
    })
    selectedCompat.value = []
    alwaysThinking.value = false
    storedEfforts.value = []
    storedEffortsDeclared.value = false
    selectedEfforts.value = [...DEFAULT_EFFORTS]
  }
}, {
  immediate: true,
})
</script>
