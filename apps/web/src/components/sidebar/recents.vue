<template>
  <div class="flex flex-col h-full min-w-0">
    <!-- Unified Recents: one timeline for chats, ACP chats, and schedule
         runs (each row carries its own type mark — see session-item.vue).
         The header folds the section, mirroring the Folders header above. -->
    <div class="shrink-0 px-2 pb-0.5 pt-1">
      <TextButton
        :class="sectionHeaderClass"
        @click="toggleSectionCollapsed"
      >
        {{ t('chat.recents') }}
        <ChevronDown
          class="size-2.5 transition-transform"
          :class="sectionCollapsed ? '-rotate-90' : ''"
        />
      </TextButton>
    </div>

    <div
      v-show="!sectionCollapsed"
      class="flex-1 relative min-h-0"
    >
      <!-- Native overflow scroll (not Reka ScrollArea) so the virtualizer can
           own the real scroll element. A bot's session list is unbounded — IM
           channels mint one session per chat/group — so rendering every row
           (each carrying a dropdown menu) froze the main thread for seconds when
           switching between two busy bots. Windowing keeps the mounted row count
           tied to the viewport, not the list length. -->
      <div
        ref="scrollEl"
        class="absolute inset-0 overflow-y-auto sidebar-scroll"
      >
        <div class="px-2 pr-3">
          <div
            v-if="visibleSessions.length > 0"
            :style="{ position: 'relative', width: '100%', height: `${totalSize}px` }"
          >
            <!-- pb-[2px] is the seam: the pill (SessionItem) keeps its own fill,
                 and the measured wrapper adds a thin transparent gap below it so
                 adjacent rows read as separate items instead of one block. Two
                 rows span 2×34px pill + 2px seam = 70px hover-top to hover-bottom. -->
            <div
              v-for="vRow in virtualRows"
              :key="vRow.key"
              :ref="measureRow"
              :data-index="vRow.index"
              class="pb-[2px]"
              :style="{ position: 'absolute', top: '0', left: '0', width: '100%', transform: `translateY(${vRow.start}px)` }"
            >
              <SessionItem
                :session="vRow.session"
                :is-active="sessionId === vRow.session.id"
                :streaming="chatStore.isSessionStreaming(currentBotId, vRow.session.id)"
                @select="handleSelect"
                @open-new-tab="handleOpenNewTab"
                @rename="sessionDialogs?.openRename($event)"
                @delete="sessionDialogs?.openDelete($event, { fallbackMode: 'recent' })"
              />
            </div>
          </div>

          <!-- Sentinel + bottom loader for cursor-paginated session history.
               Sits AFTER the virtualizer's height spacer so it lives at the
               natural end of the scroll content; the IntersectionObserver
               below uses scrollEl as its root and triggers the next page
               200px before the user actually reaches the bottom.
               Mounted only once the list overflows the viewport — see
               shouldShowLoadMoreSentinel — so a short first paint cannot
               falsely intersect and kick a page fetch that grows the spacer. -->
          <div
            v-if="showLoadMoreSentinel"
            ref="loadMoreSentinel"
            data-testid="load-more-sentinel"
            class="h-px w-full"
          />

          <div
            v-if="loadingMoreSessions"
            class="flex justify-center py-3"
          >
            <Spinner class="size-4" />
          </div>

          <div
            v-if="currentBotId && !loadingChats && visibleSessions.length === 0"
            class="px-3 py-6 text-center text-xs text-muted-foreground"
          >
            {{ t('chat.noSessions') }}
          </div>

          <div
            v-if="loadingChats"
            class="flex justify-center py-3"
          >
            <Spinner class="size-4" />
          </div>
        </div>
      </div>
    </div>

    <SessionDialogs ref="sessionDialogs" />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, nextTick, watch } from 'vue'
import { ChevronDown } from 'lucide-vue-next'
import { useElementSize, useLocalStorage } from '@vueuse/core'
import { useVirtualizer } from '@tanstack/vue-virtual'
import { useIntersectionObserver } from '@vueuse/core'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { useChatStore } from '@/store/chat-list'
import { useWorkdirsStore } from '@/store/workdirs'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { normalizedSessionMode, sortByRecency } from '@/store/chat-list.utils'
import { measureVirtualRow } from '@/utils/virtual-row-measure'
import type { SessionSummary } from '@/composables/api/useChat'
import {
  didLoadMoreMakeProgress,
  shouldPrefetchToFillViewport,
  shouldShowLoadMoreSentinel,
} from './recents-scroll'
import { TextButton, Spinner } from '@felinic/ui'
import SessionItem from './session-item.vue'
import SessionDialogs from './session-dialogs.vue'
// The narrow native scrollbar for sidebar scroll panes — shared with
// panel-schedule.vue, so it must not live in this file's scoped style.
import '@/styles/sidebar-scroll.css'

const { t } = useI18n()
const chatStore = useChatStore()
const workdirsStore = useWorkdirsStore()
const workspaceTabs = useWorkspaceTabsStore()
const {
  sessions,
  sessionId,
  currentBotId,
  loadingChats,
  hasMoreSessions,
  loadingMoreSessions,
  sessionsCursor,
} = storeToRefs(chatStore)

const sessionDialogs = ref<InstanceType<typeof SessionDialogs> | null>(null)

// One unified timeline: chats, discuss threads, ACP chats, and schedule runs
// all live here (each row carries its own type mark in session-item.vue).
// Subagent runs stay reachable through their parent session only.
const SIDEBAR_SESSION_MODES = new Set(['chat', 'discuss', 'schedule'])

// Same header type as the Folders section above so the two sibling section
// titles read identically; the 11px inset aligns the sidebar's 19px
// icon/label column (see panel-header.vue).
const sectionHeaderClass = 'text-xs font-[550] tracking-[-0.02em] pl-[11px] select-none' /* ui-allow-px: aligns the sidebar 19px label column (see panel-header.vue) */ /* ui-allow-style */

// Fold state per bot, persisted like the Folders section's.
const sectionCollapsedByBot = useLocalStorage<Record<string, boolean>>(
  'workspace-sidebar-recents-collapsed',
  {},
)
const sectionCollapsed = computed(() => sectionCollapsedByBot.value[currentBotId.value ?? ''] === true)
function toggleSectionCollapsed() {
  const botId = (currentBotId.value ?? '').trim()
  if (!botId) return
  sectionCollapsedByBot.value = {
    ...sectionCollapsedByBot.value,
    [botId]: !sectionCollapsed.value,
  }
}

// Sessions bound to a live workdir live in the Folders section — a SIBLING
// of this list (folders-section.vue) — so Recents excludes them here.
// Sessions of an archived or vanished workdir degrade back into this list.
watch(currentBotId, (botId) => {
  if (botId) void workdirsStore.ensureWorkdirs(botId)
}, { immediate: true })
const liveWorkdirIds = computed(() => new Set(
  workdirsStore.workdirsFor(currentBotId.value)
    .filter(workdir => !workdir.archived && !!workdir.id)
    .map(workdir => workdir.id ?? ''),
))

const visibleSessions = computed(() => {
  const inScope = sessions.value.filter(s => SIDEBAR_SESSION_MODES.has(normalizedSessionMode(s)))
  const unbound = inScope.filter(s => !liveWorkdirIds.value.has((s.workdir_id ?? '').trim()))
  return sortByRecency(unbound)
})

// ---- virtualized session list ----
// Rows are MEASURED, not pinned to a px stride: SessionItem sizes in rem (min-h-9)
// so its real height tracks the UI font setting / browser zoom. A fixed px estimate
// would desync the row offsets the moment the font scales, so we hand the virtualizer
// a rough estimate and let measureElement read each row's actual height.
const scrollEl = ref<HTMLElement | null>(null)
const virtualizer = useVirtualizer<HTMLElement, HTMLElement>(
  computed(() => ({
    count: visibleSessions.value.length,
    getScrollElement: () => scrollEl.value,
    estimateSize: () => 36,
    overscan: 10,
    getItemKey: (index: number) => visibleSessions.value[index]?.id ?? index,
    // Rows can be (re)measured while this pane is display:none (v-show'd
    // sidebar view) or mid-unmount, where they report height 0 —
    // measureVirtualRow keeps the last real size so those bogus zeros
    // can't desync the virtualizer's offset (the "dead gap above Recents,
    // newest sessions invisible" bug).
    measureElement: measureVirtualRow,
  })),
)
const totalSize = computed(() => virtualizer.value.getTotalSize())
const virtualRows = computed(() =>
  virtualizer.value.getVirtualItems().flatMap((vi) => {
    const session = visibleSessions.value[vi.index]
    return session ? [{ key: String(vi.key), index: vi.index, start: vi.start, session }] : []
  }),
)

// Read each rendered row's real (rem-based) height so offsets stay correct when the
// UI font scales — pairs with the estimate above.
function measureRow(el: unknown) {
  if (el instanceof HTMLElement) virtualizer.value.measureElement(el)
}

// Cursor-paginated load-more: a sentinel sits at the natural bottom of the
// scroll content and fires the next page 200px before the user reaches it.
// The store's loadMoreSessions guards re-entry (loadingMoreSessions / cursor
// exhaustion), so the observer can fire freely as the user scrolls.
const { height: viewportHeight } = useElementSize(scrollEl)
const showLoadMoreSentinel = computed(() => shouldShowLoadMoreSentinel({
  hasMore: hasMoreSessions.value,
  contentHeight: totalSize.value,
  viewportHeight: viewportHeight.value,
}))
const loadMoreSentinel = ref<HTMLElement | null>(null)
useIntersectionObserver(
  loadMoreSentinel,
  ([entry]) => {
    if (!entry?.isIntersecting) return
    void chatStore.loadMoreSessions()
  },
  {
    root: scrollEl,
    rootMargin: '0px 0px 200px 0px',
    threshold: 0,
  },
)

// Short first pages never reach the sentinel gate above — keep paging until
// the list overflows (or the cursor ends) so empty viewport space is filled
// without relying on a falsely-intersecting sentinel.
watch(
  [
    hasMoreSessions,
    loadingChats,
    loadingMoreSessions,
    totalSize,
    viewportHeight,
    sessionsCursor,
  ],
  (curr, prev) => {
    if (!shouldPrefetchToFillViewport({
      hasMore: hasMoreSessions.value,
      loading: loadingChats.value || loadingMoreSessions.value,
      contentHeight: totalSize.value,
      viewportHeight: viewportHeight.value,
    })) return
    // Failed / no-op loadMore: loading settles with the same cursor and height.
    // Do not tight-loop; a later viewport/list change can try again.
    if (
      prev
      && !didLoadMoreMakeProgress({
        wasLoadingMore: prev[2],
        isLoadingMore: curr[2],
        previousCursor: prev[5],
        currentCursor: curr[5],
        previousContentHeight: prev[3],
        currentContentHeight: curr[3],
      })
    ) return
    void chatStore.loadMoreSessions()
  },
)

// Switching bots swaps the whole list; start the new bot at the top instead of
// inheriting the previous bot's scroll offset (which could land past the end of
// a shorter list).
watch(currentBotId, () => {
  nextTick(() => {
    scrollEl.value?.scrollTo({ top: 0 })
    virtualizer.value.scrollToOffset(0)
  })
})

function handleSelect(session: SessionSummary) {
  // Opening (or focusing) the tab activates it, which selects the session.
  workspaceTabs.openSessionChat({
    sessionId: session.id,
    title: (session.title ?? '').trim() || t('chat.untitledSession'),
  })
  // Select-to-dismiss for the mobile nav sheet: picking the ALREADY active
  // session changes nothing (same sessionId, same active panel, same route),
  // so the sheet's state-change watchers never fire — close it explicitly.
  // No-op on desktop, where the sheet is never open.
  workspaceTabs.closeMobileNav()
}

// Right-click "Open": a separate pinned tab instead of reusing the preview slot.
function handleOpenNewTab(session: SessionSummary) {
  workspaceTabs.openSessionChatPinned({
    sessionId: session.id,
    title: (session.title ?? '').trim() || t('chat.untitledSession'),
  })
  // Same select-to-dismiss reason as handleSelect above.
  workspaceTabs.closeMobileNav()
}
</script>
