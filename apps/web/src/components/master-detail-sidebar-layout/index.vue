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
                <slot
                  name="sidebar-content"
                  :open-detail="openDetail"
                />
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

    <!-- Below the JS breakpoint the sidebar/detail pair becomes a two-pane stack
         keyed off component state, NOT routes — the same contract as the settings
         mobile shell. LIST renders the sidebar slots as a full-screen nav page
         (a sidebar tap pushes CONTENT via the openDetail slot prop); CONTENT
         renders the detail slot full-screen under a back bar that pops to LIST.
         Both panes stay mounted (v-show, never v-if) so the detail page keeps
         its DOM and scroll position while the list is on top. The transition
         names reuse the ui swap-forward/swap-back classes: forward pushes
         content in from the right, back slides it out to the right.
         The old hamburger + left Sheet mobile mode was deleted outright — the
         stack IS its replacement, not an addition beside it. -->
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
              <slot
                name="sidebar-content"
                :open-detail="openDetail"
              />
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
          <header class="flex h-11 shrink-0 items-center border-b border-border bg-background px-1.5">
            <Button
              variant="ghost"
              size="icon"
              shape="circle"
              :class="iconButtonClass"
              :title="backLabel"
              :aria-label="backLabel"
              @click="closeDetail"
            >
              <ChevronLeft :stroke-width="1.75" />
            </Button>
          </header>
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
import { computed, inject, ref, useSlots } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Button,
  SidebarContent,
  SidebarFooter,
  SidebarProvider,
  Sidebar,
  SidebarInset,
  ScrollArea
} from '@felinic/ui'
import { DesktopShellKey } from '@/lib/desktop-shell'
import { useIsMobile } from '@/composables/useIsMobile'
import { usePreviousRoute } from '@/composables/useBackOr'

withDefaults(defineProps<{
  // When true, this layout acts as the primary (only) sidebar: it drops the
  // nested-card chrome and sits flush against the viewport edge. Used by the
  // de-nested bot detail page; left false everywhere it nests under another nav.
  flush?: boolean
}>(), {
  flush: false,
})

defineSlots<{
  'sidebar-header'?: () => unknown
  // openDetail pushes the mobile stack to CONTENT; a no-op render prop on the
  // desktop branch (the panes it drives are not mounted there).
  'sidebar-content'?: (props: { openDetail: () => void }) => unknown
  'sidebar-footer'?: () => unknown
  detail?: () => unknown
}>()

const slots = useSlots()
const desktopShell = inject(DesktopShellKey, false)
const isMobile = useIsMobile()
const { t } = useI18n()
const previousRoute = usePreviousRoute()

// Entry rule, decided ONCE at setup — mirrors the settings mobile shell:
// arriving from any in-app page opens on the nav LIST; a cold load / refresh /
// deep link (no tracked predecessor) opens straight on CONTENT so a `?tab=`
// URL keeps its meaning. installBackHistory's afterEach has already recorded
// the predecessor by the time setup runs.
const detailOpen = ref(previousRoute.value == null)
const direction = ref<'forward' | 'back'>('forward')
const transitionName = computed(() =>
  direction.value === 'back' ? 'swap-back' : 'swap-forward',
)

function openDetail(): void {
  direction.value = 'forward'
  detailOpen.value = true
}

function closeDetail(): void {
  direction.value = 'back'
  detailOpen.value = false
}

const backLabel = computed(() => t('chat.topBar.goBack'))

// Same chrome as the settings mobile top bar's icon button (muted at rest →
// foreground on hover) so the two shells' bars read as one language.
const iconButtonClass = 'shrink-0 text-muted-foreground hover:text-foreground' /* ui-allow-style */
</script>
