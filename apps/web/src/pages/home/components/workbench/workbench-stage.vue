<template>
  <div
    ref="stageEl"
    class="pointer-events-none absolute inset-0 overflow-hidden"
  >
    <div
      v-for="tab in orderedTabs"
      v-show="isTabVisible(tab.id)"
      :key="tab.id"
      tabindex="-1"
      class="pointer-events-auto absolute isolate overflow-hidden outline-none"
      :data-workbench-panel-id="tab.id"
      :data-workbench-active-panel="store.activeId === tab.id ? 'true' : undefined"
      :style="panelBoxStyle(tab.id)"
      @pointerdown="onPanelPointerDown(tab.id)"
    >
      <ViewChat
        v-if="tab.target.kind === 'chat'"
        :tab-id="tab.id"
        :session-id="tab.target.sessionId"
        :visible="isTabVisible(tab.id)"
        :focused="store.activeId === tab.id"
      />

      <LazyFileView
        v-else-if="tab.target.kind === 'file'"
        :tab-id="tab.id"
        :file-path="tab.target.filePath"
        :visible="isTabVisible(tab.id)"
      />

      <LazyPreviewView
        v-else-if="tab.target.kind === 'preview'"
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

      <div
        v-else-if="tab.target.kind === 'terminal'"
        class="relative flex h-full w-full flex-col"
      >
        <div class="min-h-0 flex-1">
          <LazyTerminalPane
            v-if="revealedTabs[tab.id] && currentBotId"
            :key="`terminal-pane:${currentBotId}:${tab.id}`"
            :bot-id="currentBotId"
            :tab-id="tab.id"
            :active="isTabVisible(tab.id)"
          />
        </div>
      </div>

      <div
        v-else-if="tab.target.kind === 'browser'"
        class="relative flex h-full w-full flex-col"
      >
        <div class="min-h-0 flex-1">
          <LazyBrowserPane
            v-if="currentBotId"
            :bot-id="currentBotId"
            :tab-id="tab.id"
            :address="tab.target.address"
            :active="isTabVisible(tab.id)"
          />
        </div>
      </div>

      <div
        v-else-if="tab.target.kind === 'display'"
        class="relative flex h-full w-full flex-col"
      >
        <div class="min-h-0 flex-1">
          <LazyDisplayPane
            v-if="currentBotId"
            :key="currentBotId"
            :bot-id="currentBotId"
            :tab-id="tab.id"
            :title="store.resolveTabTitle(tab.id)"
            :active="isTabVisible(tab.id) && isChatRoute"
            @close="store.closeTab(tab.id)"
            @snapshot="handleDisplaySnapshot"
          />
        </div>
      </div>

      <LazyScheduleView
        v-else-if="tab.target.kind === 'schedule'"
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
  watch,
  type AsyncComponentLoader,
} from 'vue'
import { useRoute } from 'vue-router'
import { storeToRefs } from 'pinia'
import { useChatStore } from '@/store/chat-list'
import { useDisplaySnapshotsStore } from '@/store/display-snapshots'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { prefetchLater, whenNetworkQuiet } from '@/lib/idle-prefetch'
import { withStylesheetRecovery } from '@/lib/chunk-load-error'
import { listGroups } from '@/workbench/tree'
import type { WorkbenchTab } from '@/workbench/types'
import ViewChat from './view-chat.vue'
import WorkbenchPanelLoading from './workbench-panel-loading.vue'
import { retryPanelLoadKey } from './workbench-panel-load-state.vue'
import { PanelLoadFailed } from './lazy-panel-content'

const props = defineProps<{
  slotMap: Map<string, HTMLElement>
  slotVersion: number
}>()

const route = useRoute()
const store = useWorkspaceTabsStore()
const chatStore = useChatStore()
const displaySnapshots = useDisplaySnapshotsStore()
const { currentBotId } = storeToRefs(chatStore)

const isChatRoute = computed(() => route.name === 'home' || route.name === 'bot')

function lazyPanel<T extends AsyncComponentLoader>(loader: T) {
  const AsyncComp = defineAsyncComponent({
    loader,
    loadingComponent: WorkbenchPanelLoading,
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
const revealedTabs = ref<Record<string, true>>({})

// Stable sort by tab.id keeps DOM child order invariant across split drags and tab reorders.
const orderedTabs = computed<WorkbenchTab[]>(() =>
  Object.values(store.workbench.tabs).sort((a, b) => a.id.localeCompare(b.id)),
)

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

watch(
  visibleTabGroupMap,
  (map) => {
    let changed = false
    const next = { ...revealedTabs.value }
    for (const tabId of map.keys()) {
      if (!next[tabId]) {
        next[tabId] = true
        changed = true
      }
    }
    for (const existingId of Object.keys(next)) {
      if (!store.workbench.tabs[existingId]) {
        delete next[existingId]
        changed = true
      }
    }
    if (changed) revealedTabs.value = next
  },
  { immediate: true },
)

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
  const botId = currentBotId.value
  if (!botId) return
  displaySnapshots.upsert(botId, payload)
}

let resizeObserver: ResizeObserver | null = null

function syncSlotRects() {
  const root = stageEl.value
  if (!root || store.isMobile) return
  const rootRect = root.getBoundingClientRect()
  const scaleX = root.clientWidth > 0 ? rootRect.width / root.clientWidth : 1
  const scaleY = root.clientHeight > 0 ? rootRect.height / root.clientHeight : 1
  const sx = scaleX > 0 ? scaleX : 1
  const sy = scaleY > 0 ? scaleY : 1

  const next: Record<string, { left: number, top: number, width: number, height: number }> = {}
  for (const [groupId, slotEl] of props.slotMap.entries()) {
    if (!slotEl.isConnected) continue
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
  if (stageEl.value) resizeObserver.observe(stageEl.value)
  for (const slotEl of props.slotMap.values()) {
    if (slotEl.isConnected) resizeObserver.observe(slotEl)
  }
  syncSlotRects()
}

watch(
  [() => props.slotVersion, () => store.workbench.root.id, () => store.isMobile],
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
