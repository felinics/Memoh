<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useElementBounding, useWindowSize } from '@vueuse/core'
import { Check } from 'lucide-vue-next'
import {
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  Spinner,
} from '@felinic/ui'
import type { ModelsGetResponse, ProvidersGetResponse } from '@memohai/sdk'
import ModelOptions from '@/pages/bots/components/model-options.vue'
import ModelDescriptionTooltip from '@/components/model-description-tooltip/index.vue'
import { useIsMobile } from '@/composables/useIsMobile'
import { EFFORT_LABELS, displayedEffort, selectableEfforts } from '@/pages/bots/components/reasoning-effort'

const props = defineProps<{
  modelValue: string
  modelLabel: string
  defaultModelId: string
  reasoningEffort: string
  reasoningOptions?: Array<{ value: string, label: string, description?: string }>
  models: ModelsGetResponse[]
  providers: ProvidersGetResponse[]
  noneLabel?: string
  showReasoning: boolean
  loading: boolean
  error?: string
  authRequired: boolean
}>()
const emit = defineEmits<{
  'update:modelValue': [value: string]
  'update:reasoningEffort': [value: string]
  retry: []
  settings: []
}>()
const { t } = useI18n()
const modelOpen = ref(false)
const reasoningOpen = ref(false)
const isMobile = useIsMobile()
const modelLabelElement = ref<HTMLElement>()
const modelTriggerElement = computed(() => modelLabelElement.value?.closest<HTMLElement>('[role="menuitem"]'))
const { right: menuRight } = useElementBounding(modelTriggerElement)
const { width: viewportWidth } = useWindowSize()
// Below the shared menu's 10rem baseline, keep a readable surface and let
// Reka move it into the viewport instead of squeezing it into the right gutter.
const needsSubmenuFallback = computed(() => {
  if (!modelTriggerElement.value) return false
  const rootRem = Number.parseFloat(getComputedStyle(document.documentElement).fontSize)
  return viewportWidth.value - menuRight.value - 8 < 10 * rootRem
})
const modelOptions = ref<InstanceType<typeof ModelOptions>>()
const activeModel = computed(() => {
  const id = props.modelValue || props.defaultModelId
  return props.models.find(model => (model.id || model.model_id) === id)
})
const efforts = computed(() => props.reasoningOptions !== undefined
  ? props.reasoningOptions.map(option => {
      const value = option.value.trim()
      return { value, label: option.label.trim() || value, description: option.description?.trim() }
    })
  : selectableEfforts(activeModel.value?.reasoning).map(value => ({
      value,
      label: t(EFFORT_LABELS[value] ?? 'chat.reasoningOff'),
      description: undefined,
    })))
const currentEffort = computed(() => props.reasoningOptions !== undefined
  ? props.reasoningEffort
  : displayedEffort(props.reasoningEffort, activeModel.value?.reasoning))
const effortLabel = computed(() => efforts.value.find(option => option.value === currentEffort.value)?.label
  || (currentEffort.value ? t(EFFORT_LABELS[currentEffort.value] ?? 'chat.modelDefault') : t('chat.modelDefault')))

function focusModelSearch() {
  void nextTick(() => modelOptions.value?.focusSearch())
}

function selectModel(value: string) {
  emit('update:modelValue', value)
  if (isMobile.value) modelOpen.value = false
}

function selectReasoning(value: string) {
  emit('update:reasoningEffort', value)
  if (isMobile.value) reasoningOpen.value = false
}

function onModelKeydown(event: KeyboardEvent) {
  // The listbox owns search, caret movement and virtual row navigation. Let
  // Escape/Tab reach the containing menu so dismissal still uses its contract.
  const backToParent = event.key === 'ArrowLeft' && !(event.target instanceof HTMLInputElement)
  if (event.key !== 'Escape' && event.key !== 'Tab' && !backToParent) event.stopPropagation()
}
</script>

<template>
  <DropdownMenuSub v-model:open="modelOpen">
    <DropdownMenuSubTrigger>
      <span ref="modelLabelElement">{{ t('chat.modelOverride') }}</span>
      <span
        class="ml-auto min-w-0 flex-1 truncate text-right text-muted-foreground"
      >{{ modelLabel }}</span>
    </DropdownMenuSubTrigger>
    <DropdownMenuSubContent
      position-strategy="absolute"
      :scrollable="false"
      :collision-padding="8"
      :avoid-collisions="needsSubmenuFallback"
      :prioritize-position="needsSubmenuFallback"
      class="w-80 min-w-0"
      :class="needsSubmenuFallback ? 'max-w-[calc(100vw-1rem)]' : 'max-w-(--reka-dropdown-menu-content-available-width)'"
      @entry-focus.prevent="focusModelSearch"
    >
      <DropdownMenuItem
        v-if="loading"
        disabled
      >
        <Spinner />
        {{ t('common.loading') }}
      </DropdownMenuItem>
      <template v-else-if="error">
        <DropdownMenuLabel class="whitespace-normal">
          {{ error }}
        </DropdownMenuLabel>
        <DropdownMenuItem @select.prevent="authRequired ? emit('settings') : emit('retry')">
          {{ t(authRequired ? 'bots.agent.openSettings' : 'common.retry') }}
        </DropdownMenuItem>
      </template>
      <div
        v-else
        class="contents"
        @keydown="onModelKeydown"
      >
        <ModelOptions
          ref="modelOptions"
          :model-value="modelValue"
          :models="models"
          :providers="providers"
          :none-label="noneLabel"
          model-type="chat"
          :open="modelOpen"
          @select="selectModel"
        />
      </div>
    </DropdownMenuSubContent>
  </DropdownMenuSub>
  <DropdownMenuSub
    v-if="showReasoning && efforts.length"
    v-model:open="reasoningOpen"
  >
    <DropdownMenuSubTrigger :disabled="loading || !!error || !efforts.length">
      <span>{{ t('chat.reasoningEffort') }}</span>
      <span
        class="ml-auto min-w-0 flex-1 truncate text-right text-muted-foreground"
      >{{ effortLabel }}</span>
    </DropdownMenuSubTrigger>
    <DropdownMenuSubContent
      position-strategy="absolute"
      :collision-padding="8"
      :avoid-collisions="needsSubmenuFallback"
      :prioritize-position="needsSubmenuFallback"
      class="min-w-0 w-40"
      :class="needsSubmenuFallback ? 'max-w-[calc(100vw-1rem)]' : 'max-w-(--reka-dropdown-menu-content-available-width)'"
    >
      <ModelDescriptionTooltip
        v-for="option in efforts"
        :key="option.value"
        :description="option.description"
        side="right"
        :side-offset="12"
      >
        <DropdownMenuItem @select.prevent="selectReasoning(option.value)">
          <span class="min-w-0 flex-1 truncate">{{ option.label }}</span>
          <Check v-if="currentEffort === option.value" />
        </DropdownMenuItem>
      </ModelDescriptionTooltip>
    </DropdownMenuSubContent>
  </DropdownMenuSub>
</template>
