<template>
  <!-- Web keeps an opaque root over the persistent chat. macOS Desktop clears
       this root in desktop-shell.css: SidebarInset still paints the content pane,
       while the sidebar exposes the native material behind the renderer. -->
  <div
    class="safe-area-frame flex h-dvh flex-col overflow-hidden bg-background"
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
              <SidebarInset
                class="relative flex flex-col overflow-hidden"
                data-settings-content-shell
              >
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
                  data-settings-content-bar
                  mode="content"
                  @back="onContentBack"
                />
                <section
                  data-settings-content-scroll
                  class="flex-1 relative min-h-0 overflow-y-auto [scrollbar-gutter:stable]"
                >
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
                     Moving between the list and a page is a stack push/pop
                     driven by a spring (see "List ⇄ page motion" below). -->
                <Transition
                  :css="false"
                  @enter="onListEnter"
                  @leave="onListLeave"
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
import { CompositorSpring, PAGE_SPRING } from '@/lib/compositor-spring'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { SidebarInset } from '@felinic/ui'
import MainLayout from '@/layout/main-layout/index.vue'
import SettingsSidebar from '@/components/settings-sidebar/index.vue'
import MobileTopBar from './components/mobile-top-bar.vue'
import { DesktopShellKey } from '@/lib/desktop-shell'
import { useMacTrafficReserve } from '@/composables/useMacTrafficReserve'
import { useIsMobile } from '@/composables/useIsMobile'
import { backOr } from '@/composables/useBackOr'
import { useNativeBack } from '@/shells/mobile/native-bridge'
import { useBackToChatRoute } from '@/composables/useBackToChat'

const desktopShell = inject(DesktopShellKey, false)
const isMobile = useIsMobile()

const route = useRoute()
const router = useRouter()
const backToChatRoute = useBackToChatRoute()

// macOS desktop only: the settings sidebar now runs to the very top of the window
// (the old full-width topbar is gone), so its header must clear the traffic lights.
const macTrafficReserve = useMacTrafficReserve()

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

// Back on mobile, from the list or a page, on screen or by the system gesture
// (the same action, see shells/mobile/native-bridge.ts): pop history when
// settings was reached by navigating, otherwise step up to the parent — the
// chat from the list, the list from a page — without pushing.
function onListBack(): void {
  backOr(router, backToChatRoute.value)
}

function onContentBack(): void {
  backOr(router, '/settings')
}

useNativeBack(() => {
  if (!isMobile.value) return false
  if (isListView.value) onListBack()
  else onContentBack()
  return true
})

// ── List ⇄ page motion (mobile) ─────────────────────────────────────────────
// Opening a page from the list is a stack push: the page slides in from the
// right over the list while the list drifts left by a fraction of the width.
// The list stays rendered for the whole push, so the pressed row's feedback
// (ripple / highlight) plays out on it instead of vanishing with the list the
// moment the route changes.
//
// Going back cannot slide the page out the other way: bare /settings has no
// child route, so the page's content is already gone when the list returns.
// The list therefore slides back in from the left over the shell.
//
// One spring value p runs from 0 (list in front) to 1 (page in front).
const LIST_PARALLAX = 0.3
// Button-driven page moves start at 400 px/s, like the two-page navigation.
const TAP_VELOCITY_PX = 400

type StackMove = 'push' | 'pop'

let listEl: HTMLElement | null = null
let stackMove: StackMove = 'push'
let stackWidth = 0

function contentLayers(shell: HTMLElement | null): HTMLElement[] {
  if (!shell) return []
  return [...shell.querySelectorAll<HTMLElement>(':scope > [data-settings-content-bar], :scope > [data-settings-content-scroll]')]
}

const stackSpring = new CompositorSpring(0, () => {
  if (stackMove === 'pop') {
    return [{ el: listEl, prop: 'transform', format: p => `translate3d(${(-stackWidth * p).toFixed(2)}px, 0, 0)` }]
  }
  return [
    { el: listEl, prop: 'transform', format: p => `translate3d(${(-LIST_PARALLAX * stackWidth * p).toFixed(2)}px, 0, 0)` },
    ...contentLayers(listEl?.parentElement ?? null).map(el => ({
      el,
      prop: 'transform' as const,
      format: (p: number) => `translate3d(${(stackWidth * (1 - p)).toFixed(2)}px, 0, 0)`,
    })),
  ]
})

// During a push the list sits under the page (the shell isolates so that a
// negative z-index still paints above the shell's own background), and the
// page carries an opaque background so the list does not show through.
function setPushing(el: HTMLElement, pushing: boolean): void {
  const shell = el.parentElement
  if (!shell) return
  shell.style.isolation = pushing ? 'isolate' : ''
  el.style.zIndex = pushing ? '-1' : ''
  for (const layer of contentLayers(shell)) {
    layer.style.backgroundColor = pushing ? 'var(--background)' : ''
    layer.style.transform = ''
  }
}

function runStack(el: Element, move: StackMove, done: () => void): void {
  if (!(el instanceof HTMLElement) || matchMedia('(prefers-reduced-motion: reduce)').matches) {
    done()
    return
  }
  // A move that interrupts the other one starts from where it had reached.
  const interrupted = listEl !== null
  stackSpring.sampleMidAirIfRunning()
  if (listEl) setPushing(listEl, false)
  listEl = el
  stackMove = move
  stackWidth = el.parentElement?.clientWidth ?? el.clientWidth
  setPushing(el, move === 'push')
  const target = move === 'push' ? 1 : 0
  if (!interrupted) stackSpring.setImmediate(1 - target)
  stackSpring.springTo(target, (move === 'push' ? 1 : -1) * TAP_VELOCITY_PX / Math.max(1, stackWidth), {
    ...PAGE_SPRING,
    onRest: () => {
      setPushing(el, false)
      el.style.transform = ''
      listEl = null
      done()
    },
  })
}

function onListLeave(el: Element, done: () => void): void {
  runStack(el, 'push', done)
}

function onListEnter(el: Element, done: () => void): void {
  runStack(el, 'pop', done)
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

<style>
/* Keep the scroll viewport stationary while list and detail cross-slide.
   The shell bar overlays it; only the list pane reserves the bar's h-11.
   Each transitioning pane retains its own inset until it leaves the DOM.
   A page that hosts its own detail view without a top-level SwapTransition
   (Providers swaps inside each scope panel) drops the inset while a detail is
   open: the bar is hidden then, so the inset would only leave a gap. */
[data-settings-content-shell] > [data-settings-content-bar] {
  position: absolute;
  inset-inline: 0;
  top: 0;
  z-index: var(--z-panel);
}
[data-settings-content-shell]:has(> [data-settings-content-bar]) > [data-settings-content-scroll] > [data-view-swap] {
  display: flow-root;
}
[data-settings-content-shell]:has(> [data-settings-content-bar]) > [data-settings-content-scroll] > :not([data-view-swap]):not(:has([data-settings-detail-back])),
[data-settings-content-shell]:has(> [data-settings-content-bar]) > [data-settings-content-scroll] > [data-view-swap] > :not(:has([data-settings-detail-back])) {
  margin-top: calc(var(--spacing) * 11);
}
/* Cached KeepAlive pages are detached; the separate settings home bar is
   unaffected. The overlay can hide without resizing the scroll viewport. */
[data-settings-content-shell]:has([data-settings-detail-back]) > [data-settings-content-bar] {
  display: none;
}
</style>
