<template>
  <div
    ref="rootEl"
    class="flex flex-col flex-1 h-full min-w-0 bg-background"
    :class="{ 'memoh-dock-dragging': store.panelDragging }"
  >
    <DockviewVue
      class="h-full w-full"
      :components="panelComponents"
      :watermark-component="watermarkComponent"
      :default-tab-component="defaultTabComponent"
      :prefix-header-actions-component="prefixHeaderActionsComponent"
      :left-header-actions-component="leftHeaderActionsComponent"
      :right-header-actions-component="rightHeaderActionsComponent"
      :theme="memohTheme"
      :disable-floating-groups="true"
      :disable-tabs-overflow-list="true"
      :disable-auto-resizing="true"
      :scrollbars="'native'"
      :get-tab-context-menu-items="getTabContextMenuItems"
      @ready="onReady"
    />
    <TabCloseConfirm />
  </div>
</template>

<script setup lang="ts">
import { defineAsyncComponent, defineComponent, h, inject, onBeforeUnmount, onMounted, provide, ref, type AsyncComponentLoader } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import {
  DockviewVue,
  type DockviewReadyEvent,
  type DockviewTheme,
  type GetTabContextMenuItemsParams,
  type ContextMenuItem,
  type VueComponent,
} from 'dockview-vue'
import 'dockview-vue/dist/styles/dockview.css'
import '@/styles/dockview-theme.css'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { openInFileManagerKey, openAssetPreviewKey } from '../composables/useFileManagerProvider'
import { normalizeFileManagerPath } from './file-manager-path'
import { DesktopShellKey, DesktopWindowKey } from '@/lib/desktop-shell'
import { prefetchLater, whenNetworkQuiet } from '@/lib/idle-prefetch'
import { withStylesheetRecovery } from '@/lib/chunk-load-error'
import PanelChat from './dockview/panel-chat.vue'
import PanelLoading, { retryPanelLoadKey } from './dockview/panel-loading.vue'
import WorkspaceWatermark from './dockview/workspace-watermark.vue'
import WorkspaceTab from './dockview/workspace-tab.vue'
import GroupActions from './dockview/group-actions.vue'
import HeaderAddActions from './dockview/header-add-actions.vue'
import PrefixHeaderActions from './dockview/prefix-header-actions.vue'
import TabCloseConfirm from './dockview/tab-close-confirm.vue'

const { t } = useI18n()
const store = useWorkspaceTabsStore()
const { api } = storeToRefs(store)

// dockview's own auto-resize is disabled at construction (:disable-auto-resizing);
// we drive layout from this ResizeObserver instead. The dock is a flex-1 sibling
// of the sidebar (push/pull model), so when the sidebar slides out/in this root's
// width changes frame-by-frame; the observer relays layout before paint, keeping
// panels matched to the latest container size without an extra frame of lag. The
// right edge is pinned (viewport edge), so the right-side actions ("+") never move
// while the width changes - no snap.
// (NOTE: dockview 6.6.1 ignores updateOptions({ disableAutoResizing }) at runtime
// — a guard checks the wrong key — so toggling the prop reactively does nothing;
// owning the observer is the reliable route.)
const rootEl = ref<HTMLElement | null>(null)
let resizeObserver: ResizeObserver | null = null
let lastLayoutSize = { width: -1, height: -1 }

function normalizeLayoutSize(width: number, height: number): { width: number, height: number } | null {
  const normalized = {
    width: Math.round(width),
    height: Math.round(height),
  }
  if (normalized.width <= 0 || normalized.height <= 0) return null
  return normalized
}

function readLayoutSize(): { width: number, height: number } | null {
  const el = rootEl.value
  if (!el) return null

  return normalizeLayoutSize(el.clientWidth, el.clientHeight)
}

function readObservedSize(entry: ResizeObserverEntry): { width: number, height: number } | null {
  return normalizeLayoutSize(entry.contentRect.width, entry.contentRect.height)
}

function applyLayoutSize(size: { width: number, height: number }) {
  const dock = api.value
  if (!dock) return
  if (size.width === lastLayoutSize.width && size.height === lastLayoutSize.height) return

  lastLayoutSize = size
  dock.layout(size.width, size.height)
}

function applyLayout() {
  const size = readLayoutSize()
  if (!size) return
  applyLayoutSize(size)
}

function handleResize(entry?: ResizeObserverEntry) {
  const size = entry ? readObservedSize(entry) : readLayoutSize()
  if (!size) return
  applyLayoutSize(size)
}

// The first-entry shell scales in from 1.15 to 1 (pages/main-section).
// dockview places panels rendered 'always' from page bounding boxes, which
// include that scale, and re-measures them only when a size changes; a
// transform changes none. A dock mounted mid-animation (the chat opens at once
// after sign-in) would keep those panels 1.15x wide and offset until the next
// resize, so re-measure once an ancestor's transform transition ends.
const TRANSFORM_PROPERTIES = new Set(['scale', 'transform', 'translate'])

function relayoutAfterAncestorTransform(event: TransitionEvent) {
  if (!TRANSFORM_PROPERTIES.has(event.propertyName)) return
  const el = rootEl.value
  const target = event.target
  if (!el || !(target instanceof Element) || target === el || !target.contains(el)) return
  const size = readLayoutSize()
  if (!size || !api.value) return
  lastLayoutSize = size
  api.value.layout(size.width, size.height, true)
}

// Panel components are looked up by name when panels are added or restored
// from a serialized layout. dockview-vue types frameworks components as
// DefineComponent<T> (single generic), which script-setup SFC types do not
// structurally match, hence the casts.
//
// Only the chat panel ships with the workspace, so the chat can render without
// waiting on the others: the file and asset panels carry the Monaco editor and
// the terminal carries xterm. The other panel types download in the background
// once the workspace is up (below), or when a panel of that type is created
// (opened, or restored from a saved layout) before that. Until its chunk
// arrives a panel shows PanelLoading (the editor's chunks are about 700KB
// gzip, several seconds on a slow network), or its failed state if the chunk
// can't be fetched. dockview passes later params updates as new props, which
// both wrappers forward once the panel has loaded.
//
// After a failed load, mounting the async component again runs its loader
// again (Vue drops the failed request), and Chrome fetches a dynamic import
// again after a failed fetch (other browsers not tested). The failed state's
// Retry button remounts by changing the key, so recovering one panel leaves
// the others, and any unsaved edits in them, untouched. A chunk removed by a
// deployment since the page loaded fails on every retry; the failed state
// also offers a page reload for that (see panel-loading.vue).
const PanelLoadFailed = () => h(PanelLoading, { failed: true })
const lazyPanel = (loader: AsyncComponentLoader) => {
  const Panel = defineAsyncComponent({
    loader,
    loadingComponent: PanelLoading,
    errorComponent: PanelLoadFailed,
  })
  return defineComponent({
    inheritAttrs: false,
    setup(_, { attrs }) {
      const attempt = ref(0)
      provide(retryPanelLoadKey, () => { attempt.value += 1 })
      return () => h(Panel, { ...attrs, key: attempt.value })
    },
  }) as unknown as VueComponent
}

// Every panel type is also downloaded in the background, after the small
// chunks a click is more likely to need (on the web, the settings pages) and
// only once the workspace's own first requests have finished; see
// lib/idle-prefetch.ts. Waiting here covers the workspace opening after
// sign-in, when the rest of the queue may have finished long before. The file
// panel goes first: opening a file is the most common of these, and it
// carries the editor.
// Wrapped with withStylesheetRecovery so a stylesheet that failed during the
// background download is loaded again when the panel opens or retries.
const panelLoaders = {
  file: withStylesheetRecovery(() => import('./dockview/panel-file.vue')),
  preview: withStylesheetRecovery(() => import('./dockview/panel-preview.vue')),
  asset: withStylesheetRecovery(() => import('./dockview/panel-asset.vue')),
  terminal: withStylesheetRecovery(() => import('./dockview/panel-terminal.vue')),
  browser: withStylesheetRecovery(() => import('./dockview/panel-browser.vue')),
  display: withStylesheetRecovery(() => import('./dockview/panel-display.vue')),
  schedule: withStylesheetRecovery(() => import('./dockview/panel-schedule.vue')),
}
onMounted(() => {
  void whenNetworkQuiet().then(() => prefetchLater(Object.values(panelLoaders)))
})

const panelComponents: Record<string, VueComponent> = {
  chat: PanelChat as unknown as VueComponent,
  ...Object.fromEntries(Object.entries(panelLoaders).map(([type, load]) => [type, lazyPanel(load)])),
}

const watermarkComponent = WorkspaceWatermark as unknown as VueComponent
const defaultTabComponent = WorkspaceTab as unknown as VueComponent
// "+" cluster: leftActions renders right after the tabs (so it hugs the last
// tab, Chrome-style), while Preview pins to rightActions at the strip's far
// right. The growing void between them is dockview's droppable empty header.
const leftHeaderActionsComponent = HeaderAddActions as unknown as VueComponent
const rightHeaderActionsComponent = GroupActions as unknown as VueComponent
const prefixHeaderActionsComponent = PrefixHeaderActions as unknown as VueComponent

const memohTheme: DockviewTheme = {
  name: 'memoh',
  className: 'dockview-theme-memoh',
  gap: 0,
  dndTabIndicator: 'line',
}

// Closes route through the store guard so dirty files prompt to save instead of
// discarding edits silently — close-others / close-all walk the dialog per file.
function getTabContextMenuItems({ panel, group }: GetTabContextMenuItemsParams): ContextMenuItem[] {
  return [
    {
      label: t('chat.tabMenu.close'),
      action: () => store.requestCloseTab(panel.id),
    },
    {
      label: t('chat.tabMenu.closeOthers'),
      disabled: group.panels.length <= 1,
      action: () => store.requestCloseTabs(
        group.panels.filter(other => other.id !== panel.id).map(other => other.id),
      ),
    },
    {
      label: t('chat.tabMenu.closeAll'),
      action: () => store.requestCloseTabs(group.panels.map(other => other.id)),
    },
  ]
}

function onReady(event: DockviewReadyEvent) {
  // registerApi owns restoration and the only empty-dock fallback. Keeping a
  // second "no chat panel" fallback here rewrites a valid file/preview-only
  // workspace by opening New Session into its ephemeral slot.
  store.registerApi(event.api)
  applyLayout()
}

// dockview-vue renders panel/tab/header-action components by mounting fresh
// vNodes whose appContext.provides is rebuilt from THIS component's own
// instance.provides (it spreads parent.provides). A plain `inject` in a child
// like prefix-header-actions therefore only sees what this component *provides*
// itself — the normal Vue provide chain (chat/App.vue → … → here) does NOT cross
// the manual mount boundary. DesktopShellKey is provided at the desktop chat root
// to enable the macOS traffic-light reserve; without re-providing it here, the
// dock's header-actions children read it as `false` and never reserve the gutter,
// so hiding the sidebar slides the tab strip straight under the traffic lights.
// Re-inject + re-provide so the value survives the dockview mount boundary.
const desktopShell = inject(DesktopShellKey, false)
provide(DesktopShellKey, desktopShell)
// Same boundary for the fullscreen bridge: without re-providing, the dock's
// header-actions read no DesktopWindowKey and keep the traffic-light gutter
// in fullscreen (see useMacTrafficReserve).
const desktopWindow = inject(DesktopWindowKey, undefined)
provide(DesktopWindowKey, desktopWindow)

provide(openInFileManagerKey, (path: string, isDir = false) => {
  const normalizedPath = normalizeFileManagerPath(path)
  if (isDir) {
    store.openFilesAt(normalizedPath)
  } else {
    store.openFilePinned(normalizedPath)
  }
})

provide(openAssetPreviewKey, args => store.openAsset(args))

onMounted(() => {
  resizeObserver = new ResizeObserver(([entry]) => {
    handleResize(entry)
  })
  if (rootEl.value) resizeObserver.observe(rootEl.value)
  document.addEventListener('transitionend', relayoutAfterAncestorTransform)
})

onBeforeUnmount(() => {
  document.removeEventListener('transitionend', relayoutAfterAncestorTransform)
  resizeObserver?.disconnect()
  resizeObserver = null
  store.releaseApi()
})
</script>
