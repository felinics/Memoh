<template>
  <!-- Grouped nav rows; search narrows the groups in place rather than
       swapping to a separate result list.

       px-2 is NOT a mistake to "fix" to px-4: this component renders in the
       layout's content slot, which already wraps it in p-2 — the two halves
       compound into the 16px inset that keeps the hover pills flush with the
       px-4 header block above. -->
  <div class="px-2 pb-2">
    <template v-if="displayGroups.length">
      <div
        v-for="(group, idx) in displayGroups"
        :key="group.key"
        :class="idx > 0 ? 'mt-4' : ''"
      >
        <SidebarMenu class="m-0 gap-1 p-0">
          <SidebarMenuItem
            v-for="item in group.items"
            :key="item.value"
          >
            <NavItem
              :active="activeValue === item.value"
              :aria-current="activeValue === item.value ? 'page' : undefined"
              @click="emit('select', item.value)"
            >
              <component
                :is="item.icon"
                v-if="item.icon"
                :stroke-width="1.75"
                class="size-4 shrink-0"
              />
              <span class="whitespace-nowrap">{{ t(item.label) }}</span>
            </NavItem>
          </SidebarMenuItem>
        </SidebarMenu>
      </div>
    </template>
    <div
      v-else
      class="px-3 py-6 text-center text-xs text-muted-foreground"
    >
      {{ t('common.noData') }}
    </div>
  </div>
</template>

<script setup lang="ts">
// The scrolling half of the detail sidebar (see ./index.vue for the split's
// rationale): renders the grouped nav into the layout's content slot, which
// is the one wrapped in a ScrollArea. Owns the search narrowing; the query
// itself is lifted to the caller (shared with the fixed header part).
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NavItem, SidebarMenu, SidebarMenuItem } from '@felinic/ui'
import type { DetailNavGroup, DetailNavItem } from './index.vue'

const props = withDefaults(defineProps<{
  groups: DetailNavGroup[]
  activeValue: string
  /** Raw search text captured by the header part's input; empty = no filter. */
  query?: string
  /**
   * Extra match test on top of the label/value match every caller gets. Lets
   * a caller surface a row by the settings buried inside it (e.g. "telegram"
   * finding the Channels tab).
   */
  matches?: (item: DetailNavItem, query: string) => boolean
}>(), {
  query: '',
  matches: undefined,
})

const emit = defineEmits<{
  select: [value: string]
}>()

const { t } = useI18n()

const normalizedQuery = computed(() => props.query.trim().toLowerCase())

function itemMatches(item: DetailNavItem): boolean {
  const query = normalizedQuery.value
  if (!query) return true
  if (t(item.label).toLowerCase().includes(query)) return true
  if (item.value.toLowerCase().includes(query)) return true
  return props.matches?.(item, query) ?? false
}

const displayGroups = computed(() =>
  props.groups
    .map(group => ({ ...group, items: group.items.filter(itemMatches) }))
    .filter(group => group.items.length > 0),
)
</script>
