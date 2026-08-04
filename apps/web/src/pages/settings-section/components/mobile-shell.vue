<template>
  <!-- Mobile settings = a two-pane stack keyed off shell state, NOT routes:
       LIST (the settings nav, full width) and CONTENT (the routed page, full
       screen) cross-slide; "the list" never gets a URL of its own. Both panes
       stay mounted (v-show, never v-if) so the KeepAlive'd pages under
       CONTENT keep their DOM and scroll position while the list is on top.
       The transition names reuse the swap-forward/swap-back classes (see
       SwapTransition): forward pushes content in from the right, back slides
       it out to the right. -->
  <!-- SidebarProvider: the ui Sidebar injects its context unconditionally
       (even with collapsible="none"), which the desktop path gets from
       MainLayout — the mobile shell composes the sidebar directly, so it must
       provide the context itself. -->
  <SidebarProvider
    disable-default-shortcut
    class="relative h-full overflow-x-clip"
  >
    <Transition :name="transitionName">
      <div
        v-show="view === 'list'"
        class="absolute inset-0 flex flex-col"
      >
        <MobileTopBar
          mode="list"
          @back="onListBack"
        />
        <SettingsSidebar
          class="min-h-0 flex-1"
          hide-header
          full-width
          @navigate="onListNavigate"
        />
      </div>
    </Transition>
    <Transition :name="transitionName">
      <div
        v-show="view === 'content'"
        class="absolute inset-0 flex flex-col"
      >
        <!-- The bot detail route renders its OWN full-height master-detail
             chrome, so the shell bar steps aside there — the same de-nesting
             rule the desktop sidebar slot follows. -->
        <MobileTopBar
          v-if="!isBotDetail"
          mode="content"
          @back="onContentBack"
        />
        <section class="min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable]">
          <router-view v-slot="{ Component }">
            <KeepAlive>
              <component :is="Component" />
            </KeepAlive>
          </router-view>
        </section>
      </div>
    </Transition>
  </SidebarProvider>
</template>

<script setup lang="ts">
import { computed, provide, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { SidebarProvider } from '@felinic/ui'
import SettingsSidebar from '@/components/settings-sidebar/index.vue'
import MobileTopBar from './mobile-top-bar.vue'
import { useBackToChatRoute } from '@/composables/useBackToChat'
import { usePreviousRoute } from '@/composables/useBackOr'
import { SettingsMobileShellKey } from '@/lib/settings-mobile-shell'

const route = useRoute()
const router = useRouter()
const previousRoute = usePreviousRoute()
const backToChatRoute = useBackToChatRoute()

type ShellView = 'list' | 'content'
type ShellDirection = 'forward' | 'back'

// Entry rule, decided ONCE at mount (this component only mounts when the
// route enters /settings): arriving from a non-settings page opens on the
// list; a cold load / refresh on a deep link (no tracked predecessor) opens
// straight on the content so the URL keeps its meaning. installBackHistory's
// afterEach has already recorded the predecessor by the time setup runs.
const entry = previousRoute.value
const view = ref<ShellView>(
  entry != null && !entry.path.startsWith('/settings') ? 'list' : 'content',
)
const direction = ref<ShellDirection>('forward')

const transitionName = computed(() =>
  direction.value === 'back' ? 'swap-back' : 'swap-forward',
)

// A single bot's detail renders its own nav in place of the shell chrome.
const isBotDetail = computed(() => route.name === 'bot-detail')

// Any settings-internal PATH change lands on CONTENT (a list tap pushed a
// page; drill-ins push deeper). Query-only writes (in-page view-swap state
// mirrored via router.replace) must not move the shell, so this watches the
// path, not the full route.
watch(() => route.path, (path, oldPath) => {
  if (path === oldPath) return
  direction.value = 'forward'
  view.value = 'content'
})

function showList(): void {
  direction.value = 'back'
  view.value = 'list'
}

// Driven by the sidebar's navigate emit rather than the path watcher:
// re-tapping the item whose route is already current is a duplicate push the
// router drops (no path change), but it must still leave the list.
function onListNavigate(): void {
  direction.value = 'forward'
  view.value = 'content'
}

function onListBack(): void {
  router.push(backToChatRoute.value).catch(() => {})
}

function onContentBack(): void {
  const prevPath = previousRoute.value?.path
  // Pop real drill-ins (bots → bots/new → progress, supermarket → plugin
  // detail): their paths nest under the page that opened them, so "the route
  // before this one is my ancestor" is exactly when router.back() is right.
  // Anything else (a sibling top-level page, or the route the LIST state
  // happens to sit on) goes back to the list instead — blindly following
  // history there ping-pongs between the two most recent content pages and
  // never reaches the list.
  if (prevPath
    && prevPath.startsWith('/settings')
    && route.path.startsWith(`${prevPath.replace(/\/+$/, '')}/`)) {
    router.back()
    return
  }
  showList()
}

provide(SettingsMobileShellKey, { view, showList })
</script>
