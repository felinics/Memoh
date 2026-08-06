<template>
  <div
    class="h-dvh w-screen overflow-hidden transition-[opacity,scale] duration-[450ms] ease-out"
    :class="entering ? 'scale-[1.15] opacity-0' : 'scale-100 opacity-100'"
  >
    <!-- PUSH/PULL. The sidebar (a flex sibling) slides out left and the dock,
         flex-1, grows to fill the freed space — content shifts rather than being
         covered. dockview relays out per frame (no gating) to keep panels matched
         the whole way. -->
    <div class="flex h-full min-h-0 overflow-hidden">
      <SideBar
        v-if="!isMobile"
        :mac-traffic-reserve="macTrafficReserve"
      />
      <div class="flex min-w-0 min-h-0 flex-1 flex-col">
        <MobileTopBar v-if="isMobile" />
        <MainContainer />
      </div>
      <MobileNavSheet v-if="isMobile" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, inject, ref, onMounted } from 'vue'
import { storeToRefs } from 'pinia'
import { useRoute } from 'vue-router'
import { DesktopShellKey } from '@/lib/desktop-shell'
import SideBar from '@/components/sidebar/index.vue'
import MainContainer from '@/components/main-container/index.vue'
import MobileTopBar from './components/mobile-top-bar.vue'
import MobileNavSheet from './components/mobile-nav-sheet.vue'
import { ONBOARDING_KEYS } from '@/pages/onboarding/constants'
import { safeSessionGet, safeSessionRemove } from '@/utils/safe-storage'
import { useKeyboardCommand } from '@/composables/useKeyboardCommand'
import { appKeyboardCommands } from '@/lib/keyboard-commands'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'

const desktopShell = inject(DesktopShellKey, false)
const macTrafficReserve = computed(() =>
  desktopShell
  && typeof navigator !== 'undefined'
  && navigator.platform.toLowerCase().includes('mac'),
)

const shouldAnimateEntry = safeSessionGet(ONBOARDING_KEYS.entryAnimation) === '1'
if (shouldAnimateEntry) {
  safeSessionRemove(ONBOARDING_KEYS.entryAnimation)
}

const entering = ref(shouldAnimateEntry)

onMounted(() => {
  if (!shouldAnimateEntry) return
  requestAnimationFrame(() => {
    requestAnimationFrame(() => {
      entering.value = false
    })
  })
})

// toggleSidebar is registered here because MainSection owns the visible sidebar
// and workbench pane. MainSection stays mounted while settings pages are active,
// so the handler is always available; we no-op on settings routes to preserve
// the desktop settings sidebar's pinned-open intent AND prevent the web
// browser from falling through to its native Mod+B (bookmarks bar).
const workspaceTabs = useWorkspaceTabsStore()
const { isMobile } = storeToRefs(workspaceTabs)
const route = useRoute()
useKeyboardCommand(appKeyboardCommands.toggleSidebar, () => {
  if (route.path.startsWith('/settings')) return true
  // The mobile shell has no rail to toggle; still claim the command so the
  // browser never sees it.
  if (isMobile.value) return true
  workspaceTabs.toggleWorkbench()
  return true
})
</script>
