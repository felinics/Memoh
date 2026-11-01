<template>
  <section class="ml-auto">
    <Dialog v-model:open="open">
      <DialogTrigger as-child>
        <Button
          variant="default"
          :size="size"
        >
          {{ $t('models.addModel') }}
        </Button>
      </DialogTrigger>
      <!-- A chat model with reasoning settings outgrows the viewport, so the
           fields scroll inside the capped panel while the footer stays put. -->
      <DialogPanel
        width="lg"
        footer
        :aria-describedby="undefined"
      >
        <DialogHeader>
          <DialogTitle>{{ title === 'edit' ? $t('models.editModel') : $t('models.addModel') }}</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <form
            id="create-model-form"
            class="flex flex-col gap-3"
            @submit.prevent="addModel"
          >
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

            <!-- Capabilities (chat only). Modalities, tool calling and reasoning
               follow the model config schema the server stores. -->
            <template v-if="selectedType === 'chat'">
              <FieldStack :label="$t('models.inputModalities')">
                <div class="flex flex-wrap gap-3">
                  <label
                    v-for="modality in INPUT_MODALITIES"
                    :key="modality"
                    class="flex items-center gap-1.5 text-xs"
                  >
                    <Checkbox
                      :model-value="capabilities.input.includes(modality)"
                      :disabled="modality === 'text'"
                      @update:model-value="(val: boolean | 'indeterminate') => toggleModality('input', modality, val === true)"
                    />
                    {{ $t(`models.modality.${modality}`) }}
                  </label>
                </div>
              </FieldStack>

              <FieldStack :label="$t('models.outputModalities')">
                <div class="flex flex-wrap gap-3">
                  <label
                    v-for="modality in OUTPUT_MODALITIES"
                    :key="modality"
                    class="flex items-center gap-1.5 text-xs"
                  >
                    <Checkbox
                      :model-value="capabilities.output.includes(modality)"
                      @update:model-value="(val: boolean | 'indeterminate') => toggleModality('output', modality, val === true)"
                    />
                    {{ $t(`models.modality.${modality}`) }}
                  </label>
                </div>
              </FieldStack>

              <FieldStack :label="$t('models.capabilities')">
                <div class="flex flex-wrap gap-3">
                  <label class="flex items-center gap-1.5 text-xs">
                    <Checkbox
                      :model-value="capabilities.toolCall"
                      @update:model-value="(val: boolean | 'indeterminate') => capabilities.toolCall = val === true"
                    />
                    {{ $t('models.capability.toolCall') }}
                  </label>
                  <label class="flex items-center gap-1.5 text-xs">
                    <Checkbox
                      :model-value="capabilities.structuredOutput"
                      @update:model-value="(val: boolean | 'indeterminate') => capabilities.structuredOutput = val === true"
                    />
                    {{ $t('models.capability.structuredOutput') }}
                  </label>
                  <label class="flex items-center gap-1.5 text-xs">
                    <Checkbox
                      :model-value="reasoning.enabled"
                      @update:model-value="(val: boolean | 'indeterminate') => reasoning.enabled = val === true"
                    />
                    {{ $t('models.capability.reasoning') }}
                  </label>
                </div>
              </FieldStack>

              <template v-if="reasoning.enabled">
                <FieldStack
                  :label="$t('models.reasoningEfforts')"
                  :help="reasoningIsUncontrollable(reasoning) ? $t('models.reasoningUncontrollableHint') : undefined"
                >
                  <Select
                    :model-value="reasoning.efforts"
                    multiple
                    @update:model-value="onEffortsUpdate"
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
                          v-for="effort in REASONING_EFFORTS"
                          :key="effort"
                          :value="effort"
                        >
                          {{ $t(EFFORT_LABELS[effort] ?? effort) }}
                        </SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                </FieldStack>

                <label class="flex items-center gap-1.5 text-xs">
                  <Checkbox
                    :model-value="reasoning.toggle"
                    @update:model-value="(val: boolean | 'indeterminate') => reasoning.toggle = val === true"
                  />
                  {{ $t('models.reasoningToggle') }}
                </label>

                <FieldStack :label="$t('models.reasoningDialect')">
                  <Select
                    :model-value="reasoning.dialect || UNSET"
                    @update:model-value="(val: unknown) => reasoning.dialect = fromSelect(val)"
                  >
                    <SelectTrigger
                      class="w-full"
                      :aria-label="$t('models.reasoningDialect')"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectGroup>
                        <SelectItem :value="UNSET">
                          {{ $t('models.reasoningDialectAuto') }}
                        </SelectItem>
                        <SelectItem
                          v-for="dialect in REASONING_DIALECTS"
                          :key="dialect"
                          :value="dialect"
                        >
                          {{ $t(`models.reasoningDialectOption.${dialect}`) }}
                        </SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                </FieldStack>

                <div>
                  <TextButton
                    type="button"
                    @click="showMoreReasoning = !showMoreReasoning"
                  >
                    {{ showMoreReasoning ? $t('models.reasoningLess') : $t('models.reasoningMore') }}
                  </TextButton>
                </div>

                <template v-if="showMoreReasoning">
                  <FieldStack :label="$t('models.reasoningOff')">
                    <Select
                      :model-value="reasoning.off || UNSET"
                      @update:model-value="(val: unknown) => reasoning.off = fromSelect(val)"
                    >
                      <SelectTrigger
                        class="w-full"
                        :aria-label="$t('models.reasoningOff')"
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectGroup>
                          <SelectItem :value="UNSET">
                            {{ $t('models.notStated') }}
                          </SelectItem>
                          <SelectItem
                            v-for="off in REASONING_OFF_SUPPORT"
                            :key="off"
                            :value="off"
                          >
                            {{ $t(`models.reasoningOffOption.${off}`) }}
                          </SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                  </FieldStack>

                  <FieldStack
                    :label="$t('models.reasoningDefault')"
                    :help="$t('models.reasoningDefaultHint')"
                  >
                    <Select
                      :model-value="reasoning.defaultEnabled || UNSET"
                      @update:model-value="(val: unknown) => reasoning.defaultEnabled = fromSelect(val) as ReasoningForm['defaultEnabled']"
                    >
                      <SelectTrigger
                        class="w-full"
                        :aria-label="$t('models.reasoningDefault')"
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectGroup>
                          <SelectItem :value="UNSET">
                            {{ $t('models.notStated') }}
                          </SelectItem>
                          <SelectItem value="on">
                            {{ $t('chat.reasoningOn') }}
                          </SelectItem>
                          <SelectItem value="off">
                            {{ $t('chat.reasoningOff') }}
                          </SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                  </FieldStack>

                  <FieldStack
                    v-if="reasoning.efforts.length"
                    :label="$t('models.reasoningDefaultEffort')"
                  >
                    <Select
                      :model-value="reasoning.efforts.includes(reasoning.defaultEffort) ? reasoning.defaultEffort : UNSET"
                      @update:model-value="(val: unknown) => reasoning.defaultEffort = fromSelect(val)"
                    >
                      <SelectTrigger
                        class="w-full"
                        :aria-label="$t('models.reasoningDefaultEffort')"
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectGroup>
                          <SelectItem :value="UNSET">
                            {{ $t('models.notStated') }}
                          </SelectItem>
                          <SelectItem
                            v-for="effort in reasoning.efforts"
                            :key="effort"
                            :value="effort"
                          >
                            {{ $t(EFFORT_LABELS[effort] ?? effort) }}
                          </SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                  </FieldStack>

                  <FieldStack :label="$t('models.reasoningBudget')">
                    <div class="flex items-center gap-2">
                      <Input
                        v-model.number="reasoning.budgetMin"
                        type="number"
                        min="0"
                        :aria-invalid="budgetError === 'range' || undefined"
                        :aria-label="$t('models.reasoningBudgetMin')"
                        :placeholder="$t('models.reasoningBudgetMin')"
                      />
                      <Input
                        v-model.number="reasoning.budgetMax"
                        type="number"
                        min="1"
                        :aria-invalid="!!budgetError || undefined"
                        :aria-label="$t('models.reasoningBudgetMax')"
                        :placeholder="$t('models.reasoningBudgetMax')"
                      />
                    </div>
                    <FieldError v-if="budgetError">
                      {{ $t(budgetError === 'max' ? 'models.reasoningBudgetMaxMin' : 'models.reasoningBudgetRange') }}
                    </FieldError>
                  </FieldStack>

                  <!-- Only a Chat Completions host reads the replay; elsewhere the
                       stored value is kept but not offered. -->
                  <template v-if="offersReplay">
                    <FieldStack
                      :label="$t('models.reasoningReplay')"
                      :help="$t('models.reasoningReplayHint')"
                    >
                      <Select
                        :model-value="reasoning.replayField || UNSET"
                        @update:model-value="(val: unknown) => reasoning.replayField = fromSelect(val)"
                      >
                        <SelectTrigger
                          class="w-full"
                          :aria-label="$t('models.reasoningReplay')"
                        >
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectGroup>
                            <SelectItem :value="UNSET">
                              {{ $t('models.reasoningReplayProvider') }}
                            </SelectItem>
                            <SelectItem
                              v-for="field in REASONING_REPLAY_FIELDS"
                              :key="field"
                              :value="field"
                            >
                              {{ field }}
                            </SelectItem>
                          </SelectGroup>
                        </SelectContent>
                      </Select>
                    </FieldStack>

                    <label
                      v-if="reasoning.replayField && reasoning.replayField !== 'reasoning_details'"
                      class="flex items-center gap-1.5 text-xs"
                    >
                      <Checkbox
                        :model-value="reasoning.replayRequired"
                        @update:model-value="(val: boolean | 'indeterminate') => reasoning.replayRequired = val === true"
                      />
                      {{ $t('models.reasoningReplayRequired') }}
                    </label>
                  </template>
                </template>
              </template>
            </template>

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

            <!-- Max Output Tokens (optional) -->
            <FormField
              v-if="selectedType === 'chat'"
              v-slot="{ componentField }"
              name="max_output_tokens"
            >
              <FieldStack for="create-model-max-output-tokens">
                <template #label>
                  <Label for="create-model-max-output-tokens">
                    {{ $t('models.maxOutputTokens') }}
                    <span class="text-muted-foreground text-xs ml-1">({{ $t('common.optional') }})</span>
                  </Label>
                </template>
                <FormControl>
                  <Input
                    id="create-model-max-output-tokens"
                    type="number"
                    :placeholder="$t('models.maxOutputTokensPlaceholder')"
                    v-bind="componentField"
                  />
                </FormControl>
              </FieldStack>
            </FormField>
          </form>
        </DialogBody>
        <DialogFooter>
          <DialogClose as-child>
            <Button
              type="button"
              variant="outline"
            >
              {{ $t('common.cancel') }}
            </Button>
          </DialogClose>
          <Button
            type="submit"
            form="create-model-form"
            :disabled="!canSubmit"
            :loading="isLoading"
          >
            {{ title === 'edit' ? $t('common.confirm') : $t('models.addModel') }}
          </Button>
        </DialogFooter>
      </DialogPanel>
    </Dialog>
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
  TextButton,
  Dialog,
  DialogBody,
  DialogClose,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  DialogTrigger,
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
import { FieldError, FieldStack } from '@felinic/ui'
import { useDialogMutation } from '@/composables/useDialogMutation'
import {
  buildModelConfig,
  capabilityFormFrom,
  defaultCapabilityForm,
  defaultReasoningForm,
  INPUT_MODALITIES,
  OUTPUT_MODALITIES,
  REASONING_DIALECTS,
  REASONING_EFFORTS,
  REASONING_OFF_SUPPORT,
  REASONING_REPLAY_FIELDS,
  reasoningBudgetError,
  reasoningFormFrom,
  reasoningIsUncontrollable,
  type CapabilityForm,
  type ReasoningForm,
} from './model-config'
import { EFFORT_LABELS } from '@/pages/bots/components/reasoning-effort'

interface ModelTypeOption {
  value: string
  label: string
}

const capabilities = ref<CapabilityForm>(defaultCapabilityForm())
const reasoning = ref<ReasoningForm>(defaultReasoningForm())
const showMoreReasoning = ref(false)
// Reka's Select cannot hold an empty value, so "not stated" travels as this.
const UNSET = '__unset'
const { t } = useI18n()
const { run } = useDialogMutation()

const formSchema = toTypedSchema(z.object({
  type: z.string().min(1, t('models.typeRequired')),
  model_id: z.string().min(1, t('models.modelIdRequired')),
  name: z.string().optional(),
  description: z.string().optional(),
  dimensions: z.coerce.number().min(1, t('models.dimensionsMin')).optional(),
  context_window: z.coerce.number().min(1, t('models.contextWindowMin')).optional(),
  max_output_tokens: z.coerce.number().min(1, t('models.maxOutputTokensMin')).optional(),
}))

const props = withDefaults(defineProps<{
  id?: string
  // Resolves the provider on submit when `id` is not known yet (a template
  // draft); cancelling the dialog therefore never creates a provider.
  ensureProviderId?: () => Promise<string>
  // The provider's client type, when the caller knows it.
  clientType?: string
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

const emit = defineEmits<{
  // A model was added, by its model id; editing one does not emit.
  created: [modelId: string]
  // The dialog closed, whether or not anything was saved.
  closed: []
}>()

const form = useForm({
  validationSchema: formSchema,
  initialValues: {
    type: props.defaultType,
  },
})

const selectedType = computed(() => form.values.type || props.defaultType)
// The reasoning replay is sent only to a Chat Completions host; OpenCode Go
// replays reasoning on its own, and the other protocols carry it natively.
const offersReplay = computed(() => !props.clientType || props.clientType === 'openai-completions')

const open = inject<Ref<boolean>>('openModel', ref(false))
const title = inject<Ref<'edit' | 'title'>>('openModelTitle', ref('title'))
const editInfo = inject<Ref<ModelsGetResponse | null>>('openModelState', ref(null))

function fromSelect(value: unknown): string {
  return typeof value === 'string' && value !== UNSET ? value : ''
}

function onEffortsUpdate(value: unknown) {
  reasoning.value.efforts = Array.isArray(value) ? value.map(String) : []
}

function toggleModality(direction: 'input' | 'output', modality: CapabilityForm['input'][number], checked: boolean) {
  const current = capabilities.value[direction]
  capabilities.value[direction] = checked
    ? [...current.filter(m => m !== modality), modality]
    : current.filter(m => m !== modality)
}

// The server refuses a budget whose maximum is not positive or below the
// minimum; the form says so inline and holds the submit until it is fixed.
// Only a chat model saves reasoning, so another type is never held by it.
const budgetError = computed(() => selectedType.value === 'chat' ? reasoningBudgetError(reasoning.value) : undefined)

const canSubmit = computed(() => {
  if (budgetError.value) return false
  if (title.value === 'edit') return true
  const { type, model_id } = form.values
  if (!type || !model_id) return false
  return true
})

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
    // A cleared input holds ''. That removes the stored context window, so it
    // must not fall through to the edit fallback below.
    contextWindow: form.values.context_window === ''
      ? undefined
      : (typeof form.values.context_window === 'number' ? form.values.context_window : undefined)
        ?? (isEdit ? fallback!.config?.context_window : undefined),
    maxOutputTokens: (typeof form.values.max_output_tokens === 'number' ? form.values.max_output_tokens : undefined)
      ?? (isEdit ? fallback!.config?.max_output_tokens : undefined),
    capabilities: capabilities.value,
    reasoning: reasoning.value,
    existing: isEdit ? fallback!.config : undefined,
  })

  const payload: Record<string, unknown> = {
    type,
    model_id,
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
        const data = { ...payload, provider_id: props.id ?? fallback!.provider_id } as ModelsUpdateRequest
        if (modelUUID) {
          return updateModel({ id: modelUUID, data })
        }
        return updateModelByLegacyModelID({ modelId: fallback!.model_id ?? '', data })
      }
      return (async () => {
        const providerId = props.id ?? await props.ensureProviderId?.()
        if (!providerId) throw new Error('provider is missing')
        return createModel({ ...payload, provider_id: providerId })
      })()
    },
    {
      fallbackMessage: t('common.saveFailed'),
      onSuccess: () => {
        open.value = false
        if (!isEdit) emit('created', model_id)
      },
    },
  )
}

watch(open, async () => {
  if (!open.value) {
    // Keep the mode until the next open: the dialog is still playing its exit
    // animation here, and flipping it now flashes "Add model" on an edit dialog.
    editInfo.value = null
    emit('closed')
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
        max_output_tokens: config?.max_output_tokens,
      },
    })
    capabilities.value = capabilityFormFrom(config)
    reasoning.value = reasoningFormFrom(config)
    showMoreReasoning.value = false
  } else {
    form.resetForm({
      values: {
        type: props.defaultType,
        model_id: '',
        name: '',
        description: '',
        dimensions: undefined,
        context_window: undefined,
        max_output_tokens: undefined,
      },
    })
    capabilities.value = defaultCapabilityForm()
    reasoning.value = defaultReasoningForm()
    showMoreReasoning.value = false
  }
}, {
  immediate: true,
})
</script>
