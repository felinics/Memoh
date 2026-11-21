<script setup lang="ts">
import { computed, defineAsyncComponent, h } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterView, useRoute, useRouter } from 'vue-router'
import { Toaster } from '@felinic/ui'
import { useSettingsStore } from '@/store/settings'
import { isChunkLoadError, reloadForChunkLoadError } from '@/lib/chunk-load-error'
import { dismissBootSplash, showBootFailure } from '@/lib/boot-splash'
import { holdPrefetchUntil, whenNetworkQuiet } from '@/lib/idle-prefetch'
import { loadMainSection, prefetchAppPages, showsChatWorkspace } from './routes'

useSettingsStore()

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

// Loaded on demand so the login and onboarding pages don't download the chat
// workspace before they can render (see loadMainSection). A chunk that fails
// to load reloads the page, the same recovery the router applies to route
// chunks; nothing in the workspace has user state yet at this point. Failing
// again within a minute of that reload, the loading splash switches to its
// failure state instead (see reloadForChunkLoadError, showBootFailure).
let markMainSectionSettled!: () => void
const mainSectionSettled = new Promise<void>((resolve) => {
  markMainSectionSettled = resolve
})
// Until the workspace arrives, the loading splash's mark and line stand in for
// it (the .boot-splash styles are in index.html; the timed status text is left
// out because it is localized there, before i18n exists). Signing in right
// after the login page opens can outrun the warm-up on a slow network: measured
// on Slow 4G, the page was otherwise empty for about 5s after the login page left.
const MainSectionLoading = () => h('div', { class: 'boot-splash', 'aria-hidden': 'true' }, [
  h('div', { class: 'boot-splash-stage' }, [
    h('img', { class: 'boot-splash-mark', src: '/logo.svg', alt: '' }),
    h('div', { class: 'boot-splash-loading' }, [
      h('div', { class: 'boot-splash-track' }, [h('div', { class: 'boot-splash-seg' })]),
    ]),
  ]),
])
const MainSection = defineAsyncComponent({
  loader: () => loadMainSection().finally(markMainSectionSettled),
  loadingComponent: MainSectionLoading,
  // The splash keeps its mark hidden for its first 200ms, so a quick load shows
  // only the page background either way.
  delay: 0,
  onError(error, _retry, fail) {
    if (isChunkLoadError(error)) {
      if (reloadForChunkLoadError()) {
        console.warn('[App] Chat workspace chunk failed to load, reloading...', error.message)
        return
      }
      console.error('[App] Chat workspace chunk failed to load again after a reload', error)
      showBootFailure(error.message)
    }
    fail()
  },
})

// The chat workspace (MainSection: sidebar + dockview) is mounted persistently
// here rather than as a route component. This keeps the chat DOM attached and
// full-size while the user is in settings, so dockview never relayouts from a
// zero size, the message list keeps its scroll position, and returning has no
// black flash / no re-scroll.
// (KeepAlive can't achieve this: it detaches the subtree, which is what caused
// all three regressions.)
//   - chat routes (home/bot): only MainSection shows.
//   - settings routes: MainSection stays mounted, settings content renders above it.
//   - auth-boundary routes (login/onboarding/oauth/dev): MainSection is not
//     mounted at all; those render alone via RouterView.
const isSettingsRoute = computed(() => route.path.startsWith('/settings'))
const isAppArea = computed(() => showsChatWorkspace(route))

// index.html shows a loading splash (#boot-splash) while the entry downloads.
// Vue mounting is not the end of the wait: the initial navigation still loads
// its lazy route page, and on chat and settings routes the chat workspace is
// still loading. Remove the splash once both have settled, on the next frame
// so the screen behind it has rendered. On failure it is dismissed too, so an
// error state is not hidden behind it.
const firstScreenSettled = router.isReady()
  .then(() => (isAppArea.value ? mainSectionSettled : undefined))
  .catch(() => {})
void firstScreenSettled.finally(() => requestAnimationFrame(dismissBootSplash))

// Warm the chat workspace and the settings pages in the background so the
// first open after sign-in and first-time sidebar clicks don't wait on a chunk
// (see prefetchAppPages). The downloads wait for the first screen. In the chat
// workspace they also wait out the burst of requests its first screen starts
// (fonts, a dozen API calls), so they don't delay the user's first clicks. On
// the login and onboarding pages they start once the page has rendered: the
// chat workspace is first in the queue, and signing in needs it. Queued here
// rather than when the router is ready, which can happen before this
// component mounts and so before the hold is in place.
holdPrefetchUntil(firstScreenSettled.then(() => (isAppArea.value ? whenNetworkQuiet() : undefined)))
prefetchAppPages()

// Localized headings for auto-shaped long-blob toasts (raw backend errors etc.).
// @felinic/ui is i18n-agnostic, so the app supplies these; they react to locale.
const toastHeadings = computed(() => ({
  message: t('common.toast.notice'),
  success: t('common.toast.success'),
  error: t('common.toast.error'),
  warning: t('common.toast.warning'),
  info: t('common.toast.info'),
}))
</script>

<template>
  <section>
    <!-- Persistent chat area. Stays mounted (and full-size) across chat↔settings
         so its dockview/scroll survive untouched. Unmounts only when leaving the
         authenticated app area (login/onboarding). -->
    <MainSection v-if="isAppArea" />

    <!-- Single RouterView (v-slot) drives both the settings section and the
         auth-boundary pages. On chat routes the matched component is a null stub,
         so `Component` renders nothing. -->
    <RouterView v-slot="{ Component }">
      <!-- Settings section: a permanent fixed full-screen positioning layer
           (always in the DOM + compositor layer tree), toggled with `visibility`
           only — never v-if, never opacity. Why each matters:
           (1) v-if'ing the layer tore down a promoted compositor layer in one
               frame → Chromium recomposited chat underneath with a gap (flash).
           (2) The wrapper is TRANSPARENT (no bg). settings-section paints its own
               opaque bg-background, so while it slides/fades in or out the layer
               behind it is the live chat — never a black backdrop. Earlier the
               wrapper itself was near-black, so any fade/leave revealed a dark
               wash. Transparent + visibility flip = clean, no flash, no fade.
           z-(--z-overlay), not --z-panel: this was bare z-40 pre-ladder (no
           dedicated tier — the ladder's z-40 slot folds into overlay or panel
           by use, see packages/ui/AGENTS.md "z 梯"). MainSection stays mounted
           BEHIND this layer while settings is open, and its chat-pane composer/
           slash-panel floats already sit at --z-panel (30) — tying this wrapper
           to the same 30 would make "settings always wins" depend on DOM order
           instead of an explicit rank, which is exactly the hidden-dependency
           risk the ladder migration is supposed to remove. --z-overlay (50)
           preserves the original strict 40-over-30 inequality. -->
      <div
        class="fixed inset-0 z-(--z-overlay)"
        :class="isSettingsRoute ? 'visible' : 'pointer-events-none invisible'"
      >
        <component
          :is="Component"
          v-if="isSettingsRoute"
        />
      </div>
      <!-- Auth-boundary routes (login/onboarding/oauth/dev) render full-screen on
           their own; MainSection and the settings content are both absent. -->
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
