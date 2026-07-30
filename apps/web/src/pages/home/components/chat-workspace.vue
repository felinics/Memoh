<template>
  <div
    ref="rootEl"
    class="flex flex-col flex-1 h-full min-w-0 bg-background"
    :class="{ 'memoh-dock-dragging': store.panelDragging }"
  >
    <DockviewVue
      class="h-full w-full"
      :components="panelComponents"
      :tab-components="tabComponents"
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
import { inject, onBeforeUnmount, onMounted, provide, ref } from 'vue'
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
import { DesktopShellKey } from '@/lib/desktop-shell'
import PanelChat from './dockview/panel-chat.vue'
import PanelFile from './dockview/panel-file.vue'
import PanelPreview from './dockview/panel-preview.vue'
import PanelAsset from './dockview/panel-asset.vue'
import PanelTerminal from './dockview/panel-terminal.vue'
import PanelBrowser from './dockview/panel-browser.vue'
import PanelDisplay from './dockview/panel-display.vue'
import PanelSchedule from './dockview/panel-schedule.vue'
import WorkspaceWatermark from './dockview/workspace-watermark.vue'
import WorkspaceTabHost from './dockview/workspace-tab-host.vue'
import TerminalTab from './dockview/terminal-tab.vue'
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

// Panel components are looked up by name when panels are added or restored
// from a serialized layout. dockview-vue types frameworks components as
// DefineComponent<T> (single generic), which script-setup SFC types do not
// structurally match, hence the casts.
const panelComponents: Record<string, VueComponent> = {
  chat: PanelChat as unknown as VueComponent,
  file: PanelFile as unknown as VueComponent,
  preview: PanelPreview as unknown as VueComponent,
  asset: PanelAsset as unknown as VueComponent,
  terminal: PanelTerminal as unknown as VueComponent,
  browser: PanelBrowser as unknown as VueComponent,
  display: PanelDisplay as unknown as VueComponent,
  schedule: PanelSchedule as unknown as VueComponent,
}

const watermarkComponent = WorkspaceWatermark as unknown as VueComponent
const tabComponents: Record<string, VueComponent> = {
  terminalTab: TerminalTab as unknown as VueComponent,
}
const defaultTabComponent = WorkspaceTabHost as unknown as VueComponent
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

const FILE_MANAGER_ROOT = '/data'

function normalizeFileManagerPath(path: string): string {
  const trimmedPath = path.trim()
  if (!trimmedPath) return FILE_MANAGER_ROOT
  if (trimmedPath === FILE_MANAGER_ROOT || trimmedPath.startsWith(`${FILE_MANAGER_ROOT}/`)) {
    return trimmedPath
  }
  if (trimmedPath === '/') return FILE_MANAGER_ROOT
  if (trimmedPath.startsWith('/')) {
    return `${FILE_MANAGER_ROOT}${trimmedPath}`
  }
  return `${FILE_MANAGER_ROOT}/${trimmedPath}`
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

provide(openInFileManagerKey, (path: string, isDir = false) => {
  const normalizedPath = normalizeFileManagerPath(path)
  if (isDir) {
    store.openFilesAt(normalizedPath)
  } else {
    store.openFile(normalizedPath)
  }
})

provide(openAssetPreviewKey, args => store.openAsset(args))

onMounted(() => {
  resizeObserver = new ResizeObserver(([entry]) => {
    handleResize(entry)
  })
  if (rootEl.value) resizeObserver.observe(rootEl.value)
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  resizeObserver = null
  store.releaseApi()
})
</script>
