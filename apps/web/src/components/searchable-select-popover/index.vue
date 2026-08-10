<template>
  <Popover v-model:open="open">
    <PopoverTrigger as-child>
      <slot
        name="trigger"
        :open="open"
        :display-label="displayLabel"
        :selected-option="selectedOption"
        :placeholder="placeholder"
      >
        <button
          data-slot="select-trigger"
          data-size="default"
          :data-placeholder="displayLabel ? undefined : ''"
          type="button"
          :aria-expanded="open"
          :aria-label="ariaLabel || placeholder"
          :class="[selectTriggerClass, 'w-full']"
        >
          <span class="line-clamp-1">{{ displayLabel || placeholder }}</span>
          <ChevronsUpDown class="opacity-50" />
        </button>
      </slot>
    </PopoverTrigger>
    <PopoverContent
      menu
      :align="popoverAlign"
      :align-offset="alignOffset"
      :class="popoverClass || 'p-0'"
      :style="{ minWidth: `calc(var(--reka-popover-trigger-width) * ${widthRatio} + ${-menuAlignOffset * 2}px)` }"
    >
      <div :class="menuChromeClass">
        <div :class="menuSearchHeaderClass">
          <input
            v-model="searchTerm"
            role="combobox"
            :aria-controls="listboxId"
            :aria-expanded="open"
            :aria-activedescendant="activeIndex >= 0 ? `${listboxId}-${activeIndex}` : undefined"
            :placeholder="searchPlaceholder"
            :aria-label="searchAriaLabel"
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
            {{ emptyText }}
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
                <slot
                  name="group-label"
                  :group="vRow.row.group"
                >
                  {{ vRow.row.group.label }}
                </slot>
              </div>

              <button
                v-else
                :id="`${listboxId}-${vRow.virtual.index}`"
                type="button"
                role="option"
                :aria-selected="selected === vRow.row.option.value"
                :aria-setsize="optionCount"
                :aria-posinset="vRow.row.posinset"
                :data-highlighted="activeIndex === vRow.virtual.index ? '' : undefined"
                :class="menuItemClass"
                @click="selectOption(vRow.row.option.value)"
                @pointermove="activeIndex = vRow.virtual.index"
              >
                <slot
                  name="option-icon"
                  :option="vRow.row.option"
                />
                <slot
                  name="option-label"
                  :option="vRow.row.option"
                >
                  <span
                    class="min-w-0 flex-1 truncate text-left"
                    :title="vRow.row.option.label"
                  >{{ vRow.row.option.label }}</span>
                </slot>
                <Check
                  v-if="selected === vRow.row.option.value"
                  class="ml-auto size-4 shrink-0"
                />
              </button>
            </div>
          </div>
        </div>
      </div>
    </PopoverContent>
  </Popover>
</template>

<script setup lang="ts">
import { Check, ChevronsUpDown } from 'lucide-vue-next'
import {
  menuItemClass,
  menuAlignOffset,
  menuChromeClass,
  menuLabelClass,
  menuSearchHeaderClass,
  menuSearchInputClass,
  Popover,
  PopoverTrigger,
  PopoverContent,
  selectTriggerClass,
  virtualListboxClass,
} from '@felinic/ui'
import { computed, nextTick, ref, useId, watch } from 'vue'
import { useVirtualizer } from '@tanstack/vue-virtual'
import { useListboxKeyboard } from '@/composables/useListboxKeyboard'
import { measureVirtualRow } from '@/utils/virtual-row-measure'

export interface SearchableSelectOption {
  value: string
  label: string
  description?: string
  group?: string
  groupLabel?: string
  keywords?: string[]
  meta?: unknown
}

interface SearchableSelectGroup {
  key: string
  label: string
  items: SearchableSelectOption[]
}

interface HeaderRow {
  type: 'header'
  key: string
  group: SearchableSelectGroup
}

interface ItemRow {
  type: 'item'
  key: string
  option: SearchableSelectOption
  // 1-based position among option rows (excludes headers), for aria-posinset
  posinset: number
}

type Row = HeaderRow | ItemRow

const props = withDefaults(defineProps<{
  options: SearchableSelectOption[]
  placeholder?: string
  ariaLabel?: string
  searchPlaceholder?: string
  searchAriaLabel?: string
  emptyText?: string
  showGroupHeaders?: boolean
  widthRatio?: number
  popoverClass?: string
  popoverAlign?: 'start' | 'center' | 'end'
  alignOffset?: number
}>(), {
  placeholder: '',
  ariaLabel: '',
  searchPlaceholder: 'Search...',
  searchAriaLabel: 'Search options',
  emptyText: 'No results.',
  showGroupHeaders: true,
  widthRatio: 1,
  popoverAlign: 'start',
  // Default to text-align: shift the panel so its first row's text lands under
  // the trigger's text (see menu.ts → menuAlignOffset). Long-content surfaces
  // that widen the panel past the trigger pass `:align-offset="0"` to align the
  // box edge instead.
  alignOffset: menuAlignOffset,
})

const selected = defineModel<string>({ default: '' })
const searchTerm = ref('')
const open = ref(false)
const scrollEl = ref<HTMLElement | null>(null)

const selectedOption = computed(() =>
  props.options.find((option) => option.value === selected.value),
)

const displayLabel = computed(() =>
  selectedOption.value?.label ?? selected.value,
)

const filteredOptions = computed(() => {
  const keyword = searchTerm.value.trim().toLowerCase()
  if (!keyword) {
    return props.options
  }
  return props.options.filter((option) => {
    const terms = [option.label, option.description, ...(option.keywords ?? [])]
      .filter((term): term is string => Boolean(term))
      .join(' ')
      .toLowerCase()
    return terms.includes(keyword)
  })
})

const filteredGroups = computed<SearchableSelectGroup[]>(() => {
  const groups = new Map<string, SearchableSelectGroup>()
  for (const option of filteredOptions.value) {
    const key = option.group ?? '__ungrouped__'
    if (!groups.has(key)) {
      groups.set(key, {
        key,
        label: option.groupLabel ?? option.group ?? '',
        items: [],
      })
    }
    groups.get(key)!.items.push(option)
  }
  return Array.from(groups.values())
})

// Flatten groups into a single list so the dropdown can be virtualized:
// some consumers (e.g. timezone-select) feed hundreds of options, and
// rendering them all at once on open janks the main thread. Row heights are
// measured at runtime, so nothing here is hard-coded.
const rows = computed<Row[]>(() => {
  const result: Row[] = []
  let posinset = 0
  for (const group of filteredGroups.value) {
    if (props.showGroupHeaders && group.label) {
      result.push({ type: 'header', key: `header:${group.key}`, group })
    }
    for (const option of group.items) {
      posinset += 1
      result.push({ type: 'item', key: option.value, option, posinset })
    }
  }
  return result
})

// Total option count (excludes headers) for aria-setsize: virtualization drops
// off-screen options from the DOM, so screen readers need the real set size.
const optionCount = computed(() => filteredOptions.value.length)

const virtualizer = useVirtualizer<HTMLElement, HTMLElement>(
  computed(() => ({
    count: rows.value.length,
    getScrollElement: () => scrollEl.value,
    // Rows are single-line (~32px); this seed keeps the scroll size close to
    // real so the scrollbar tracks. measureRow measures the true height at
    // runtime, so an off estimate only causes minor jitter, never misalignment.
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

const listboxId = useId()
const { activeIndex, onKeydown, reset: resetActive } = useListboxKeyboard<Row>({
  rows,
  scrollToIndex: (index) => virtualizer.value.scrollToIndex(index),
  onSelect: (row) => {
    if (row.type === 'item') selectOption(row.option.value)
  },
})

watch(open, (value) => {
  if (value) {
    searchTerm.value = ''
    resetActive()
    nextTick(() => virtualizer.value.scrollToOffset(0))
  }
})

watch(searchTerm, () => virtualizer.value.scrollToOffset(0))

function selectOption(value: string) {
  selected.value = value
  open.value = false
}
</script>
