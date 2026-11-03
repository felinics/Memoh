<template>
  <!-- Mobile home for the two-page navigation: the current bot's sessions
       first. Mobile is not the desktop rail at phone width, so this page keeps
       only what a phone needs:
         - a top row of bot pills, with the way back to the chat at its right;
         - the current bot's own pages (files, schedules, bot settings) as one
           group of large buttons, then Recents;
         - search, general settings and "new session" in a bottom dock, where
           the thumb already is when the app opens to type. This page is
           reached from the chat, which already opens on an empty composer, so
           "new session" only needs to be a round button here.
       The top row and the button group are deliberately tall: together they
       bring the session list down to roughly a third of the screen, so the
       eye does not start at the status bar.

       One geometry runs through the page: a 16px gutter on both sides, and
       one 48px round control size. The selected pill, the back-to-chat button
       and the dock's buttons all share it, so the top-right and bottom-right
       circles stand on the same right edge.

       Sizes are an Android-calibrated mobile scale, measured on a 360px phone
       against a native reference app: 56px group rows (as the mobile settings
       list), 16px regular list titles over 14px meta text, 16px medium button
       labels, 14px medium section labels, 20px icons with a 2px stroke. The
       desktop body size (14px, weight 430) reads small and thin there.

       Left out on purpose: the desktop quick actions (new session moved to
       the dock, bot settings into the group), the app store (it would make
       the group longer for something rarely opened from a phone), and
       folders (how they work on a phone is not decided yet; their sessions
       appear in Recents for now). -->
  <div class="relative flex h-full min-h-0 flex-col bg-background text-foreground">
    <template v-if="subview === 'home'">
      <header class="flex shrink-0 items-center gap-2 py-2 pr-4">
        <MobileBotPills
          :bots="pillBots"
          :current-id="currentBotId"
          :select="switchBot"
        />
        <button
          type="button"
          class="flex size-12 shrink-0 items-center justify-center rounded-full bg-muted"
          :aria-label="t('settings.backToChat')"
          @click="workspaceTabs.closeMobileNav()"
        >
          <ChevronRight
            :stroke-width="2"
            class="size-5"
          />
        </button>
      </header>

      <div
        ref="scrollEl"
        class="min-h-0 flex-1 overflow-y-auto overscroll-contain pb-28"
      >
        <!-- One group of large buttons, separated by a hairline gap of the
             page color, the same grouped shape as the Mono settings rows. -->
        <div class="mx-4 mt-3 flex flex-col gap-0.5 overflow-hidden rounded-3xl">
          <button
            v-for="tool in tools"
            :key="tool.view"
            type="button"
            class="flex h-14 items-center gap-3.5 bg-muted px-4 text-left text-base font-medium"
            @click="tool.view === 'bot-settings' ? openBotSettings() : openSubview(tool.view)"
          >
            <component
              :is="tool.icon"
              :stroke-width="2"
              class="size-5"
            />
            {{ tool.label }}
          </button>
        </div>

        <h2 class="px-4 pt-5 pb-1 text-sm font-medium text-muted-foreground">
          {{ t('chat.recents') }}
        </h2>

        <div
          v-for="session in visibleSessions"
          :key="session.id"
          role="button"
          tabindex="0"
          class="flex items-center py-2.5 pr-4 pl-4"
          @click="openSession(session)"
          @keydown.enter.prevent="openSession(session)"
        >
          <div class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="truncate text-base font-normal">{{ sessionTitle(session) }}</span>
            <span class="text-sm font-normal text-muted-foreground">{{ sessionTime(session) }}</span>
          </div>
          <DropdownMenu>
            <DropdownMenuTrigger as-child>
              <button
                type="button"
                class="flex size-12 shrink-0 items-center justify-center rounded-full text-muted-foreground"
                :aria-label="t('chat.sessionActions')"
                @click.stop
              >
                <EllipsisVertical class="size-5" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem @select="sessionDialogs?.openRename(session)">
                <Pencil />
                {{ t('common.rename') }}
              </DropdownMenuItem>
              <DropdownMenuItem
                variant="destructive"
                @select="sessionDialogs?.openDelete(session)"
              >
                <Trash2 />
                {{ t('common.delete') }}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>

        <div
          v-if="showSentinel"
          :ref="el => (loadMoreSentinel = el as HTMLElement | null)"
          class="h-px w-full"
        />
        <div
          v-if="currentBotId && !loadingChats && visibleSessions.length === 0"
          class="px-4 py-6 text-base text-muted-foreground"
        >
          {{ t('chat.noSessions') }}
        </div>
      </div>

      <!-- Dock. The fade lets the list scroll under it without a hard edge. -->
      <div class="pointer-events-none absolute inset-x-0 bottom-0 flex gap-2 bg-gradient-to-b from-transparent to-background to-40% px-4 pt-6 pb-3">
        <button
          type="button"
          class="pointer-events-auto flex h-12 min-w-0 flex-1 items-center gap-2.5 rounded-full bg-muted px-4 text-base font-normal text-muted-foreground"
          @click="searchOpen = true"
        >
          <Search
            :stroke-width="2"
            class="size-5 shrink-0"
          />
          <span class="truncate">{{ t('chat.searchSessions') }}</span>
        </button>
        <button
          type="button"
          class="pointer-events-auto flex size-12 shrink-0 items-center justify-center rounded-full bg-muted"
          :aria-label="t('sidebar.settings')"
          @click="goSettings"
        >
          <Settings
            :stroke-width="2"
            class="size-5"
          />
        </button>
        <button
          type="button"
          class="pointer-events-auto flex size-12 shrink-0 items-center justify-center rounded-full bg-foreground text-background"
          :aria-label="t('chat.newSession')"
          :disabled="!currentBotId"
          @click="newSession"
        >
          <SquarePen
            :stroke-width="2"
            class="size-5"
          />
        </button>
      </div>
    </template>

    <!-- Files / schedules: the same panels the desktop rail hosts, as a page
         of their own with a way back to the list. -->
    <template v-else>
      <MobileBar>
        <template #left>
          <MobileBarIconButton
            :icon="ChevronLeft"
            :label="t('common.back')"
            @click="closeSubview"
          />
        </template>
        <div class="min-w-0 flex-1 truncate text-center text-control font-medium">
          {{ subviewLabel }}
        </div>
        <template #right>
          <div class="size-9 shrink-0" />
        </template>
      </MobileBar>
      <div class="min-h-0 flex-1 overflow-hidden">
        <PanelFiles
          v-if="subview === 'files'"
          class="h-full"
        />
        <PanelSchedule
          v-else
          class="h-full"
        />
      </div>
    </template>

    <SessionSearchDialog v-model:open="searchOpen" />
    <SessionDialogs ref="sessionDialogs" />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch, type Component } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useLocalStorage } from '@vueuse/core'
import { useQuery } from '@pinia/colada'
import { getBotsQuery } from '@memohai/sdk/colada'
import type { BotsBot } from '@memohai/sdk'
import { Calendar, ChevronLeft, ChevronRight, EllipsisVertical, Files, Pencil, Search, Settings2, SquarePen, Trash2 } from 'lucide-vue-next'
import { SettingsIcon as Settings } from '@memohai/icon/ui'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import { useChatSelectionStore } from '@/store/chat-selection'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { normalizedSessionMode, routeConversationLabel, sortByRecency } from '@/store/chat-list.utils'
import type { SessionSummary } from '@/composables/api/useChat'
import { usePinnedBots } from '@/composables/usePinnedBots'
import { hasBotPermission } from '@/utils/bot-permissions'
import { formatCalendarTime } from '@/utils/date-time'
import { switcherBots } from '@/components/sidebar/bot-switcher-list'
import { useSidebarInfiniteScroll } from '@/components/sidebar/use-sidebar-infinite-scroll'
import PanelFiles from '@/components/sidebar/panel-files.vue'
import PanelSchedule from '@/components/sidebar/panel-schedule.vue'
import SessionDialogs from '@/components/sidebar/session-dialogs.vue'
import SessionSearchDialog from '@/components/sidebar/session-search-dialog.vue'
import MobileBotPills from './bot-pills.vue'
import { useNativeBack } from './native-bridge'
import MobileBar from '@/components/mobile-bar/index.vue'
import MobileBarIconButton from '@/components/mobile-bar/icon-button.vue'

type Subview = 'home' | 'files' | 'schedule'

const { t, locale } = useI18n()
const router = useRouter()
const chatStore = useChatStore()
const selectionStore = useChatSelectionStore()
const workspaceTabs = useWorkspaceTabsStore()
const { activeId, mobileNavOpen } = storeToRefs(workspaceTabs)
const { currentBotId, sessions, loadingChats, hasMoreSessions, loadingMoreSessions, sessionsCursor } = storeToRefs(chatStore)

// ── Bots ────────────────────────────────────────────────────────────────────
// Same list and order as the desktop bot switcher (pinned first, then the
// manual drag order it persists).
const { data: botData } = useQuery(getBotsQuery())
const { sortBots } = usePinnedBots()
const botOrder = useLocalStorage<string[]>('bot-order', [])
const pillBots = computed<BotsBot[]>(() =>
  switcherBots(botData.value?.items ?? [], sortBots, botOrder.value),
)
const currentBot = computed(() => pillBots.value.find(bot => bot.id === currentBotId.value) ?? null)

// ── Dismissal ───────────────────────────────────────────────────────────────
// Picking something for the chat page to show returns to it: a session (from
// the list or from search), a new session, or a file opened from the files
// page. Leaving for settings or bot settings does NOT close this page, so the
// way back from there lands here again, where it was left.
// Switching bots from the pills clears and then restores the selected session
// while the new bot loads; the user stays on this page to pick one, so those
// changes are let through.
let switchingBot = false
function dismiss() {
  if (!switchingBot) workspaceTabs.closeMobileNav()
}
watch(() => selectionStore.sessionId, dismiss)
watch(activeId, dismiss)

async function switchBot(bot: BotsBot) {
  const id = bot.id ?? ''
  if (!id || id === currentBotId.value) return
  switchingBot = true
  try {
    await Promise.all([
      chatStore.selectBot(id),
      router.push({ name: 'bot', params: { botName: bot.name ?? id } }),
    ])
    await nextTick()
  }
  finally {
    switchingBot = false
  }
  scrollEl.value?.scrollTo({ top: 0 })
}

// ── Sessions ────────────────────────────────────────────────────────────────
const SESSION_MODES = new Set(['chat', 'discuss', 'schedule'])
const visibleSessions = computed(() => sortByRecency(
  sessions.value.filter(session => SESSION_MODES.has(normalizedSessionMode(session))
    && !chatStore.isSessionTentative(session.id)),
))

function sessionTitle(session: SessionSummary) {
  return (session.title ?? '').trim() || routeConversationLabel(session) || t('chat.untitledSession')
}

function sessionTime(session: SessionSummary) {
  return formatCalendarTime(session.updated_at ?? session.created_at, { locale: locale.value, hour12: false })
}

const scrollEl = ref<HTMLElement | null>(null)
const { loadMoreSentinel, showSentinel } = useSidebarInfiniteScroll({
  scrollEl,
  hasMore: hasMoreSessions,
  loading: computed(() => loadingChats.value || loadingMoreSessions.value),
  loadMore: () => chatStore.loadMoreSessions(),
  progressCursor: sessionsCursor,
  itemCount: computed(() => visibleSessions.value.length),
})

const sessionDialogs = ref<InstanceType<typeof SessionDialogs> | null>(null)
const searchOpen = ref(false)

function openSession(session: SessionSummary) {
  workspaceTabs.openSessionChat({ sessionId: session.id, title: (session.title ?? '').trim() })
  workspaceTabs.closeMobileNav()
}

function newSession() {
  if (!currentBotId.value) return
  workspaceTabs.openDraftChat({ title: t('chat.newSession'), explicitSelection: false })
  // Already on the draft: nothing above changes, so close explicitly.
  workspaceTabs.closeMobileNav()
}

function goSettings() {
  void router.push('/settings')
}

function openBotSettings() {
  const botId = currentBotId.value
  if (!botId) return
  void router.push({ name: 'bot-detail', params: { botName: currentBot.value?.name ?? botId } })
}

// ── Files / schedules ───────────────────────────────────────────────────────
const canWorkspaceRead = computed(() =>
  hasBotPermission(currentBot.value?.current_user_permissions, 'workspace_read'),
)
const tools = computed<{ view: Exclude<Subview, 'home'> | 'bot-settings', label: string, icon: Component }[]>(() => [
  ...(canWorkspaceRead.value ? [{ view: 'files' as const, label: t('chat.activityBar.files'), icon: Files }] : []),
  { view: 'schedule', label: t('chat.activityBar.schedule'), icon: Calendar },
  { view: 'bot-settings', label: t('chat.botSettings'), icon: Settings2 },
])

const subview = ref<Subview>('home')
const subviewLabel = computed(() => tools.value.find(tool => tool.view === subview.value)?.label ?? '')

// The hosted panels read the shared sidebar view to know they are visible.
function openSubview(view: Exclude<Subview, 'home'>) {
  workspaceTabs.selectSidebarView(view)
  subview.value = view
}

function closeSubview() {
  workspaceTabs.selectSidebarView('sessions')
  subview.value = 'home'
}

watch(currentBotId, () => {
  if (subview.value !== 'home') closeSubview()
})

// ── System back ─────────────────────────────────────────────────────────────
// Neither the files/schedules page nor this page itself is a route, so back
// is answered here (see native-bridge.ts): it first leaves a files/schedules
// page, then returns from this page to the chat.
useNativeBack(() => {
  if (!mobileNavOpen.value) return false
  if (subview.value !== 'home') closeSubview()
  else workspaceTabs.closeMobileNav()
  return true
})
</script>
