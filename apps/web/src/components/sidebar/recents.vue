<template>
  <div class="flex min-w-0 flex-col">
    <!-- Unified Recents: one timeline for chats, ACP chats, and schedule
         runs (each row carries its own type mark — see session-item.vue).
         The header is the same Arkloop group header as every folder above
         (group-header.vue): click to fold, hover reveals the chevron and
         the trailing plus. The plus starts a session bound to NO folder
         (clears the bot's working-workdir binding), mirroring the folders'
         per-folder plus. Recents does not own a scrollport: it is the tail
         of the sessions panel's single scroll container (passed in as
         `scrollEl`), so the folders above it and this timeline scroll as
         one list. -->
    <div class="shrink-0 px-2 pb-0.5 pt-1">
      <GroupHeader
        :label="t('chat.recents')"
        :expanded="!sectionCollapsed"
        :label-class="sectionLabelClass"
        compact
        @toggle="toggleSectionCollapsed"
      >
        <TextButton
          :class="rowActionRevealClass"
          :aria-label="t('chat.newSession')"
          @click.stop="startUnfiledSession"
        >
          <Plus />
        </TextButton>
      </GroupHeader>
    </div>

    <!-- Cursor-paginated rows render as a plain list; load-more still uses a
         bottom sentinel, now observed against the shared scrollport. -->
    <div
      v-show="!sectionCollapsed"
      class="px-2"
    >
      <div
        v-for="session in visibleSessions"
        :key="session.id"
        class="pb-[2px]"
      >
        <SessionItem
          :session="session"
          :is-active="sessionId === session.id"
          :streaming="chatStore.isSessionStreaming(currentBotId, session.id)"
          @select="handleSelect"
          @open-new-tab="handleOpenNewTab"
          @rename="sessionDialogs?.openRename($event)"
          @delete="sessionDialogs?.openDelete($event, { fallbackMode: 'recent' })"
        />
      </div>

      <div
        v-if="showSentinel"
        ref="loadMoreSentinel"
        data-testid="load-more-sentinel"
        class="h-px w-full"
      />

      <template v-if="loadingMoreSessions">
        <div
          v-for="(widthClass, index) in loadMoreSkeletonRows"
          :key="`load-more-${index}`"
          class="pb-[2px]"
        >
          <div class="flex min-h-[2.125rem] items-center rounded-[9px] px-3">
            <Skeleton
              :class="[sessionSkeletonBarClass, widthClass]"
            />
          </div>
        </div>
      </template>

      <div
        v-if="currentBotId && !loadingChats && visibleSessions.length === 0"
        class="px-3 py-6 text-center text-xs text-muted-foreground"
      >
        {{ t('chat.noSessions') }}
      </div>

      <template v-if="loadingChats && visibleSessions.length === 0">
        <div
          v-for="(widthClass, index) in initialSkeletonRows"
          :key="`initial-${index}`"
          class="pb-[2px]"
        >
          <div class="flex min-h-[2.125rem] items-center rounded-[9px] px-3">
            <Skeleton
              :class="[sessionSkeletonBarClass, widthClass]"
            />
          </div>
        </div>
      </template>
    </div>

    <SessionDialogs ref="sessionDialogs" />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, nextTick, toRef, watch } from 'vue'
import { Plus } from 'lucide-vue-next'
import { useLocalStorage } from '@vueuse/core'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { useChatStore } from '@/store/chat-list'
import { useWorkdirsStore } from '@/store/workdirs'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { normalizedSessionMode, sortByRecency } from '@/store/chat-list.utils'
import type { SessionSummary } from '@/composables/api/useChat'
import { useSidebarInfiniteScroll } from './use-sidebar-infinite-scroll'
import { TextButton, Skeleton } from '@felinic/ui'
import SessionItem from './session-item.vue'
import SessionDialogs from './session-dialogs.vue'
import GroupHeader from './group-header.vue'

// The sessions panel owns the scrollport shared with Folders; Recents pages
// against it (sentinel root + reset-to-top) instead of nesting its own.
const props = defineProps<{
  scrollEl: HTMLElement | null
}>()

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

const SIDEBAR_SESSION_MODES = new Set(['chat', 'discuss', 'schedule'])

const sessionSkeletonBarClass = 'h-4 max-w-full rounded-xs!'

/** Slightly above text-control cap height (14px → 16px bar). */
const SESSION_SKELETON_WIDTHS = [
  'w-[58%]',
  'w-[42%]',
  'w-[71%]',
  'w-[36%]',
  'w-[65%]',
  'w-[48%]',
  'w-[54%]',
  'w-[39%]',
] as const

const initialSkeletonRows = SESSION_SKELETON_WIDTHS
const loadMoreSkeletonRows = SESSION_SKELETON_WIDTHS.slice(0, 3)

// 区头 label 沿用改造前 TextButton 的渲染:静态 muted、hover 行变 foreground
// (旧区头是 ghost TextButton,text-muted-foreground hover:text-foreground)
const sectionLabelClass = 'text-xs font-[550] tracking-[-0.02em] text-muted-foreground transition-colors group-hover/group-header:text-foreground' /* ui-allow-style */

// 组头 trailing 动作的显现类,与 folders-section 的组头按钮同一套
// (见 group-header.vue 头注释)。
const rowActionRevealClass = 'opacity-0 pointer-events-none group-hover/group-header:pointer-events-auto group-hover/group-header:opacity-100 focus-visible:pointer-events-auto focus-visible:opacity-100'

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

const scrollEl = toRef(props, 'scrollEl')
const {
  loadMoreSentinel,
  showSentinel,
  resetScrollTop,
} = useSidebarInfiniteScroll({
  scrollEl,
  hasMore: hasMoreSessions,
  loading: computed(() => loadingChats.value || loadingMoreSessions.value),
  loadMore: () => chatStore.loadMoreSessions(),
  progressCursor: sessionsCursor,
  itemCount: computed(() => visibleSessions.value.length),
})

watch(currentBotId, () => {
  nextTick(() => {
    resetScrollTop()
  })
})

// The Recents group-header plus: start a session bound to no folder, so it
// lands in this unfiled timeline (mirrors folders-section.startFolderSession
// with the working-workdir binding cleared).
function startUnfiledSession() {
  const botId = (currentBotId.value ?? '').trim()
  if (!botId) return
  workdirsStore.setWorkingWorkdir(botId, null)
  workspaceTabs.openDraftChat({ title: t('chat.newSession'), explicitSelection: false })
  workspaceTabs.closeMobileNav()
}

function handleSelect(session: SessionSummary) {
  // Pass the raw title (possibly empty): openSessionChat's fallback derives
  // the tab title, including the channel conversation name for untitled
  // channel sessions — never bake the literal "Untitled" text in here.
  workspaceTabs.openSessionChat({
    sessionId: session.id,
    title: (session.title ?? '').trim(),
  })
  workspaceTabs.closeMobileNav()
}

function handleOpenNewTab(session: SessionSummary) {
  workspaceTabs.openSessionChatPinned({
    sessionId: session.id,
    title: (session.title ?? '').trim(),
  })
  workspaceTabs.closeMobileNav()
}
</script>
