<template>
  <!-- Web keeps an opaque root over the persistent chat. macOS Desktop clears
       this root in desktop-shell.css: SidebarInset still paints the content pane,
       while the sidebar exposes the native material behind the renderer. -->
  <div
    class="flex h-dvh flex-col overflow-hidden bg-background"
    data-desktop-window-layer
  >
    <div class="min-h-0 flex-1">
      <!-- Whole settings view (sidebar + content) slides in from the right on open,
           faster slide-out on leave; navigation is held until the leave plays. -->
      <Transition
        appear
        enter-active-class="transition-all duration-[90ms] ease-out"
        enter-from-class="opacity-0 translate-x-2.5"
        leave-active-class="transition-all duration-[40ms] ease-in"
        leave-to-class="opacity-0 translate-x-2.5"
        @after-leave="onAfterLeave"
      >
        <div
          v-if="show"
          class="h-full"
        >
          <MainLayout>
            <template #sidebar>
              <!-- De-nest: inside a single bot, its own nav (rendered by
                   bots/detail.vue) takes over this column, so the settings nav
                   steps aside instead of stacking a second sidebar beside it.
                   The sidebar is h-dvh, so its right border now runs unbroken from
                   the very top — there's no full-width topbar cutting across it.
                   The macOS traffic lights are cleared by the sidebar's own top
                   drag row (mac-traffic-reserve), mirroring the chat SideBar. -->
              <SettingsSidebar
                v-if="!isBotDetail"
                :mac-traffic-reserve="macTrafficReserve"
              />
            </template>
            <template #main>
              <SidebarInset class="flex flex-col overflow-hidden">
                <!-- Top drag strip over the content pane only (not full-width), so
                     the window stays draggable up here while the sidebar's vertical
                     edge reads as the single continuous divider. No border/fill —
                     it shares --background with the content below. Skipped for bot
                     detail: that route renders its OWN full-height sidebar inside
                     #main (MasterDetailSidebarLayout), so a strip here would sit
                     ON TOP of it and push its divider down — bot detail handles its
                     own top drag/traffic clearance instead. -->
                <div
                  v-if="desktopShell && !isBotDetail"
                  class="h-8 shrink-0 [-webkit-app-region:drag]"
                />
                <section class="flex-1 relative min-h-0 overflow-y-auto [scrollbar-gutter:stable]">
                  <router-view v-slot="{ Component }">
                    <KeepAlive>
                      <component :is="Component" />
                    </KeepAlive>
                  </router-view>
                </section>
              </SidebarInset>
            </template>
          </MainLayout>
        </div>
      </Transition>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, inject, ref } from 'vue'
import { onBeforeRouteLeave, useRoute } from 'vue-router'
import { SidebarInset } from '@felinic/ui'
import MainLayout from '@/layout/main-layout/index.vue'
import SettingsSidebar from '@/components/settings-sidebar/index.vue'
import { DesktopShellKey } from '@/lib/desktop-shell'

const desktopShell = inject(DesktopShellKey, false)

// macOS desktop only: the settings sidebar now runs to the very top of the window
// (the old full-width topbar is gone), so its header must clear the traffic lights.
// Mirrors main-section's computation.
const macTrafficReserve = computed(() =>
  desktopShell
  && typeof navigator !== 'undefined'
  && navigator.platform.toLowerCase().includes('mac'),
)

const route = useRoute()
// On a single bot's detail page the bot's own nav owns the left column, so we
// drop the settings nav here to avoid two stacked sidebars (the "three-column"
// nesting). Every other settings route keeps the settings nav.
const isBotDetail = computed(() => route.name === 'bot-detail')

// Page transition: slide-in from the right on open, faster slide-out on leave.
// We hold the navigation until the leave animation has played.
const show = ref(true)
let pendingNext: (() => void) | null = null

onBeforeRouteLeave((_to, _from, next) => {
  show.value = false
  pendingNext = next
})

function onAfterLeave(): void {
  pendingNext?.()
  pendingNext = null
}
</script>
