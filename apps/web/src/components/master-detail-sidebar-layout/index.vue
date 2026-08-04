<template>
  <SidebarProvider
    class="min-h-[initial]! absolute inset-0"
    :default-open="true"
    disable-default-shortcut
  >
    <template v-if="!isMobile">
      <!-- Fixed width, never w-fit: the panel must not be sized by its content, or a
           long back label / name would stretch the whole sidebar. Web keeps
           responsive widths; desktop matches SettingsSidebar's fixed 15rem width. -->
      <Sidebar
        class="relative! **:[[role=navigation]]:relative! sidebar-container h-full! border-0! [&_[data-sidebar=sidebar]]:bg-transparent!"
        :class="desktopShell ? 'w-(--desktop-sidebar-width)!' : 'w-48! lg:w-52! xl:w-60!'"
      >
        <SidebarContent
          class="overflow-hidden h-full flex flex-col"
          :class="flush ? 'p-0' : 'p-2 pb-4 pt-4'"
        >
          <!-- Default: a nested card shell (box-in-box) for pages that sit to the
               right of the settings nav. flush: this layout IS the primary nav, so
               it goes edge-to-edge with a right divider, matching SettingsSidebar. -->
          <div
            class="flex-1 flex flex-col overflow-hidden min-h-0"
            :data-native-sidebar-surface="flush || undefined"
            :data-native-sidebar-tint="flush || undefined"
            :class="flush
              ? 'workspace-divider-r bg-sidebar'
              : 'border border-border-soft bg-muted/10 rounded-lg'"
          >
            <!-- Integrated Header (if provided) -->
            <div
              v-if="slots['sidebar-header']"
              class="shrink-0"
            >
              <slot name="sidebar-header" />
            </div>

            <!-- Content Group with ScrollArea -->
            <ScrollArea class="flex-1 min-h-0">
              <div class="p-2 flex flex-col gap-1">
                <slot name="sidebar-content" />
              </div>
            </ScrollArea>

            <!-- Integrated Footer (if provided) -->
            <SidebarFooter
              v-if="slots['sidebar-footer']"
              class="p-2 pt-0"
            >
              <slot name="sidebar-footer" />
            </SidebarFooter>
          </div>
        </SidebarContent>
      </Sidebar>

      <SidebarInset class="min-w-0 overflow-hidden">
        <section class="flex-1 min-w-0 relative min-h-0 overflow-hidden">
          <slot name="detail" />
        </section>
      </SidebarInset>
    </template>

    <!-- Below the JS breakpoint the sidebar/detail pair becomes a two-pane
         stack driven by the `detailOpen` prop — for the bot detail that prop
         is simply "the ?tab= query is present", so every level is addressable
         (list = bare path, content = ?tab=x) and the system back button walks
         the same history as the bar's ←. The old hamburger + left Sheet
         mobile mode was deleted outright — the stack IS its replacement.
         Both panes stay mounted (v-show, never v-if) so the detail page keeps
         its DOM and scroll position while the list is on top. -->
    <div
      v-else
      class="relative h-full w-full overflow-x-clip"
    >
      <Transition :name="transitionName">
        <div
          v-show="!detailOpen"
          class="absolute inset-0 flex flex-col bg-sidebar"
        >
          <div
            v-if="slots['sidebar-header']"
            class="shrink-0"
          >
            <slot name="sidebar-header" />
          </div>
          <ScrollArea class="flex-1 min-h-0">
            <div class="p-2 flex flex-col gap-1">
              <slot name="sidebar-content" />
            </div>
          </ScrollArea>
          <SidebarFooter
            v-if="slots['sidebar-footer']"
            class="p-2 pt-0"
          >
            <slot name="sidebar-footer" />
          </SidebarFooter>
        </div>
      </Transition>
      <Transition :name="transitionName">
        <div
          v-show="detailOpen"
          class="absolute inset-0 flex flex-col bg-background"
        >
          <!-- No title here: detail pages render their own PageShell headers,
               matching the settings shell's CONTENT bar. -->
          <MobileBar class="border-b border-border">
            <template #left>
              <MobileBarIconButton
                :icon="ChevronLeft"
                :label="t('common.back')"
                @click="emit('closeDetail')"
              />
            </template>
          </MobileBar>
          <section class="relative min-h-0 flex-1">
            <slot name="detail" />
          </section>
        </div>
      </Transition>
    </div>
  </SidebarProvider>
</template>

<script setup lang="ts">
import { ChevronLeft } from 'lucide-vue-next'
import { computed, inject, ref, useSlots, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  SidebarContent,
  SidebarFooter,
  SidebarProvider,
  Sidebar,
  SidebarInset,
  ScrollArea
} from '@felinic/ui'
import MobileBar from '@/components/mobile-bar/index.vue'
import MobileBarIconButton from '@/components/mobile-bar/icon-button.vue'
import { DesktopShellKey } from '@/lib/desktop-shell'
import { useIsMobile } from '@/composables/useIsMobile'

const props = withDefaults(defineProps<{
  // When true, this layout acts as the primary (only) sidebar: it drops the
  // nested-card chrome and sits flush against the viewport edge. Used by the
  // de-nested bot detail page; left false everywhere it nests under another nav.
  flush?: boolean
  // Mobile stack state, owned by the caller so it can be derived from the URL
  // (bot detail: "?tab= present"). Desktop ignores it entirely.
  detailOpen?: boolean
}>(), {
  flush: false,
  detailOpen: false,
})

const emit = defineEmits<{ closeDetail: [] }>()

defineSlots<{
  'sidebar-header'?: () => unknown
  'sidebar-content'?: () => unknown
  'sidebar-footer'?: () => unknown
  detail?: () => unknown
}>()

const slots = useSlots()
const desktopShell = inject(DesktopShellKey, false)
const isMobile = useIsMobile()
const { t } = useI18n()

const direction = ref<'forward' | 'back'>('forward')
const transitionName = computed(() =>
  direction.value === 'back' ? 'swap-back' : 'swap-forward',
)
watch(() => props.detailOpen, (open) => {
  direction.value = open ? 'forward' : 'back'
})
</script>
