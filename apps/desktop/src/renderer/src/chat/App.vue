<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, provide } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterView, useRouter, useRoute } from 'vue-router'
import { Toaster } from '@felinic/ui'
import {
  DesktopRuntimeKey,
  DesktopShellKey,
  DesktopUpdatesKey,
  DesktopWindowKey,
  type DesktopRuntimeBridge,
  type DesktopUpdateBridge,
  type DesktopWindowBridge,
} from '@memohai/web/lib/desktop-shell'
import { isChunkLoadError, reloadForChunkLoadError } from '@memohai/web/lib/chunk-load-error'
import { holdPrefetchUntil, whenNetworkQuiet } from '@memohai/web/lib/idle-prefetch'
import { loadMainSection, prefetchAppPages, showsChatWorkspace } from '@memohai/web/routes'
import { useSettingsStore } from '@memohai/web/store/settings'

useSettingsStore()

const { t } = useI18n()

provide(DesktopShellKey, true)
provide(DesktopWindowKey, {
  isFullScreen: window.api.desktop.isFullScreen,
  onFullScreenChanged: window.api.desktop.onFullScreenChanged,
  setIgnoreMenuShortcuts: window.api.window.setIgnoreMenuShortcuts,
} satisfies DesktopWindowBridge)
provide(DesktopRuntimeKey, {
  runtimeState: window.api.desktop.runtimeState,
  configureRuntime: window.api.desktop.configureRuntime,
  setRuntimePaused: window.api.desktop.setRuntimePaused,
  onRuntimeStateChanged: window.api.desktop.onRuntimeStateChanged,
} satisfies DesktopRuntimeBridge)
provide(DesktopUpdatesKey, {
  getInfo: window.api.desktop.updates.getInfo,
  getState: window.api.desktop.updates.getState,
  check: window.api.desktop.updates.check,
  setAutoUpdate: window.api.desktop.updates.setAutoUpdate,
  install: window.api.desktop.updates.install,
  onStateChanged: window.api.desktop.updates.onStateChanged,
} satisfies DesktopUpdateBridge)

let markMainSectionSettled!: () => void
const mainSectionSettled = new Promise<void>((resolve) => {
  markMainSectionSettled = resolve
})
const MainSection = defineAsyncComponent({
  loader: () => loadMainSection().finally(markMainSectionSettled),
  onError(error, _retry, fail) {
    if (isChunkLoadError(error) && reloadForChunkLoadError()) {
      console.warn('[App] Chat workspace chunk failed to load, reloading...', error.message)
      return
    }
    fail()
  },
})

// Mirror apps/web App.vue: keep chat dockview/scroll alive (DOM attached,
// full-size) while in settings, so returning has no black flash / re-scroll /
// relayout.
const route = useRoute()
const router = useRouter()
const isSettingsRoute = computed(() => route.path.startsWith('/settings'))
const isAppArea = computed(() => showsChatWorkspace(route))

const firstScreenSettled = router.isReady()
  .then(() => (isAppArea.value ? mainSectionSettled : undefined))
  .catch(() => {})
holdPrefetchUntil(firstScreenSettled.then(() => (isAppArea.value ? whenNetworkQuiet() : undefined)))
prefetchAppPages()

const toastHeadings = computed(() => ({
  message: t('common.toast.notice'),
  success: t('common.toast.success'),
  error: t('common.toast.error'),
  warning: t('common.toast.warning'),
  info: t('common.toast.info'),
}))

// Dev-only: toggle the component wall / design-token reference with
// Cmd/Ctrl+Shift+D. No-op (and not registered) in production builds.
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
        class="fixed inset-0 z-(--z-overlay)"
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
    <Toaster
      position="top-right"
      :headings="toastHeadings"
    />
  </section>
</template>
