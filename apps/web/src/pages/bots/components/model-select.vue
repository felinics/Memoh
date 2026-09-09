<template>
  <Popover v-model:open="open">
    <PopoverTrigger as-child>
      <button
        data-slot="select-trigger"
        data-size="default"
        :data-placeholder="displayLabel ? undefined : ''"
        type="button"
        :aria-expanded="open"
        :aria-label="placeholder || 'Select model'"
        :class="[selectTriggerClass, 'w-full']"
      >
        <span
          class="line-clamp-1"
          :title="displayLabel || placeholder"
        >
          {{ displayLabel || placeholder }}
        </span>
        <ChevronsUpDown class="opacity-50" />
      </button>
    </PopoverTrigger>
    <PopoverContent
      menu
      align="end"
      class="min-w-[var(--reka-popover-trigger-width)] w-80 p-0"
    >
      <div :class="menuChromeClass">
        <ModelOptions
          v-model="selected"
          v-model:reasoning-effort="reasoningEffort"
          :models="models"
          :providers="providers"
          :model-type="modelType"
          :open="open"
          :none-label="noneLabel"
          :show-reasoning="showReasoning"
          :reasoning-options="reasoningOptions"
        />
      </div>
    </PopoverContent>
  </Popover>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ChevronsUpDown } from 'lucide-vue-next'
import { Popover, PopoverTrigger, PopoverContent, menuChromeClass, selectTriggerClass } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import type { ModelsGetResponse, ModelsModelType, ProvidersGetResponse } from '@memohai/sdk'
import ModelOptions from './model-options.vue'
import { EFFORT_LABELS, REASONING_EFFORT_DISABLE } from './reasoning-effort'

const props = defineProps<{
  models: ModelsGetResponse[]
  providers: ProvidersGetResponse[]
  modelType: ModelsModelType
  placeholder?: string
  noneLabel?: string
  showReasoning?: boolean
  // Runtime-supplied tiers, forwarded to ModelOptions. Callers whose efforts do
  // not come from the model's own capabilities (ACP agents report their own)
  // pass them here instead of letting the menu derive them.
  reasoningOptions?: Array<{
    value: string
    label: string
    description?: string
  }>
}>()

const selected = defineModel<string>({ default: '' })
const reasoningEffort = defineModel<string>('reasoningEffort', { default: '' })
const open = ref(false)

// Picking a model closes the menu; picking an effort does not — the effort
// flyout is a sub-menu of the same decision, so collapsing on every tier tap
// would force a reopen to compare tiers.
watch(selected, () => {
  open.value = false
})

const { t } = useI18n()

const modelLabel = computed(() => {
  const model = props.models.find((m) => (m.id || m.model_id) === selected.value)
  return model?.name || model?.model_id || selected.value
})

// The effort moved inside this menu, so the trigger has to carry it — otherwise
// the value is invisible until the menu is reopened. "Off" is not appended: a
// bare model name already reads as "no reasoning".
const displayLabel = computed(() => {
  if (!props.showReasoning || !modelLabel.value) return modelLabel.value
  const effort = reasoningEffort.value
  if (!effort || effort === REASONING_EFFORT_DISABLE) return modelLabel.value
  if (props.reasoningOptions) {
    const option = props.reasoningOptions.find(o => o.value === effort)
    return option ? `${modelLabel.value} · ${option.label}` : modelLabel.value
  }
  const key = EFFORT_LABELS[effort]
  return `${modelLabel.value} · ${key ? t(key) : effort}`
})
</script>
