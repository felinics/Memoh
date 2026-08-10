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
        v-model="query"
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
        v-if="query"
        type="button"
        :class="clearButtonClass"
        :title="t('common.clear')"
        :aria-label="t('common.clear')"
        @click="query = ''"
      >
        <X class="size-2.5" />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronLeft, Search, X } from 'lucide-vue-next'
import { Input, NavItem } from '@felinic/ui'

// The fixed head of the left bar shared by every detail surface that
// configures ONE entity (a bot, a project): back row → identity → search.
// The grouped nav is a SEPARATE component (./menu.vue) on purpose: the
// layout renders its header slot in a bare non-scrolling container and its
// content slot inside a ScrollArea, so the head goes to #sidebar-header and
// the menu to #sidebar-content. Collapsing the two into one component placed
// in a single slot either loses scrolling (header slot) or scrolls the back
// row away (content slot) — both were shipped once and reverted.
//
// The search query is the one piece of state the two parts share, so it is
// lifted to the caller via v-model and handed to the menu as the `query`
// prop. Filtering itself lives in the menu.
export interface DetailNavItem {
  value: string
  /** i18n key, resolved by the menu. */
  label: string
  icon?: Component
}

export interface DetailNavGroup {
  key: string
  items: DetailNavItem[]
}

withDefaults(defineProps<{
  backLabel: string
  searchable?: boolean
  searchPlaceholder?: string
  // When true, the sidebar reserves a draggable macOS traffic-light strip above
  // its visible controls.
  macTrafficReserve?: boolean
}>(), {
  searchable: true,
  searchPlaceholder: undefined,
  macTrafficReserve: false,
})

const emit = defineEmits<{
  back: []
}>()

const query = defineModel<string>({ default: '' })

const { t } = useI18n()

// Hand-rolled clear affordance inside the field (an in-field control, not a
// standalone button) — same shape the bot detail sidebar shipped.
const clearButtonClass = 'absolute right-2 top-1/2 flex size-4 shrink-0 -translate-y-1/2 items-center justify-center rounded-full text-muted-foreground hover:bg-muted' /* ui-allow-style */
</script>
