<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, provide } from 'vue'
import { RouterView, useRouter, useRoute } from 'vue-router'
import { Toaster } from '@felinic/ui'
import { useSettingsStore } from '@memohai/web/store/settings'
import {
  DesktopRuntimeKey,
  DesktopShellKey,
  DesktopUpdatesKey,
  type DesktopRuntimeBridge,
  type DesktopUpdateBridge,
} from '@memohai/web/lib/desktop-shell'
import MainSection from '@memohai/web/pages/main-section/index.vue'

provide(DesktopShellKey, true)
provide(DesktopRuntimeKey, {
  runtimeState: window.api.desktop.runtimeState,
  configureRuntime: window.api.desktop.configureRuntime,
  onRuntimeStateChanged: window.api.desktop.onRuntimeStateChanged,
} satisfies DesktopRuntimeBridge)
provide(DesktopUpdatesKey, {
  getInfo: window.api.desktop.updates.getInfo,
  getState: window.api.desktop.updates.getState,
  check: window.api.desktop.updates.check,
  download: window.api.desktop.updates.download,
  install: window.api.desktop.updates.install,
  onStateChanged: window.api.desktop.updates.onStateChanged,
} satisfies DesktopUpdateBridge)
useSettingsStore()

// Mirror apps/web App.vue: keep chat dockview/scroll alive (DOM attached,
// full-size) while in settings, so returning has no black flash / re-scroll /
// relayout.
const route = useRoute()
const isChatRoute = computed(() => route.name === 'home' || route.name === 'bot')
const isSettingsRoute = computed(() => route.path.startsWith('/settings'))
const isAppArea = computed(() => isChatRoute.value || isSettingsRoute.value)

// Dev-only: toggle the component wall / design-token reference with
// Cmd/Ctrl+Shift+D. No-op (and not registered) in production builds.
const router = useRouter()
function onDevKey(e: KeyboardEvent) {
  if (!import.meta.env.DEV) return
  if ((e.metaKey || e.ctrlKey) && e.shiftKey && (e.key === 'D' || e.key === 'd')) {
    e.preventDefault()
    const onWall = router.currentRoute.value.path.startsWith('/dev/')
    void router.push(onWall ? '/' : '/dev/components')
  }
}
onMounted(() => {
  if (import.meta.env.DEV) window.addEventListener('keydown', onDevKey)
})
onBeforeUnmount(() => window.removeEventListener('keydown', onDevKey))
</script>

<template>
  <section>
    <MainSection v-if="isAppArea" />
    <!-- Permanent fixed settings layer (see apps/web App.vue): TRANSPARENT wrapper
         toggled with `visibility` only. settings-section paints its own opaque
         bg, so chat (not black) shows behind its slide/fade. No v-if (avoids
         compositor layer teardown flash), no opacity transition. -->
    <RouterView v-slot="{ Component }">
      <div
        class="fixed inset-0 z-40"
        :class="isSettingsRoute ? 'visible' : 'pointer-events-none invisible'"
      >
        <component
          :is="Component"
          v-if="isSettingsRoute"
        />
      </div>
      <component
        :is="Component"
        v-if="!isAppArea"
      />
    </RouterView>
    <Toaster position="top-right" />
  </section>
</template>
