<template>
  <!-- The Popover root is renderless (reka's PopoverRoot only provides context),
       so wrapping unconditionally costs nothing when the reasoning footer is off. -->
  <Popover v-model:open="reasoningOpen">
    <!-- No pointerenter closers on the search header or the listbox: with the
         flyout collision-shifted up beside the list (short windows), every
         diagonal path from the footer trigger to the flyout crossed one of
         those zones and the menu died mid-flight. The flyout now closes only
         on explicit actions — model pick, list scroll, typing, outside click
         / Esc (see commitModel / scroll listener / searchTerm watcher). -->
    <div :class="menuSearchHeaderClass">
      <input
        v-model="searchTerm"
        role="combobox"
        :aria-controls="listboxId"
        :aria-expanded="open"
        :aria-activedescendant="activeIndex >= 0 ? `${listboxId}-${activeIndex}` : undefined"
        :placeholder="$t('bots.settings.searchModel')"
        aria-label="Search models"
        :class="menuSearchInputClass"
        @keydown="onKeydown"
      >
    </div>

    <div
      :id="listboxId"
      ref="scrollEl"
      :class="virtualListboxClass"
      role="listbox"
    >
      <div
        v-if="rows.length === 0"
        class="py-6 text-center text-control text-muted-foreground"
      >
        {{ $t('bots.settings.noModel') }}
      </div>

      <div
        v-else
        :style="{ height: `${totalSize}px`, width: '100%', position: 'relative' }"
      >
        <div
          v-for="vRow in virtualRows"
          :key="vRow.key"
          :ref="measureRow"
          :data-index="vRow.virtual.index"
          :style="{ position: 'absolute', top: '0', left: '0', width: '100%', transform: `translateY(${vRow.virtual.start}px)` }"
        >
          <div
            v-if="vRow.row.type === 'header'"
            :class="menuLabelClass"
          >
            {{ vRow.row.label }}
          </div>

          <ModelDescriptionTooltip
            v-else
            :description="vRow.row.option.description"
            :open="openDescriptionTooltipKey === vRow.row.key"
            @update:open="setDescriptionTooltipOpen(vRow.row.key, $event)"
          >
            <button
              :id="`${listboxId}-${vRow.virtual.index}`"
              type="button"
              role="option"
              :aria-selected="modelValue === vRow.row.option.value"
              :aria-setsize="optionCount"
              :aria-posinset="vRow.row.posinset"
              :data-highlighted="activeIndex === vRow.virtual.index ? '' : undefined"
              :class="menuItemClass"
              @click="commitModel(vRow.row.option.value)"
              @pointermove="activeIndex = vRow.virtual.index"
            >
              <span
                class="min-w-0 flex-1 truncate text-left"
                :title="vRow.row.option.label"
              >{{ vRow.row.option.label }}</span>
              <Check
                v-if="modelValue === vRow.row.option.value"
                class="ml-2 size-4 shrink-0"
              />
            </button>
          </ModelDescriptionTooltip>
        </div>
      </div>
    </div>

    <!-- Reasoning effort lives in the model menu, not beside it: the tiers a model
         offers depend on the model, so picking one without the other is a two-stop
         trip through a menu that already knows the answer. -->
    <template v-if="showReasoning">
      <div :class="menuSeparatorClass" />
      <div class="p-1.5 pt-0">
        <PopoverAnchor as-child>
          <!-- menuItemClass highlights only via [data-highlighted] (the reka
               Menu contract); these plain buttons must drive it themselves,
               same as the list rows do via activeIndex. -->
          <button
            type="button"
            :aria-label="$t('chat.reasoningEffort')"
            :class="menuItemClass"
            :data-disabled="canSelectReasoning ? undefined : ''"
            :data-highlighted="reasoningTriggerHover ? '' : undefined"
            :disabled="!canSelectReasoning"
            @pointerenter="reasoningTriggerHover = true; reasoningOpen = true"
            @pointerleave="reasoningTriggerHover = false"
            @click="reasoningOpen = true"
          >
            <Lightbulb :style="{ opacity: EFFORT_OPACITY[currentReasoningValue] ?? 0.5 }" />
            <span class="min-w-0 flex-1 truncate text-left">{{ currentReasoningLabel || $t(currentReasoningLabelKey) }}</span>
            <ChevronRight class="ml-2" />
          </button>
        </PopoverAnchor>
      </div>
    </template>

    <PopoverContent
      v-if="showReasoning"
      menu
      side="right"
      align="start"
      :side-offset="12"
      :align-offset="-4"
      :align-flip="false"
      :collision-padding="8"
      class="w-44"
      @open-auto-focus.prevent
    >
      <div
        :class="menuChromeClass"
        @pointerleave="reasoningHoverValue = null"
      >
        <div
          ref="reasoningScrollEl"
          :class="virtualListboxClass"
        >
          <div class="flex flex-col gap-0.5">
            <ModelDescriptionTooltip
              v-for="option in availableReasoningOptions"
              :key="option.value"
              :description="option.description"
              :open="openDescriptionTooltipKey === `reasoning:${option.value}`"
              @update:open="setDescriptionTooltipOpen(`reasoning:${option.value}`, $event)"
            >
              <button
                type="button"
                :class="menuItemClass"
                :data-highlighted="reasoningHoverValue === option.value ? '' : undefined"
                @pointermove="reasoningHoverValue = option.value"
                @click="setEffort(option.value)"
              >
                <Lightbulb :style="{ opacity: EFFORT_OPACITY[option.value] ?? 0.5 }" />
                <span class="min-w-0 flex-1 truncate text-left">{{ option.label || $t(option.labelKey ?? 'chat.reasoningOff') }}</span>
                <Check
                  v-if="selectedReasoningValue === option.value"
                  class="ml-2 size-4 shrink-0"
                />
              </button>
            </ModelDescriptionTooltip>
          </div>
        </div>
      </div>
    </PopoverContent>
  </Popover>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from 'vue'
import { useVirtualizer } from '@tanstack/vue-virtual'
import { useEventListener } from '@vueuse/core'
import { Check, ChevronRight, Lightbulb } from 'lucide-vue-next'
import {
  Popover,
  PopoverAnchor,
  PopoverContent,
  menuChromeClass,
  menuItemClass,
  menuLabelClass,
  menuSearchHeaderClass,
  menuSearchInputClass,
  menuSeparatorClass,
  virtualListboxClass,
} from '@felinic/ui'
import type { ModelsGetResponse, ModelsModelType, ProvidersGetResponse } from '@memohai/sdk'
import { useListboxKeyboard } from '@/composables/useListboxKeyboard'
import ModelDescriptionTooltip from '@/components/model-description-tooltip/index.vue'
import { getModelDescription, matchesModelSearch } from '@/utils/model-description'
import { measureVirtualRow } from '@/utils/virtual-row-measure'
import {
  EFFORT_LABELS,
  EFFORT_OPACITY,
  REASONING_EFFORT_DISABLE,
  selectableEfforts,
} from './reasoning-effort'

export interface ModelOption {
  value: string
  label: string
  modelId: string
  description?: string
  groupKey: string
  groupLabel: string
  keywords: string[]
  compatibilities?: string[]
  contextWindow?: number
  providerId: string
  config?: ModelsGetResponse['config']
  reasoning?: ModelsGetResponse['reasoning']
}

interface HeaderRow {
  type: 'header'
  key: string
  label: string
}

interface ItemRow {
  type: 'item'
  key: string
  option: ModelOption
  // 1-based position among option rows (excludes headers), for aria-posinset
  posinset: number
}

interface NoneRow {
  type: 'none'
  key: string
  option: ModelOption
  posinset: number
}

type Row = HeaderRow | ItemRow | NoneRow

interface ReasoningOption {
  value: string
  label?: string
  labelKey?: string
  description?: string
}

const props = defineProps<{
  models: ModelsGetResponse[]
  providers: ProvidersGetResponse[]
  modelType: ModelsModelType
  open?: boolean
  showTags?: boolean
  showIcons?: boolean
  noneLabel?: string
  // Opt in to the reasoning-effort footer. Off by default so the embedding/tts/…
  // pickers that share this component stay a plain model list.
  showReasoning?: boolean
  // Runtime-supplied tiers (ACP agents report their own). When omitted, the tiers
  // are derived from the selected model's capabilities.
  reasoningOptions?: Array<{
    value: string
    label: string
    description?: string
  }>
}>()

// No explicit defineEmits for update:modelValue — defineModel already declares
// it, and declaring both widens the emit signature to (...args: unknown[]),
// which breaks callers that bind :model-value + @update:model-value explicitly.
const modelValue = defineModel<string>({ default: '' })
const reasoningEffort = defineModel<string>('reasoningEffort', { default: '' })

const searchTerm = ref('')
const scrollEl = ref<HTMLElement | null>(null)
const reasoningScrollEl = ref<HTMLElement | null>(null)
const reasoningOpen = ref(false)
// Pointer-driven highlight state for the effort trigger and the flyout rows.
// Both are plain buttons wearing menuItemClass, whose highlight only renders
// through [data-highlighted]; nothing reka-side sets it here (unlike the list
// rows, which are driven by the keyboard composable's activeIndex). Reset when
// the flyout closes so a stale row never stays lit into the next open.
const reasoningTriggerHover = ref(false)
const reasoningHoverValue = ref<string | null>(null)
const openDescriptionTooltipKey = ref<string | null>(null)
// Sort order is captured when the picker opens. Changing models inside the same
// open menu must not make the list jump under the pointer.
const pinnedSortValue = ref('')

const providerMap = computed(() => {
  const map = new Map<string, string>()
  for (const p of props.providers) {
    if (p.id) map.set(p.id, p.name ?? p.id)
  }
  return map
})

const typeFilteredModels = computed(() =>
  props.models.filter((m) => m.type === props.modelType),
)

const options = computed<ModelOption[]>(() =>
  typeFilteredModels.value.map((model) => {
    const providerId = model.provider_id ?? ''
    const config = model.config as { compatibilities?: string[]; context_window?: number } | undefined
    return {
      value: model.id || model.model_id || '',
      label: model.name || model.model_id || '',
      modelId: model.model_id || '',
      description: getModelDescription(model.config),
      groupKey: providerId,
      groupLabel: providerMap.value.get(providerId) ?? providerId,
      keywords: [model.model_id ?? '', model.name ?? ''],
      compatibilities: config?.compatibilities,
      contextWindow: config?.context_window,
      providerId,
      config: model.config,
      reasoning: model.reasoning,
    }
  }),
)

const noneOption = computed<ModelOption | undefined>(() =>
  props.noneLabel
    ? {
        value: '',
        label: props.noneLabel,
        modelId: '',
        description: undefined,
        groupKey: '',
        groupLabel: '',
        keywords: [props.noneLabel],
        providerId: '',
      }
    : undefined,
)

const filteredOptions = computed(() => {
  const keyword = searchTerm.value.trim().toLowerCase()
  if (!keyword) return options.value
  return options.value.filter((opt) => {
    return matchesModelSearch(keyword, [opt.label, opt.modelId, opt.description, ...opt.keywords])
  })
})

const filteredGroups = computed(() => {
  const groups = new Map<string, { key: string; label: string; items: ModelOption[] }>()
  for (const opt of filteredOptions.value) {
    if (!groups.has(opt.groupKey)) {
      groups.set(opt.groupKey, { key: opt.groupKey, label: opt.groupLabel, items: [] })
    }
    groups.get(opt.groupKey)!.items.push(opt)
  }

  // Float the model that was active when this menu opened. During one open
  // interaction, changing the effort can update the selection without reordering.
  const list = Array.from(groups.values())
  const selected = options.value.find((o) => o.value === pinnedSortValue.value)
  if (!selected) return list
  list.sort((a, b) => Number(b.key === selected.groupKey) - Number(a.key === selected.groupKey))
  const activeGroup = list.find((g) => g.key === selected.groupKey)
  if (activeGroup) {
    activeGroup.items = [...activeGroup.items].sort(
      (a, b) => Number(b.value === selected.value) - Number(a.value === selected.value),
    )
  }
  return list
})

// Flatten the grouped options into one linear list so the dropdown can be
// virtualized: rendering every row at once instantiates hundreds of buttons
// (each with capability-icon sub-components) synchronously and freezes the
// main thread when a provider exposes hundreds of models. Heights are measured
// at runtime by the virtualizer, so no row heights are hard-coded here.
const rows = computed<Row[]>(() => {
  const result: Row[] = []
  let posinset = 0
  if (noneOption.value) {
    posinset += 1
    result.push({
      type: 'none',
      key: 'none',
      option: noneOption.value,
      posinset,
    })
  }
  for (const group of filteredGroups.value) {
    if (group.label) {
      result.push({ type: 'header', key: `header:${group.key}`, label: group.label })
    }
    for (const option of group.items) {
      posinset += 1
      result.push({
          type: 'item',
          key: option.value,
          option,
          posinset,
        })
    }
  }
  return result
})

// Total option count (excludes group headers) for aria-setsize: virtualization
// drops off-screen options from the DOM, so screen readers need this to know the
// real set size rather than only the rendered window.
const optionCount = computed(() => (noneOption.value ? 1 : 0) + filteredOptions.value.length)

const virtualizer = useVirtualizer<HTMLElement, HTMLElement>(
  computed(() => ({
    count: rows.value.length,
    getScrollElement: () => scrollEl.value,
    // Per-row size estimate, kept close to the real rendered heights so the
    // total scroll size barely shifts as rows get measured — otherwise the
    // scrollbar drifts (estimate vs. measured mismatch). These are only seeds:
    // measureRow measures the true height at runtime, so being slightly off
    // causes minor jitter at worst, never clipping/misalignment.
    estimateSize: () => 32,
    gap: 2,
    overscan: 8,
    getItemKey: (index: number) => rows.value[index]?.key ?? index,
    // Rows measured while hidden or mid-unmount report height 0; keep the
    // last real size so the virtualizer's offset never desyncs.
    measureElement: measureVirtualRow,
  })),
)

const totalSize = computed(() => virtualizer.value.getTotalSize())

const virtualRows = computed(() =>
  virtualizer.value.getVirtualItems().flatMap((vi) => {
    const row = rows.value[vi.index]
    return row ? [{ key: String(vi.key), virtual: vi, row }] : []
  }),
)

const measureRow = (el: unknown) => {
  if (el instanceof HTMLElement) virtualizer.value.measureElement(el)
}

// Selection applies immediately and the popover STAYS OPEN on purpose (#899):
// the user can hop between models (and efforts) to compare without reopening the
// menu. Dismissal is the host popover's business — outside click / Esc / its own
// watcher on the selected value.
function commitModel(value: string) {
  reasoningOpen.value = false
  if (value !== modelValue.value) modelValue.value = value
}

const listboxId = useId()
const { activeIndex, onKeydown, reset: resetActive } = useListboxKeyboard<Row>({
  rows,
  scrollToIndex: (index) => virtualizer.value.scrollToIndex(index),
  onSelect: (row) => {
    if (row.type === 'item' || row.type === 'none') commitModel(row.option.value)
  },
})

function setDescriptionTooltipOpen(key: string, open: boolean) {
  if (open) {
    openDescriptionTooltipKey.value = key
  } else if (openDescriptionTooltipKey.value === key) {
    openDescriptionTooltipKey.value = null
  }
}

useEventListener(scrollEl, 'scroll', () => {
  reasoningOpen.value = false
  openDescriptionTooltipKey.value = null
}, { passive: true })

useEventListener(reasoningScrollEl, 'scroll', () => {
  openDescriptionTooltipKey.value = null
}, { passive: true })

const activeModel = computed(() =>
  options.value.find((o) => o.value === modelValue.value),
)

const nativeAvailableEfforts = computed(() =>
  selectableEfforts(activeModel.value?.reasoning),
)

const availableReasoningOptions = computed<ReasoningOption[]>(() => {
  if (props.reasoningOptions !== undefined) {
    return props.reasoningOptions.flatMap((option) => {
      const value = option.value.trim()
      if (!value) return []
      return [{
        value,
        label: option.label.trim() || value,
        description: option.description?.trim() || undefined,
      }]
    })
  }
  return nativeAvailableEfforts.value.map(value => ({
    value,
    labelKey: EFFORT_LABELS[value] ?? 'chat.reasoningOff',
  }))
})

const canSelectReasoning = computed(() => availableReasoningOptions.value.length > 0)

const selectedReasoningValue = computed(() =>
  reasoningEffort.value || REASONING_EFFORT_DISABLE,
)

const currentReasoningValue = computed(() =>
  canSelectReasoning.value ? selectedReasoningValue.value : REASONING_EFFORT_DISABLE,
)

const currentReasoningOption = computed(() =>
  availableReasoningOptions.value.find(option => option.value === currentReasoningValue.value),
)

const currentReasoningLabel = computed(() => currentReasoningOption.value?.label ?? '')
const currentReasoningLabelKey = computed(() =>
  currentReasoningOption.value?.labelKey ?? EFFORT_LABELS[currentReasoningValue.value] ?? 'chat.reasoningOff',
)

function setEffort(level: string) {
  // Keep the effort flyout open after a pick (#899): the check mark just
  // moves. Collapsing on every click made a shaky double-click land on the
  // backdrop and dismiss the whole popover.
  reasoningEffort.value = level
}

watch(reasoningOpen, (open) => {
  if (!open) {
    reasoningTriggerHover.value = false
    reasoningHoverValue.value = null
  }
})

watch(() => props.open, (v) => {
  if (v) {
    searchTerm.value = ''
    pinnedSortValue.value = modelValue.value
    openDescriptionTooltipKey.value = null
    resetActive()
    nextTick(() => virtualizer.value.scrollToOffset(0))
  } else {
    reasoningOpen.value = false
    openDescriptionTooltipKey.value = null
  }
}, { immediate: true })

watch(searchTerm, () => {
  reasoningOpen.value = false
  openDescriptionTooltipKey.value = null
  virtualizer.value.scrollToOffset(0)
})
</script>
