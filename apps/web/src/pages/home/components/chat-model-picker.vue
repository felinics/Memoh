<template>
  <Popover v-model:open="reasoningOpen">
    <div class="flex flex-col">
      <!-- Search: no leading glyph — the placeholder already says what to do,
           and a magnifier on a 1-row field is decoration that eats width. -->
      <div
        class="flex h-9 shrink-0 items-center gap-2 border-b border-border/40 px-3"
        @pointerenter="reasoningOpen = false"
      >
        <input
          v-model="searchTerm"
          role="combobox"
          :placeholder="$t('bots.settings.searchModel')"
          aria-label="Search models"
          class="flex h-full w-full bg-transparent text-control outline-hidden placeholder:text-muted-foreground"
        >
        <button
          v-if="searchTerm"
          type="button"
          class="shrink-0 text-muted-foreground hover:text-foreground"
          :aria-label="$t('common.clear')"
          @click="searchTerm = ''"
        >
          <X class="size-3.5" />
        </button>
      </div>

      <div
        ref="scrollHost"
        class="relative"
        @pointerenter="reasoningOpen = false"
      >
        <ScrollArea
          class="composer-model-list"
          :style="{ height: `${listHeight}px` }"
        >
          <section
            v-if="rows.length === 0"
            class="py-6 text-center text-body text-muted-foreground"
          >
            {{ $t('bots.settings.noModel') }}
          </section>

          <section
            v-else
            :style="{ height: `${totalSize}px`, width: '100%', position: 'relative' }"
          >
            <div
              v-for="vRow in virtualRows"
              :key="vRow.key"
              :ref="measureRow"
              :data-index="vRow.virtual.index"
              class="absolute left-1 right-1 top-0 py-px"
              :style="{ transform: `translateY(${vRow.virtual.start}px)` }"
            >
              <div
                v-if="vRow.row.type === 'header'"
                class="px-3 pt-1.5 pb-0.5 text-caption font-medium text-muted-foreground"
              >
                {{ vRow.row.label }}
              </div>

              <div
                v-else
                class="group/row relative flex items-center gap-1 rounded-md px-1 transition-colors duration-75"
                :class="modelValue === vRow.row.option.value ? 'bg-[var(--overlay-hover)]' : 'hover:bg-[var(--overlay-hover-light)]'"
              >
                <ModelDescriptionTooltip
                  :description="vRow.row.option.description"
                  :open="openDescriptionTooltipKey === vRow.row.key"
                  @update:open="setDescriptionTooltipOpen(vRow.row.key, $event)"
                >
                  <button
                    type="button"
                    class="flex min-w-0 flex-1 items-center gap-2 rounded-md px-2 py-1.5 text-left text-control"
                    :class="modelValue === vRow.row.option.value ? 'font-medium text-foreground' : 'text-foreground'"
                    @click="commitModel(vRow.row.option.value)"
                  >
                    <span class="min-w-0 flex-1 truncate">{{ vRow.row.option.label }}</span>
                  </button>
                </ModelDescriptionTooltip>

                <div class="flex shrink-0 items-center gap-1 pr-1">
                  <Check
                    v-if="modelValue === vRow.row.option.value"
                    class="size-3.5 shrink-0 text-muted-foreground"
                  />
                </div>
              </div>
            </div>
          </section>
        </ScrollArea>
      </div>

      <div class="border-t border-border px-1 py-1">
        <PopoverAnchor as-child>
          <button
            type="button"
            :aria-label="$t('chat.reasoningEffort')"
            class="flex w-full items-center gap-1 rounded-md px-1 transition-colors hover:bg-[var(--overlay-hover-light)] disabled:pointer-events-none disabled:opacity-40"
            :disabled="!canSelectReasoning"
            @pointerenter="reasoningOpen = true"
            @click="reasoningOpen = true"
          >
            <span class="flex min-w-0 flex-1 items-center gap-2 rounded-md px-2 py-1.5 text-left text-control">
              <Lightbulb
                class="size-3.5 shrink-0 text-muted-foreground"
                :style="{ opacity: EFFORT_OPACITY[currentReasoningValue] ?? 0.5 }"
              />
              <span class="min-w-0 flex-1 truncate text-foreground">{{ currentReasoningLabel || $t(currentReasoningLabelKey) }}</span>
            </span>
            <span class="flex shrink-0 items-center gap-1 pr-1">
              <ChevronRight class="size-3.5 shrink-0 text-muted-foreground" />
            </span>
          </button>
        </PopoverAnchor>
      </div>
    </div>

    <PopoverContent
      side="right"
      align="start"
      :side-offset="12"
      :align-offset="-4"
      :align-flip="false"
      :collision-padding="8"
      class="w-44 p-1"
      @open-auto-focus.prevent
    >
      <div
        ref="reasoningScrollHost"
        class="reasoning-effort-list"
        :style="{ height: `${reasoningMenuHeight}px` }"
      >
        <ScrollArea class="h-full">
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
                class="flex w-full items-center justify-between gap-2 rounded-md px-2 py-1.5 text-left text-control transition-colors hover:bg-[var(--overlay-hover-light)]"
                :class="selectedReasoningValue === option.value ? 'font-medium text-foreground' : 'text-foreground'"
                @click="setEffort(option.value)"
              >
                <Lightbulb
                  class="size-3.5 shrink-0 text-muted-foreground"
                  :style="{ opacity: EFFORT_OPACITY[option.value] ?? 0.5 }"
                />
                <span class="min-w-0 flex-1 truncate">{{ option.label || $t(option.labelKey ?? 'chat.reasoningOff') }}</span>
                <Check
                  v-if="selectedReasoningValue === option.value"
                  class="size-3.5 shrink-0 text-muted-foreground"
                />
              </button>
            </ModelDescriptionTooltip>
          </div>
        </ScrollArea>
      </div>
    </PopoverContent>
  </Popover>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useVirtualizer } from '@tanstack/vue-virtual'
import { useEventListener } from '@vueuse/core'
import { ChevronRight, X, Check, Lightbulb } from 'lucide-vue-next'
import { Popover, PopoverAnchor, PopoverContent, ScrollArea } from '@felinic/ui'
import type { ModelsGetResponse, ProvidersGetResponse } from '@memohai/sdk'
import {
  REASONING_EFFORT_DISABLE,
  EFFORT_LABELS,
  EFFORT_OPACITY,
  resolveThinkingMode,
  resolveEffortLevels,
  availableEffortsForMode,
} from '@/pages/bots/components/reasoning-effort'
import ModelDescriptionTooltip from '@/components/model-description-tooltip/index.vue'
import { getModelDescription, matchesModelSearch } from '@/utils/model-description'

const props = defineProps<{
  models: ModelsGetResponse[]
  providers: ProvidersGetResponse[]
  modelType: 'chat' | 'embedding'
  open?: boolean
  reasoningOptions?: Array<{
    value: string
    label: string
    description?: string
  }>
}>()

const modelValue = defineModel<string>({ default: '' })
const reasoningEffort = defineModel<string>('reasoningEffort', { default: '' })

const searchTerm = ref('')
const scrollHost = ref<HTMLElement | null>(null)
const reasoningScrollHost = ref<HTMLElement | null>(null)
const reasoningOpen = ref(false)
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

interface ModelOption {
  value: string
  label: string
  modelId: string
  description?: string
  groupKey: string
  groupLabel: string
  config: ModelsGetResponse['config']
  providerId: string
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
}

type Row = HeaderRow | ItemRow

const options = computed<ModelOption[]>(() =>
  typeFilteredModels.value.map((model) => {
    const providerId = model.provider_id ?? ''
    return {
      value: model.id || model.model_id || '',
      label: model.name || model.model_id || '',
      modelId: model.model_id || '',
      description: getModelDescription(model.config),
      groupKey: providerId,
      groupLabel: providerMap.value.get(providerId) ?? providerId,
      config: model.config,
      providerId,
    }
  }),
)

const filteredGroups = computed(() => {
  const keyword = searchTerm.value.trim().toLowerCase()
  const filtered = keyword
    ? options.value.filter(opt => matchesModelSearch(keyword, [opt.label, opt.modelId, opt.description]))
    : options.value

  const groups = new Map<string, { key: string; label: string; items: ModelOption[] }>()
  for (const opt of filtered) {
    if (!groups.has(opt.groupKey)) {
      groups.set(opt.groupKey, { key: opt.groupKey, label: opt.groupLabel, items: [] })
    }
    groups.get(opt.groupKey)!.items.push(opt)
  }

  // Float the model that was active when this menu opened. During one open
  // interaction, changing Options can update the selection without reordering.
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

// The provider list can contain hundreds of models. Flatten headers + options
// into one virtualized list so opening the picker only mounts visible rows.
const rows = computed<Row[]>(() => {
  const result: Row[] = []
  for (const group of filteredGroups.value) {
    if (group.label) {
      result.push({ type: 'header', key: `header:${group.key}`, label: group.label })
    }
    for (const option of group.items) {
      result.push({ type: 'item', key: option.value, option })
    }
  }
  return result
})

const scrollViewport = computed(() =>
  scrollHost.value?.querySelector<HTMLElement>('[data-slot="scroll-area-viewport"]') ?? null,
)

const virtualizer = useVirtualizer<HTMLElement, HTMLElement>(
  computed(() => ({
    count: rows.value.length,
    getScrollElement: () => scrollViewport.value,
    estimateSize: (index) => {
      const row = rows.value[index]
      if (!row) return 36
      return row.type === 'header' ? 28 : 38
    },
    overscan: 8,
    getItemKey: (index: number) => rows.value[index]?.key ?? index,
  })),
)

const totalSize = computed(() => virtualizer.value.getTotalSize())
const listHeight = computed(() => {
  if (rows.value.length === 0) return 96
  return Math.min(320, Math.max(36, totalSize.value))
})

const virtualRows = computed(() =>
  virtualizer.value.getVirtualItems().flatMap((vi) => {
    const row = rows.value[vi.index]
    return row ? [{ key: String(vi.key), virtual: vi, row }] : []
  }),
)

const measureRow = (el: unknown) => {
  if (el instanceof HTMLElement) virtualizer.value.measureElement(el)
}

function handleListScroll() {
  reasoningOpen.value = false
  openDescriptionTooltipKey.value = null
}

useEventListener(scrollViewport, 'scroll', handleListScroll, { passive: true })

function setDescriptionTooltipOpen(key: string, open: boolean) {
  if (open) {
    openDescriptionTooltipKey.value = key
  } else if (openDescriptionTooltipKey.value === key) {
    openDescriptionTooltipKey.value = null
  }
}

const activeModel = computed(() =>
  options.value.find((o) => o.value === modelValue.value),
)

const activeClientType = computed(() =>
  props.providers.find((p) => p.id === activeModel.value?.providerId)?.client_type,
)

const nativeAvailableEfforts = computed(() => {
  if (!activeModel.value) return []
  return availableEffortsForMode(
    resolveThinkingMode(activeModel.value.config),
    resolveEffortLevels(activeModel.value.config, activeClientType.value),
  )
})

interface ReasoningOption {
  value: string
  label?: string
  labelKey?: string
  description?: string
}

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

const reasoningMenuHeight = computed(() =>
  Math.min(288, Math.max(34, availableReasoningOptions.value.length * 34)),
)

const reasoningScrollViewport = computed(() =>
  reasoningScrollHost.value?.querySelector<HTMLElement>('[data-slot="scroll-area-viewport"]') ?? null,
)

useEventListener(reasoningScrollViewport, 'scroll', () => {
  openDescriptionTooltipKey.value = null
}, { passive: true })

const canSelectReasoning = computed(() =>
  availableReasoningOptions.value.length > 0,
)

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

// Selection applies immediately and the popover STAYS OPEN on purpose (#899):
// the user can hop between models (and efforts) to compare without reopening
// the menu. Dismissal is the popover's own business — outside click / Esc.
function commitModel(value: string) {
  reasoningOpen.value = false
  if (value !== modelValue.value) modelValue.value = value
}

function setEffort(level: string) {
  // Keep the effort flyout open after a pick (#899): the check mark just
  // moves. Collapsing on every click made a shaky double-click land on the
  // backdrop and dismiss the whole popover.
  reasoningEffort.value = level
}

watch(() => props.open, (v) => {
  if (v) {
    searchTerm.value = ''
    pinnedSortValue.value = modelValue.value
    openDescriptionTooltipKey.value = null
    nextTick(() => {
      virtualizer.value.scrollToOffset(0)
    })
  } else {
    reasoningOpen.value = false
    openDescriptionTooltipKey.value = null
  }
}, { immediate: true })

watch(searchTerm, () => {
  reasoningOpen.value = false
  openDescriptionTooltipKey.value = null
  nextTick(() => {
    virtualizer.value.scrollToOffset(0)
  })
})
</script>
