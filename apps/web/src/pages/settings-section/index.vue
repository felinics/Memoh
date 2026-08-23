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
          <!-- ONE persistent shell across the breakpoint: the content column
               (router-view + KeepAlive) never unmounts when the width crosses
               <768px — only the chrome around it branches. An earlier
               MainLayout/MobileSettingsShell v-if swap destroyed the whole
               subtree (KeepAlive caches, scroll, unsaved form state) on every
               crossing; that is the same failure the chat shell's in-section
               branch was chosen to avoid. -->
          <MainLayout>
            <template #sidebar>
              <!-- Desktop-only rail. On a single bot's detail page the bot's own
                   nav (rendered by bots/detail.vue) takes over this column, so the
                   settings nav steps aside instead of stacking a second sidebar
                   beside it. Below the JS breakpoint the nav becomes the list
                   overlay inside #main instead. -->
              <SettingsSidebar
                v-if="!isMobile && !isBotDetail"
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
                  v-if="desktopShell && !isMobile && !isBotDetail"
                  class="h-8 shrink-0 [-webkit-app-region:drag]"
                />
                <!-- Mobile CONTENT bar: bot detail renders its own master-detail
                     chrome, so the shell bar steps aside there; the LIST overlay
                     carries the list-mode bar itself. -->
                <MobileTopBar
                  v-if="isMobile && !isBotDetail && !isListView"
                  mode="content"
                  @back="onContentBack"
                />
                <section class="flex-1 relative min-h-0 overflow-y-auto [scrollbar-gutter:stable]">
                  <router-view v-slot="{ Component }">
                    <KeepAlive>
                      <component :is="Component" />
                    </KeepAlive>
                  </router-view>
                </section>

                <!-- Mobile nav LIST = bare /settings, a REAL route (the desktop
                     default redirect to /settings/bots is re-applied by the
                     guard below, not by the router). Every level of the mobile
                     settings stack is addressable — list /settings, page
                     /settings/x, drill /settings/x/y — so the system back
                     button and the shell's ← walk the SAME history; the old
                     "list as route-less shell state" made the two diverge and
                     every seam (phantom page on system back, deep link buried
                     under the list, history loops) was that one divergence.
                     The overlay slides in from the LEFT: reaching the list is
                     always a "back" in the stack. -->
                <Transition
                  enter-active-class="transition-all duration-[90ms] ease-out"
                  enter-from-class="opacity-0 -translate-x-2.5"
                  leave-active-class="transition-all duration-[40ms] ease-in"
                  leave-to-class="opacity-0 -translate-x-2.5"
                >
                  <div
                    v-if="isMobile && isListView"
                    class="absolute inset-0 z-(--z-panel) flex flex-col bg-background"
                  >
                    <MobileTopBar
                      mode="list"
                      @back="onListBack"
                    />
                    <SettingsSidebar
                      class="min-h-0 flex-1"
                      hide-header
                      full-width
                    />
                  </div>
                </Transition>
              </SidebarInset>
            </template>
          </MainLayout>
        </div>
      </Transition>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, inject, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { SidebarInset } from '@felinic/ui'
import MainLayout from '@/layout/main-layout/index.vue'
import SettingsSidebar from '@/components/settings-sidebar/index.vue'
import MobileTopBar from './components/mobile-top-bar.vue'
import { DesktopShellKey } from '@/lib/desktop-shell'
import { useIsMobile } from '@/composables/useIsMobile'
import { usePreviousRoute } from '@/composables/useBackOr'
import { useBackToChatRoute } from '@/composables/useBackToChat'

const desktopShell = inject(DesktopShellKey, false)
const isMobile = useIsMobile()

const route = useRoute()
const router = useRouter()
const previousRoute = usePreviousRoute()
const backToChatRoute = useBackToChatRoute()

// macOS desktop only: the settings sidebar now runs to the very top of the window
// (the old full-width topbar is gone), so its header must clear the traffic lights.
// Mirrors main-section's computation.
const macTrafficReserve = computed(() =>
  desktopShell
  && typeof navigator !== 'undefined'
  && navigator.platform.toLowerCase().includes('mac'),
)

// On a single bot's detail page the bot's own nav owns the left column, so we
// drop the settings nav here to avoid two stacked sidebars (the "three-column"
// nesting). Every other settings route keeps the settings nav.
const isBotDetail = computed(() => route.name === 'bot-detail')

// The mobile list lives AT bare /settings; anything deeper is content.
const isListView = computed(() => route.path === '/settings')

// Desktop keeps the historical default page: bare /settings means "the bots
// page" there (the router carries no redirect so mobile can render its list
// at the same path).
watch([isMobile, () => route.path], ([mobile, path]) => {
  if (!mobile && path === '/settings') {
    void router.replace('/settings/bots').catch(() => {})
  }
}, { immediate: true })

// LIST ← exits settings: follow real history when settings was entered from
// another app page (chat), otherwise route to chat explicitly (cold load, or
// the entries below are settings-internal from replace cycles).
function onListBack(): void {
  const prev = previousRoute.value
  if (prev && !prev.path.startsWith('/settings')) {
    router.back()
    return
  }
  void router.push(backToChatRoute.value).catch(() => {})
}

// CONTENT ← : pop real drill-ins (bots → bots/new → progress, supermarket →
// Package detail) — their paths nest under the page that opened them, so "the
// route before this one is my ancestor" is exactly when router.back() is
// right. Everything else goes back to the list at /settings: replace, so the
// content page doesn't linger as a history entry above the list.
function onContentBack(): void {
  const prevPath = previousRoute.value?.path
  if (prevPath
    && prevPath.startsWith('/settings')
    && route.path.startsWith(`${prevPath.replace(/\/+$/, '')}/`)) {
    router.back()
    return
  }
  void router.replace('/settings').catch(() => {})
}

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
