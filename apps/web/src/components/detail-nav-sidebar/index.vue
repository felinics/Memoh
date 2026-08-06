<template>
  <!-- Mac desktop: the sidebar's top strip is the window drag handle, so the
       traffic lights never sit on top of the back row. -->
  <div
    v-if="macTrafficReserve"
    class="h-12 shrink-0 [-webkit-app-region:drag]"
    aria-hidden="true"
  />
  <div
    class="flex flex-col px-4 pb-3"
    :class="macTrafficReserve ? undefined : 'pt-[1.125rem]'"
  >
    <!-- Back sits at the very top, same position/size/style as the settings
         sidebar's own back row, so returning always lands the affordance in
         the same spot. -->
    <NavItem
      class="[-webkit-app-region:no-drag]"
      @click="emit('back')"
    >
      <ChevronLeft class="size-3.5 shrink-0" />
      <span class="min-w-0 truncate">{{ backLabel }}</span>
    </NavItem>

    <!-- Whatever identifies the thing being configured (an avatar card, a
         name block). Owned by the caller: it is the one part that differs
         per entity. -->
    <div
      v-if="$slots.identity"
      class="mt-3"
    >
      <slot name="identity" />
    </div>

    <div
      v-if="searchable"
      class="relative mt-3"
    >
      <Search class="absolute left-2.5 top-1/2 size-3 -translate-y-1/2 text-muted-foreground" />
      <Input
        v-model="searchQuery"
        type="text"
        name="detail-nav-search"
        autocomplete="off"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        class="h-8 pl-8 pr-8 text-xs"
        :placeholder="searchPlaceholder ?? t('common.search')"
      />
      <button
        v-if="searchQuery"
        type="button"
        :class="clearButtonClass"
        :title="t('common.clear')"
        :aria-label="t('common.clear')"
        @click="searchQuery = ''"
      >
        <X class="size-2.5" />
      </button>
    </div>
  </div>

  <!-- Grouped nav rows; search narrows the groups in place rather than
       swapping to a separate result list. -->
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
              @click="select(item.value)"
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
import { computed, ref, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronLeft, Search, X } from 'lucide-vue-next'
import { Input, NavItem, SidebarMenu, SidebarMenuItem } from '@felinic/ui'

// The left bar shared by every detail surface that configures ONE entity
// (a bot, a project): back row → identity → search → grouped nav. It owns the
// chrome and the in-place search narrowing; the caller owns what the rows mean
// and how selecting one navigates.
export interface DetailNavItem {
  value: string
  /** i18n key, resolved here. */
  label: string
  icon?: Component
}

export interface DetailNavGroup {
  key: string
  items: DetailNavItem[]
}

const props = withDefaults(defineProps<{
  backLabel: string
  groups: DetailNavGroup[]
  activeValue: string
  searchable?: boolean
  searchPlaceholder?: string
  macTrafficReserve?: boolean
  /**
   * Extra match test for the search box, on top of the label/value match
   * every caller gets. Lets a caller surface a row by the settings buried
   * inside it (e.g. "telegram" finding the Channels tab).
   */
  matches?: (item: DetailNavItem, query: string) => boolean
}>(), {
  searchable: true,
  searchPlaceholder: undefined,
  macTrafficReserve: false,
  matches: undefined,
})

const emit = defineEmits<{
  back: []
  select: [value: string]
}>()

const { t } = useI18n()

const searchQuery = ref('')
const normalizedQuery = computed(() => searchQuery.value.trim().toLowerCase())

// Hand-rolled clear affordance inside the field (an in-field control, not a
// standalone button) — same shape the bot detail sidebar shipped.
const clearButtonClass = 'absolute right-2 top-1/2 flex size-4 shrink-0 -translate-y-1/2 items-center justify-center rounded-full text-muted-foreground hover:bg-muted' /* ui-allow-style */

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

function select(value: string) {
  searchQuery.value = ''
  emit('select', value)
}
</script>
