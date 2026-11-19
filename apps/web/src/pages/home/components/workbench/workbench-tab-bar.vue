<template>
  <DefineTabMenu v-slot="{ tab }">
    <ContextMenuContent class="w-48">
      <ContextMenuItem
        v-if="!tab.pinned && group.previewTabId === tab.id"
        @select="store.keepOpenTab(tab.id)"
      >
        {{ t('chat.tabMenu.keepOpen') }}
      </ContextMenuItem>
      <ContextMenuItem @select="tab.pinned ? store.unpinTab(tab.id) : store.pinTab(tab.id)">
        {{ tab.pinned ? t('chat.tabMenu.unpin') : t('chat.tabMenu.pin') }}
      </ContextMenuItem>
      <template v-if="!isMobile">
        <ContextMenuSeparator />
        <ContextMenuItem @select="activateAndSplit(tab.id, 'right')">
          {{ t('chat.tabBarToolkit.splitRight') }}
        </ContextMenuItem>
        <ContextMenuItem @select="activateAndSplit(tab.id, 'below')">
          {{ t('chat.tabBarToolkit.splitDown') }}
        </ContextMenuItem>
      </template>
      <ContextMenuSeparator />
      <ContextMenuItem @select="store.requestCloseTab(tab.id)">
        {{ t('chat.tabMenu.close') }}
      </ContextMenuItem>
      <ContextMenuItem
        :disabled="!hasClosableOthers(tab.id)"
        @select="store.closeOtherTabs(group.id, tab.id)"
      >
        {{ t('chat.tabMenu.closeOthers') }}
      </ContextMenuItem>
      <ContextMenuItem
        :disabled="!hasClosableRight(tab.id)"
        @select="store.closeTabsToRight(group.id, tab.id)"
      >
        {{ t('chat.tabMenu.closeToRight') }}
      </ContextMenuItem>
      <ContextMenuItem
        :disabled="unpinnedTabs.length === 0"
        @select="store.closeAllTabsInGroup(group.id)"
      >
        {{ t('chat.tabMenu.closeAll') }}
      </ContextMenuItem>
    </ContextMenuContent>
  </DefineTabMenu>

  <header
    class="workbench-header flex w-full shrink-0 items-center overflow-hidden"
    :data-workbench-header-group-id="group.id"
    :data-last-tab-active="isLastRegularTabActive ? 'true' : undefined"
    @pointerdown="store.activateGroup(group.id)"
    @pointermove="clearHoverLock"
    @pointerleave="scheduleUnlockWidth"
    @pointerenter="cancelUnlockWidth"
  >
    <!-- Spatial top-left group navigation & macOS traffic-light gutter -->
    <WorkbenchPrefixNav v-if="isSpatialFirstGroup && !isMobile" />

    <!-- Non-scrolling left pinned tabs segment -->
    <div
      v-if="pinnedTabs.length > 0"
      data-workbench-pinned-zone
      class="flex h-full shrink-0 items-end pl-(--wb-edge-pad) pr-0"
    >
      <TransitionGroup
        name="wb-tab"
        tag="div"
        class="workbench-tab-track relative flex h-full items-end"
        @before-leave="onBeforeTabLeave"
      >
        <!-- ui-allow-z: tab-local stacking order (crown/foot -> label -> dot) -->
        <div
          v-for="tab in pinnedTabs"
          :key="tab.id"
          class="workbench-tab workbench-tab-pinned group/tab z-[1] flex"
          :data-workbench-tab-id="tab.id"
          :data-group-active="group.activeTabId === tab.id ? 'true' : undefined"
          :data-dragging-rail="isRailDragged(tab.id) ? 'true' : undefined"
          :data-dragging-detached="isDetachedDragged(tab.id) ? 'true' : undefined"
        >
          <ContextMenu>
            <ContextMenuTrigger as-child>
              <div
                class="workbench-tab-hit flex size-full min-w-0 items-center gap-1.5"
                role="tab"
                tabindex="0"
                :aria-selected="group.activeTabId === tab.id"
                :title="store.resolveTabTitle(tab.id)"
                @pointerdown="onTabPointerDown($event, tab)"
                @dblclick.stop="store.unpinTab(tab.id)"
                @auxclick.middle.prevent="onCloseTab($event, tab.id, true, false)"
                @keydown.enter.exact.prevent="store.activateTab(tab.id)"
                @keydown.space.prevent="store.activateTab(tab.id)"
              >
                <span
                  v-if="group.activeTabId === tab.id"
                  class="workbench-tab-crown"
                  aria-hidden="true"
                />
                <span
                  v-if="group.activeTabId === tab.id"
                  class="workbench-tab-foot"
                  aria-hidden="true"
                />
                <!-- ui-allow-z: tab-local icon, label, and dirty dot above active crown/foot -->
                <Pin class="relative z-[1] size-3 shrink-0 text-muted-foreground" />
                <span
                  class="relative z-[1] min-w-0 flex-1 truncate text-label leading-[1.3]"
                  :class="group.activeTabId === tab.id ? 'text-foreground' : 'text-muted-foreground'"
                >{{ store.resolveTabTitle(tab.id) }}</span>
                <span
                  v-if="store.fileDirty[tab.id]"
                  class="relative z-[1] size-1.5 shrink-0 rounded-full"
                  :class="group.activeTabId === tab.id ? 'bg-foreground' : 'bg-muted-foreground'"
                />
              </div>
            </ContextMenuTrigger>
            <ReuseTabMenu :tab="tab" />
          </ContextMenu>
        </div>
      </TransitionGroup>
    </div>

    <!-- Scrollable regular tabs segment (Flexbox + Chrome consecutive-close width lock) -->
    <div
      ref="scrollerEl"
      data-workbench-tab-scroller
      class="workbench-tab-scroller flex h-full min-w-0 shrink items-end overflow-x-auto"
      :data-width-locked="lockedTabWidthPx !== null ? 'true' : undefined"
      :data-fade-left="scrollFadeLeft ? 'true' : undefined"
      :data-fade-right="scrollFadeRight ? 'true' : undefined"
      :style="scrollerLockStyle"
      @scroll.passive="syncScrollEdges"
      @wheel="onScrollerWheel"
    >
      <TransitionGroup
        name="wb-tab"
        tag="div"
        class="workbench-tab-track relative flex h-full items-end"
        :style="trackSpacerStyle"
        @before-leave="onBeforeTabLeave"
      >
        <!-- ui-allow-z: tab-local stacking order (crown/foot -> label -> dot/close action) -->
        <div
          v-for="tab in unpinnedTabs"
          :key="tab.id"
          class="workbench-tab group/tab z-[1] flex"
          :data-workbench-tab-id="tab.id"
          :data-tab-dirty="store.fileDirty[tab.id] ? 'true' : undefined"
          :data-hover-locked="hoverLockedTabId === tab.id ? 'true' : undefined"
          :data-close-hover-locked="closeHoverLockedTabId === tab.id ? 'true' : undefined"
          :data-group-active="group.activeTabId === tab.id ? 'true' : undefined"
          :data-dragging-rail="isRailDragged(tab.id) ? 'true' : undefined"
          :data-dragging-detached="isDetachedDragged(tab.id) ? 'true' : undefined"
        >
          <ContextMenu>
            <ContextMenuTrigger as-child>
              <div
                class="workbench-tab-hit flex size-full min-w-0 items-center"
                role="tab"
                tabindex="0"
                :aria-selected="group.activeTabId === tab.id"
                :title="store.resolveTabTitle(tab.id)"
                @pointerdown="onTabPointerDown($event, tab)"
                @click="onTabHitClick($event)"
                @dblclick.stop="store.keepOpenTab(tab.id)"
                @auxclick.middle.prevent="onCloseTab($event, tab.id, false, false)"
                @keydown.enter.exact.prevent="store.activateTab(tab.id)"
                @keydown.space.prevent="store.activateTab(tab.id)"
              >
                <span
                  v-if="group.activeTabId === tab.id"
                  class="workbench-tab-crown"
                  aria-hidden="true"
                />
                <span
                  v-if="group.activeTabId === tab.id"
                  class="workbench-tab-foot"
                  aria-hidden="true"
                />
                <!-- ui-allow-z: tab-local label above active crown/foot -->
                <span
                  class="workbench-tab-title relative z-[1] min-w-0 flex-1 overflow-hidden text-label leading-[1.3] whitespace-nowrap transition-colors"
                  :class="group.activeTabId === tab.id ? 'text-foreground' : 'text-muted-foreground'"
                >{{ store.resolveTabTitle(tab.id) }}</span>

                <!-- Unsaved dot at rest in the exact close button slot -->
                <!-- ui-allow-z: tab-local indicator above label -->
                <div
                  v-if="store.fileDirty[tab.id]"
                  class="workbench-tab-action is-dot pointer-events-none absolute z-[2] flex items-center"
                >
                  <span class="flex size-5 items-center justify-center">
                    <span
                      class="size-1.75 rounded-full"
                      :class="group.activeTabId === tab.id ? 'bg-foreground' : 'bg-muted-foreground'"
                    />
                  </span>
                </div>

                <!-- Zero-background close button -->
                <!-- ui-allow-z: tab-local close control above label and dot -->
                <div class="workbench-tab-action pointer-events-none absolute z-[2] flex items-center">
                  <Button
                    variant="ghost"
                    tone="muted"
                    size="icon-sm"
                    shape="circle"
                    data-workbench-tab-close
                    class="pointer-events-auto size-5 shrink-0 p-0"
                    :aria-label="t('chat.tabMenu.close')"
                    @pointerdown.stop
                    @mousedown.stop
                    @dblclick.stop
                    @click.stop.prevent="onCloseTab($event, tab.id, false, true)"
                  >
                    <X class="size-3.5" />
                  </Button>
                </div>
              </div>
            </ContextMenuTrigger>
            <ReuseTabMenu :tab="tab" />
          </ContextMenu>
        </div>
      </TransitionGroup>
    </div>

    <!-- "+" action menu hugging the end of the tab strip -->
    <div
      v-if="hasAnyAddAction"
      class="workbench-add-actions flex h-full shrink-0 items-center [-webkit-app-region:no-drag]"
    >
      <DropdownMenu>
        <DropdownMenuTrigger as-child>
          <Button
            variant="ghost"
            tone="muted"
            size="icon-sm"
            shape="circle"
            class="size-7 shrink-0 p-0"
            :title="t('chat.tabBarToolkit.openMenu')"
            :aria-label="t('chat.tabBarToolkit.openMenu')"
          >
            <AddIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start">
          <DropdownMenuItem
            v-if="currentBotId"
            @select="store.openDraftChat({ title: t('chat.newSession'), groupId: group.id, explicitSelection: false })"
          >
            <MessageCircle />
            {{ t('sidebar.chat') }}
          </DropdownMenuItem>
          <DropdownMenuItem
            v-if="canWorkspaceExec"
            @select="store.openTerminal(group.id)"
          >
            <TerminalIcon />
            {{ t('chat.tabBarToolkit.newTerminal') }}
          </DropdownMenuItem>
          <DropdownMenuItem
            v-if="canManage"
            @select="store.openBrowser(group.id)"
          >
            <BrowserIcon />
            {{ t('chat.tabBarToolkit.openBrowser') }}
          </DropdownMenuItem>
          <DropdownMenuItem
            v-if="canManage"
            @select="store.openDisplay(group.id)"
          >
            <ComputerIcon />
            {{ t('chat.tabBarToolkit.openDesktop') }}
          </DropdownMenuItem>
          <template v-if="group.activeTabId && !isMobile">
            <DropdownMenuSeparator v-if="currentBotId || canWorkspaceExec || canManage" />
            <DropdownMenuItem @select="store.splitGroup(group.id, 'right')">
              <SplitRightIcon />
              {{ t('chat.tabBarToolkit.splitRight') }}
            </DropdownMenuItem>
            <DropdownMenuItem @select="store.splitGroup(group.id, 'below')">
              <SplitDownIcon />
              {{ t('chat.tabBarToolkit.splitDown') }}
            </DropdownMenuItem>
          </template>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>

    <!-- Flexible window-drag & header-drop void -->
    <div
      class="h-full min-w-4 flex-1"
      :class="panelDragging ? '[-webkit-app-region:no-drag]' : '[-webkit-app-region:drag]'"
    />

    <!-- Right-aligned group action: open Markdown/HTML preview to the side -->
    <div
      v-if="previewFilePath"
      class="flex h-full shrink-0 items-center pr-2 [-webkit-app-region:no-drag]"
    >
      <Button
        variant="ghost"
        tone="muted"
        size="icon-sm"
        shape="circle"
        class="size-7 shrink-0 p-0"
        :title="t('chat.openPreviewToSide')"
        :aria-label="t('chat.openPreviewToSide')"
        @click.stop="store.openPreview(previewFilePath, group.id)"
      >
        <Columns2 class="size-3.5" />
      </Button>
    </div>
  </header>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { createReusableTemplate } from '@vueuse/core'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { Columns2, MessageCircle, Pin, X } from 'lucide-vue-next'
import {
  AddIcon,
  BrowserIcon,
  ComputerIcon,
  SplitDownIcon,
  SplitRightIcon,
  TerminalIcon,
} from '@memohai/icon/ui'
import {
  Button,
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@felinic/ui'
import { isHtmlFile, isMarkdownFile } from '@/components/file-manager/utils'
import { useChatStore } from '@/store/chat-list'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { hasBotPermission } from '@/utils/bot-permissions'
import { workbenchFileBaseName } from '@/workbench/titles'
import { findGroup } from '@/workbench/tree'
import { activeWorkbenchDrag, startWorkbenchTabPointerGesture } from '@/workbench/drag'
import type { WorkbenchGroupNode, WorkbenchTab } from '@/workbench/types'
import WorkbenchPrefixNav from './workbench-prefix-nav.vue'

const props = defineProps<{
  group: WorkbenchGroupNode
}>()

const [DefineTabMenu, ReuseTabMenu] = createReusableTemplate<{ tab: WorkbenchTab }>({ inheritAttrs: false })

const { t } = useI18n()
const store = useWorkspaceTabsStore()
const { isMobile, panelDragging } = storeToRefs(store)
const chatStore = useChatStore()
const { currentBotId, bots } = storeToRefs(chatStore)

const scrollerEl = ref<HTMLElement | null>(null)
const scrollFadeLeft = ref(false)
const scrollFadeRight = ref(false)
let scrollerRo: ResizeObserver | null = null

function syncScrollEdges() {
  const el = scrollerEl.value
  if (!el) {
    scrollFadeLeft.value = false
    scrollFadeRight.value = false
    return
  }
  scrollFadeLeft.value = el.scrollLeft > 2
  scrollFadeRight.value = el.scrollLeft + el.clientWidth < el.scrollWidth - 2
}

const isSpatialFirstGroup = computed(() => props.group.id === store.spatialFirstGroupId)

const currentBot = computed(() =>
  bots.value.find(bot => bot.id === currentBotId.value) ?? null,
)
const currentPermissions = computed(() => currentBot.value?.current_user_permissions ?? [])
const canWorkspaceRead = computed(() => hasBotPermission(currentPermissions.value, 'workspace_read'))
const canWorkspaceExec = computed(() => hasBotPermission(currentPermissions.value, 'workspace_exec'))
const canManage = computed(() => hasBotPermission(currentPermissions.value, 'manage'))

const previewFilePath = computed<string | null>(() => {
  if (!canWorkspaceRead.value || !props.group.activeTabId) return null
  const tab = store.workbench.tabs[props.group.activeTabId]
  if (!tab || tab.target.kind !== 'file') return null
  const name = workbenchFileBaseName(tab.target.filePath)
  return isMarkdownFile(name) || isHtmlFile(name) ? tab.target.filePath : null
})

const hasAnyAddAction = computed(() =>
  Boolean(currentBotId.value) || canWorkspaceExec.value || canManage.value || (Boolean(props.group.activeTabId) && !isMobile.value),
)

const groupTabs = computed<WorkbenchTab[]>(() =>
  props.group.tabIds
    .map(id => store.workbench.tabs[id])
    .filter((tab): tab is WorkbenchTab => Boolean(tab)),
)

const pinnedTabs = computed(() => groupTabs.value.filter(tab => tab.pinned))
const unpinnedTabs = computed(() => groupTabs.value.filter(tab => !tab.pinned))
const isLastRegularTabActive = computed(() => {
  const last = unpinnedTabs.value.at(-1) ?? pinnedTabs.value.at(-1)
  return Boolean(last && props.group.activeTabId === last.id)
})

function hasClosableOthers(tabId: string): boolean {
  return props.group.tabIds.some(id => id !== tabId && !store.workbench.tabs[id]?.pinned)
}

function hasClosableRight(tabId: string): boolean {
  const idx = props.group.tabIds.indexOf(tabId)
  if (idx < 0) return false
  return props.group.tabIds.slice(idx + 1).some(id => !store.workbench.tabs[id]?.pinned)
}

function activateAndSplit(tabId: string, direction: 'right' | 'below') {
  store.activateTab(tabId)
  store.splitGroup(props.group.id, direction)
}

// ---- Chrome consecutive-close width lock (`--tab-lock-w`) ----------------
const lockedTabWidthPx = ref<number | null>(null)
const lockedSpacerPx = ref(0)
const hoverLockedTabId = ref<string | null>(null)
const closeHoverLockedTabId = ref<string | null>(null)
let hoverLockX = 0
let hoverLockY = 0
let unlockTimer: ReturnType<typeof setTimeout> | null = null
let closingViaStripButton = false

const scrollerLockStyle = computed(() => (
  lockedTabWidthPx.value !== null ? { '--tab-lock-w': `${lockedTabWidthPx.value}px` } : {}
))

const trackSpacerStyle = computed(() => (
  lockedSpacerPx.value > 0 ? { paddingRight: `${lockedSpacerPx.value}px` } : undefined
))

function isStationaryCloseClick(event: MouseEvent): boolean {
  return (
    closeHoverLockedTabId.value !== null
    && event.button === 0
    && Math.hypot(event.clientX - hoverLockX, event.clientY - hoverLockY) <= 4
  )
}

function clearHoverLock(event?: PointerEvent) {
  if (!hoverLockedTabId.value && !closeHoverLockedTabId.value) return
  if (event && Math.hypot(event.clientX - hoverLockX, event.clientY - hoverLockY) <= 2) return
  hoverLockedTabId.value = null
  closeHoverLockedTabId.value = null
}

function clearWidthLock() {
  if (unlockTimer !== null) {
    clearTimeout(unlockTimer)
    unlockTimer = null
  }
  lockedTabWidthPx.value = null
  lockedSpacerPx.value = 0
  hoverLockedTabId.value = null
  closeHoverLockedTabId.value = null
}

function cancelUnlockWidth() {
  if (unlockTimer !== null) {
    clearTimeout(unlockTimer)
    unlockTimer = null
  }
}

function scheduleUnlockWidth() {
  hoverLockedTabId.value = null
  closeHoverLockedTabId.value = null
  if (lockedTabWidthPx.value === null) return
  cancelUnlockWidth()
  unlockTimer = setTimeout(() => {
    unlockTimer = null
    clearWidthLock()
  }, 120)
}

// Unlock immediately when a brand-new tab is added to this group while locked.
watch(
  () => unpinnedTabs.value.length,
  (nextLen, prevLen) => {
    if (lockedTabWidthPx.value !== null && nextLen > prevLen && !closingViaStripButton) {
      clearWidthLock()
    }
    void nextTick(syncScrollEdges)
  },
)

watch(lockedTabWidthPx, () => {
  void nextTick(syncScrollEdges)
})

onMounted(() => {
  syncScrollEdges()
  if (typeof ResizeObserver !== 'undefined' && scrollerEl.value) {
    scrollerRo = new ResizeObserver(() => syncScrollEdges())
    scrollerRo.observe(scrollerEl.value)
  }
})

onBeforeUnmount(() => {
  clearWidthLock()
  scrollerRo?.disconnect()
  scrollerRo = null
})

function onCloseTab(event: MouseEvent, tabId: string, pinned: boolean, fromCloseButton: boolean) {
  if (!pinned && !store.fileDirty[tabId] && unpinnedTabs.value.length >= 1) {
    const idx = unpinnedTabs.value.findIndex(t => t.id === tabId)
    const fallbackEl = scrollerEl.value
      ? Array.from(scrollerEl.value.querySelectorAll<HTMLElement>('[data-workbench-tab-id]'))
          .find(el => el.dataset.workbenchTabId === tabId) ?? null
      : null
    const tabEl = (event.currentTarget as HTMLElement | null)?.closest<HTMLElement>('[data-workbench-tab-id]') ?? fallbackEl
    if (tabEl) {
      cancelUnlockWidth()
      const measured = lockedTabWidthPx.value ?? tabEl.getBoundingClientRect().width
      if (measured > 0 && unpinnedTabs.value.length > 1) {
        lockedTabWidthPx.value = measured
        if (scrollerEl.value && scrollerEl.value.scrollLeft > 0) {
          lockedSpacerPx.value += measured
        }
      }
      const nextUnderCursor = idx >= 0 && idx < unpinnedTabs.value.length - 1
        ? unpinnedTabs.value[idx + 1]?.id ?? null
        : null
      hoverLockedTabId.value = nextUnderCursor
      closeHoverLockedTabId.value = fromCloseButton && nextUnderCursor && !store.fileDirty[nextUnderCursor]
        ? nextUnderCursor
        : null
      hoverLockX = event.clientX
      hoverLockY = event.clientY
    }
  }
  closingViaStripButton = true
  try {
    store.requestCloseTab(tabId)
  } finally {
    closingViaStripButton = false
  }
}

function onTabHitClick(event: MouseEvent) {
  if (!isStationaryCloseClick(event)) return
  const targetId = closeHoverLockedTabId.value
  if (!targetId) return
  event.stopPropagation()
  event.preventDefault()
  onCloseTab(event, targetId, false, true)
}

function onBeforeTabLeave(el: Element) {
  const node = el as HTMLElement
  const width = !node.classList.contains('workbench-tab-pinned') && lockedTabWidthPx.value !== null
    ? lockedTabWidthPx.value
    : node.getBoundingClientRect().width
  node.style.setProperty('--leave-w', `${width}px`)
}

function onScrollerWheel(event: WheelEvent) {
  const el = scrollerEl.value
  if (!el || el.scrollWidth <= el.clientWidth) return
  if (Math.abs(event.deltaY) > Math.abs(event.deltaX)) {
    event.preventDefault()
    el.scrollLeft += event.deltaY
  }
}

function isRailDragged(tabId: string): boolean {
  const rail = activeWorkbenchDrag.value?.railTab
  return Boolean(rail && rail.groupId === props.group.id && rail.tabId === tabId)
}

function isDetachedDragged(tabId: string): boolean {
  return activeWorkbenchDrag.value?.detachedTabId === tabId
}

function onTabPointerDown(event: PointerEvent, tab: WorkbenchTab) {
  if (isStationaryCloseClick(event)) {
    event.stopPropagation()
    return
  }
  startWorkbenchTabPointerGesture({
    event,
    tabId: tab.id,
    groupId: props.group.id,
    pinned: tab.pinned,
    getTitle: () => store.resolveTabTitle(tab.id),
    isMobile: () => store.isMobile,
    getGroupTabIds: gid => findGroup(store.workbench.root, gid)?.tabIds ?? [],
    isTabPinned: tid => Boolean(store.workbench.tabs[tid]?.pinned),
    onActivate: () => store.activateTab(tab.id),
    onDragStateChange: (dragging) => {
      clearWidthLock()
      store.panelDragging = dragging
    },
    onReorderInGroup: (gid, tid, idx) => store.reorderTab(gid, tid, idx),
    onCommitDrop: (tid, targetGid, zone, idx) => store.moveTab(tid, targetGid, zone, idx),
  })
}
</script>
