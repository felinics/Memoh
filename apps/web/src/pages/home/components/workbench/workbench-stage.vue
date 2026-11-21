<template>
  <div
    ref="stageEl"
    class="pointer-events-none absolute inset-0 overflow-hidden"
  >
    <div
      v-for="tab in orderedTabs"
      v-show="isTabVisible(tab.id)"
      :key="`${stageBotId}:${tab.id}`"
      tabindex="-1"
      class="pointer-events-auto absolute isolate overflow-hidden bg-surface-editor outline-none"
      :class="fullscreenTabId === tab.id ? 'z-(--z-top)' : undefined"
      :data-workbench-panel-id="tab.id"
      :data-workbench-active-panel="store.activeId === tab.id ? 'true' : undefined"
      :style="panelBoxStyle(tab.id)"
      @pointerdown.capture="onPanelPointerDown(tab.id)"
    >
      <ViewChat
        v-if="tab.target.kind === 'chat'"
        :bot-id="stageBotId"
        :tab-id="tab.id"
        :session-id="tab.target.sessionId"
        :visible="isTabVisible(tab.id)"
        :focused="store.activeId === tab.id"
      />

      <LazyFileView
        v-else-if="tab.target.kind === 'file'"
        :bot-id="stageBotId"
        :tab-id="tab.id"
        :file-path="tab.target.filePath"
        :visible="isTabVisible(tab.id)"
      />

      <LazyPreviewView
        v-else-if="tab.target.kind === 'preview'"
        :bot-id="stageBotId"
        :file-path="tab.target.filePath"
        :visible="isTabVisible(tab.id)"
      />

      <LazyAssetView
        v-else-if="tab.target.kind === 'asset'"
        :name="tab.target.name"
        :bot-id="tab.target.botId"
        :content-hash="tab.target.contentHash"
        :src="tab.target.src"
        :visible="isTabVisible(tab.id)"
      />

      <LazyTerminalPane
        v-else-if="tab.target.kind === 'terminal' && revealedTerminalIds.has(tab.id)"
        :bot-id="stageBotId"
        :tab-id="tab.id"
        :active="isTabVisible(tab.id)"
      />

      <LazyBrowserPane
        v-else-if="tab.target.kind === 'browser'"
        :bot-id="stageBotId"
        :tab-id="tab.id"
        :address="tab.target.address"
        :active="isTabVisible(tab.id)"
      />

      <LazyDisplayPane
        v-else-if="tab.target.kind === 'display'"
        :bot-id="stageBotId"
        :tab-id="tab.id"
        :title="store.resolveTabTitle(tab.id)"
        :active="isTabVisible(tab.id) && isChatRoute"
        @close="store.closeTab(tab.id)"
        @snapshot="handleDisplaySnapshot"
        @fullscreen-change="onDisplayFullscreenChange(tab.id, $event)"
      />

      <LazyScheduleView
        v-else-if="tab.target.kind === 'schedule'"
        :bot-id="stageBotId"
        :tab-id="tab.id"
        :schedule-id="tab.target.scheduleId"
        :visible="isTabVisible(tab.id)"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import {
  computed,
  defineAsyncComponent,
  defineComponent,
  h,
  onBeforeUnmount,
  onMounted,
  provide,
  ref,
  shallowRef,
  watch,
  type AsyncComponentLoader,
} from 'vue'
import { useRoute } from 'vue-router'
import { useDisplaySnapshotsStore } from '@/store/display-snapshots'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { prefetchLater, whenNetworkQuiet } from '@/lib/idle-prefetch'
import { withStylesheetRecovery } from '@/lib/chunk-load-error'
import { getElementScale } from '@/workbench/drag'
import { listGroups } from '@/workbench/tree'
import type { WorkbenchNode, WorkbenchTab } from '@/workbench/types'
import ViewChat from './view-chat.vue'
import WorkbenchPanelLoadState, { retryPanelLoadKey } from './workbench-panel-load-state.vue'
import { PanelLoadFailed } from './lazy-panel-content'

const route = useRoute()
const store = useWorkspaceTabsStore()
const displaySnapshots = useDisplaySnapshotsStore()

const isChatRoute = computed(() => route.name === 'home' || route.name === 'bot')

function lazyPanel<T extends AsyncComponentLoader>(loader: T) {
  const AsyncComp = defineAsyncComponent({
    loader,
    loadingComponent: WorkbenchPanelLoadState,
    errorComponent: PanelLoadFailed,
  })
  return defineComponent({
    inheritAttrs: false,
    setup(_, { attrs }) {
      const attempt = ref(0)
      provide(retryPanelLoadKey, () => { attempt.value += 1 })
      return () => h(AsyncComp, { ...attrs, key: attempt.value })
    },
  })
}

const panelLoaders = {
  file: withStylesheetRecovery(() => import('./view-file.vue')),
  preview: withStylesheetRecovery(() => import('./view-preview.vue')),
  asset: withStylesheetRecovery(() => import('./view-asset.vue')),
  terminal: withStylesheetRecovery(() => import('../terminal-pane.vue')),
  browser: withStylesheetRecovery(() => import('../browser-pane.vue')),
  display: withStylesheetRecovery(() => import('../display-pane.vue')),
  schedule: withStylesheetRecovery(() => import('./view-schedule.vue')),
}

const LazyFileView = lazyPanel(panelLoaders.file)
const LazyPreviewView = lazyPanel(panelLoaders.preview)
const LazyAssetView = lazyPanel(panelLoaders.asset)
const LazyTerminalPane = lazyPanel(panelLoaders.terminal)
const LazyBrowserPane = lazyPanel(panelLoaders.browser)
const LazyDisplayPane = lazyPanel(panelLoaders.display)
const LazyScheduleView = lazyPanel(panelLoaders.schedule)

const stageEl = ref<HTMLElement | null>(null)
const slotRects = ref<Record<string, { left: number, top: number, width: number, height: number }>>({})
const revealedTerminalIds = shallowRef(new Set<string>())
const fullscreenTabId = ref<string | null>(null)

const stageBotId = computed(() => (store.loadedBotId ?? '').trim())

// Stable sort by tab.id keeps DOM child order invariant across split drags and tab reorders.
const orderedTabs = computed<WorkbenchTab[]>(() => {
  if (!stageBotId.value) return []
  return Object.values(store.workbench.tabs).sort((a, b) => a.id.localeCompare(b.id))
})

const visibleTabGroupMap = computed(() => {
  const map = new Map<string, string>()
  if (store.isMobile) {
    if (store.activeId && store.activeGroupId) {
      map.set(store.activeId, store.activeGroupId)
    }
    return map
  }
  for (const group of listGroups(store.workbench.root)) {
    if (group.activeTabId) {
      map.set(group.activeTabId, group.id)
    }
  }
  return map
})

function isTabVisible(tabId: string): boolean {
  return visibleTabGroupMap.value.has(tabId)
}

function treeLayoutKey(node: WorkbenchNode): string {
  return node.kind === 'group' ? node.id : `${node.id}(${node.axis}:${treeLayoutKey(node.first)},${treeLayoutKey(node.second)})`
}
const layoutShapeKey = computed(() => treeLayoutKey(store.workbench.root))

watch(
  [stageBotId, visibleTabGroupMap],
  ([botId, map], prev) => {
    const botChanged = prev !== undefined && botId !== prev[0]
    const prevSet = botChanged ? new Set<string>() : revealedTerminalIds.value
    const next = new Set<string>()
    let changed = botChanged && revealedTerminalIds.value.size > 0
    if (botId) {
      for (const tabId of prevSet) {
        if (store.workbench.tabs[tabId]?.target.kind === 'terminal') next.add(tabId)
        else changed = true
      }
      for (const tabId of map.keys()) {
        if (store.workbench.tabs[tabId]?.target.kind === 'terminal' && !next.has(tabId)) {
          next.add(tabId)
          changed = true
        }
      }
    } else if (revealedTerminalIds.value.size > 0) {
      changed = true
    }
    if (changed) revealedTerminalIds.value = next
  },
  { immediate: true },
)

watch(
  [stageBotId, isChatRoute, visibleTabGroupMap],
  ([bid, chatRoute], prev) => {
    const activeFullscreenId = fullscreenTabId.value
    if (!activeFullscreenId) return
    if ((prev && bid !== prev[0]) || !chatRoute || !isTabVisible(activeFullscreenId) || !store.workbench.tabs[activeFullscreenId]) {
      fullscreenTabId.value = null
    }
  },
)

function onDisplayFullscreenChange(tabId: string, active: boolean) {
  if (active) {
    fullscreenTabId.value = tabId
  } else if (fullscreenTabId.value === tabId) {
    fullscreenTabId.value = null
  }
}

function panelBoxStyle(tabId: string): Record<string, string> {
  if (store.isMobile) {
    return { left: '0px', top: '0px', width: '100%', height: '100%' }
  }
  const groupId = visibleTabGroupMap.value.get(tabId)
  const rect = groupId ? slotRects.value[groupId] : undefined
  if (!rect) {
    return { left: '0px', top: '0px', width: '100%', height: '100%' }
  }
  return {
    left: `${rect.left}px`,
    top: `${rect.top}px`,
    width: `${rect.width}px`,
    height: `${rect.height}px`,
  }
}

function onPanelPointerDown(tabId: string) {
  if (store.activeId !== tabId) {
    store.activateTab(tabId)
  }
}

function handleDisplaySnapshot(payload: { tabId: string, sessionId?: string, dataUrl: string }) {
  if (!stageBotId.value) return
  displaySnapshots.upsert(stageBotId.value, payload)
}

let resizeObserver: ResizeObserver | null = null

function queryGroupSlots(): HTMLElement[] {
  const container = stageEl.value?.parentElement
  if (!container) return []
  return Array.from(container.querySelectorAll<HTMLElement>('[data-workbench-slot-group-id]:not([data-workbench-empty-slot="true"])'))
}

function syncSlotRects() {
  const root = stageEl.value
  if (!root || store.isMobile) return
  const rootRect = root.getBoundingClientRect()
  if (rootRect.width <= 0 && rootRect.height <= 0) return
  // MainSection mounts with a CSS scale-[1.15] login/onboarding entry transition that does not re-fire ResizeObserver when it settles.
  const { x: sx, y: sy } = getElementScale(root, rootRect)

  const next: Record<string, { left: number, top: number, width: number, height: number }> = {}
  for (const slotEl of queryGroupSlots()) {
    const groupId = slotEl.dataset.workbenchSlotGroupId
    if (!groupId || !slotEl.isConnected) continue
    const sr = slotEl.getBoundingClientRect()
    next[groupId] = {
      left: Math.round(((sr.left - rootRect.left) / sx) * 2) / 2,
      top: Math.round(((sr.top - rootRect.top) / sy) * 2) / 2,
      width: Math.max(0, Math.round((sr.width / sx) * 2) / 2),
      height: Math.max(0, Math.round((sr.height / sy) * 2) / 2),
    }
  }
  slotRects.value = next
}

function rebuildObservations() {
  if (!resizeObserver) return
  resizeObserver.disconnect()
  if (store.isMobile) return
  if (stageEl.value) resizeObserver.observe(stageEl.value)
  for (const slotEl of queryGroupSlots()) {
    resizeObserver.observe(slotEl)
  }
  syncSlotRects()
}

watch(
  [layoutShapeKey, () => store.isMobile, () => orderedTabs.value.length > 0],
  () => rebuildObservations(),
  { flush: 'post' },
)

onMounted(() => {
  void whenNetworkQuiet().then(() => prefetchLater([
    panelLoaders.file,
    panelLoaders.preview,
    panelLoaders.asset,
    panelLoaders.browser,
    panelLoaders.display,
    panelLoaders.schedule,
  ]))
  resizeObserver = new ResizeObserver(() => syncSlotRects())
  rebuildObservations()
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  resizeObserver = null
})
</script>
