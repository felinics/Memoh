<template>
  <!-- Reuse the settings view's open/close motion: slide-in from the right on
       enter, faster slide-out on leave, holding navigation until leave plays.
       Same curve/timing as settings-section/index.vue. -->
  <Transition
    appear
    enter-active-class="transition-all duration-[90ms] ease-out"
    enter-from-class="opacity-0 translate-x-2.5"
    leave-active-class="transition-all duration-[40ms] ease-in"
    leave-to-class="opacity-0 translate-x-2.5"
    @after-leave="onAfterLeave"
  >
    <section
      v-if="show"
      class="absolute inset-0 flex flex-col bg-background"
      data-desktop-window-layer
    >
      <div class="flex-1 relative">
        <MasterDetailSidebarLayout
          flush
          class="[&_td:last-child]:w-45"
          :detail-open="mobileDetailOpen"
          @close-detail="closeMobileDetail"
        >
          <template #sidebar-header>
            <!-- Back: a full-width row at the very top — same position, size and
                 style as the settings sidebar's < Settings — so returning lands
                 the affordance in the exact same spot. Identity sits just below as
                 a floating card, which anchors the header and keeps back from
                 reading as a stray nav row — no hairline needed. -->
            <div
              v-if="macTrafficReserve"
              class="h-12 shrink-0 [-webkit-app-region:drag]"
              aria-hidden="true"
            />
            <div
              class="px-4 pb-3 flex flex-col"
              :class="macTrafficReserve ? undefined : 'pt-[18px]'"
            >
              <NavItem
                class="[-webkit-app-region:no-drag]"
                @click="goBack()"
              >
                <ChevronLeft class="size-3.5 shrink-0" />
                <span class="min-w-0 truncate">{{ backLabel }}</span>
              </NavItem>

              <!-- Identity floats as a card — same recipe as the bots-list persona
                   cards (bg-card + border + menu-shell radius), just tighter padding.
                   Wrapping it gives the header a real visual anchor: the round avatar
                   no longer sits bare against the nav-hover edge, so back + name read
                   as one settled block instead of two misaligned centers. The card
                   border replaces the old hairline, so no divider above or below. -->
              <div class="mt-3 flex items-center gap-3 rounded-[var(--radius-menu-shell)] border border-border bg-card p-3">
                <!-- Avatar -->
                <div class="group/avatar relative size-12 shrink-0 rounded-full overflow-hidden bg-muted">
                  <Avatar class="size-12 rounded-full">
                    <AvatarImage
                      v-if="bot?.avatar_url"
                      :src="bot.avatar_url"
                      :alt="bot.display_name"
                    />
                    <AvatarFallback class="text-lg">
                      {{ avatarFallback }}
                    </AvatarFallback>
                  </Avatar>
                  <!-- Edit Overlay -->
                  <button
                    type="button"
                    class="absolute inset-0 flex items-center justify-center rounded-full bg-black/40 opacity-0 transition-opacity group-hover/avatar:opacity-100"
                    :title="$t('common.edit')"
                    :aria-label="$t('common.edit')"
                    :disabled="!bot || botLifecyclePending"
                    @click="handleEditAvatar"
                  >
                    <SquarePen class="size-4 text-white" />
                  </button>
                </div>
              
                <!-- Info Block -->
                <div class="min-w-0 flex-1 flex flex-col justify-center">
                  <div class="group/name flex items-center gap-1 relative min-w-0">
                    <template v-if="isEditingBotName && bot">
                      <Input
                        ref="editNameInputRef"
                        v-model="botNameDraft"
                        class="h-7 w-full text-xs px-2 pr-6 shadow-none"
                        :placeholder="$t('bots.displayNamePlaceholder')"
                        :disabled="isSavingBotName"
                        @keydown.enter.prevent="handleConfirmBotName"
                        @keydown.esc.prevent="handleCancelBotName"
                        @blur="handleConfirmBotName"
                      />
                      <div class="absolute right-1.5 top-1/2 -translate-y-1/2 opacity-50 pointer-events-none">
                        <Check class="size-3" />
                      </div>
                    </template>
                    <template v-else>
                      <h2 class="truncate text-sm font-semibold text-foreground">
                        {{ botNameDraft.trim() || bot?.display_name || botId }}
                      </h2>
                      <button
                        v-if="bot"
                        type="button"
                        class="opacity-0 group-hover/name:opacity-100 p-1 shrink-0"
                        :disabled="botLifecyclePending"
                        @click="handleStartEditBotName"
                      >
                        <SquarePen class="size-3 text-muted-foreground" />
                      </button>
                    </template>
                  </div>
                
                  <!-- Status: an inline dot + label living inside the white identity
                       card — no filled pill, so it never reads as a black blob on
                       white. A success dot for a healthy/active bot echoes the right
                       pane's green "Healthy"; an issue turns dot + label destructive;
                       a healthy-but-inactive bot dims to a muted dot; lifecycle shows
                       a spinner. Bot type trails as a muted footnote. All semantic
                       tokens, so light and dark stay in sync. -->
                  <div class="mt-1 flex items-center gap-1.5 text-[11px]">
                    <template v-if="bot">
                      <LoaderCircle
                        v-if="bot.status === 'creating' || bot.status === 'deleting'"
                        class="size-2.5 shrink-0 animate-spin text-muted-foreground"
                      />
                      <span
                        v-else
                        class="size-1.5 shrink-0 rounded-full"
                        :class="statusVariant === 'destructive'
                          ? 'bg-destructive'
                          : statusVariant === 'secondary'
                            ? 'bg-muted-foreground/40'
                            : 'bg-success'"
                      />
                      <span
                        class="font-medium"
                        :class="statusVariant === 'destructive' ? 'text-destructive' : 'text-muted-foreground'"
                        :title="hasIssue ? issueTitle : undefined"
                      >{{ statusLabel }}</span>
                      <span
                        v-if="bot.type"
                        class="text-muted-foreground/60"
                      >· {{ botTypeLabel }}</span>
                    </template>
                  </div>
                </div>
              </div>
            
              <!-- Search Input -->
              <div class="mt-3 relative">
                <Search class="absolute left-2.5 top-1/2 -translate-y-1/2 size-3 text-muted-foreground" />
                <Input
                  v-model="searchQuery"
                  type="text"
                  name="bot-settings-search"
                  autocomplete="off"
                  autocapitalize="off"
                  autocorrect="off"
                  spellcheck="false"
                  class="pl-8 pr-8 h-8 text-xs"
                  :placeholder="$t('common.search')"
                />
                <button
                  v-if="searchQuery"
                  type="button"
                  class="absolute right-2 top-1/2 -translate-y-1/2 size-4 flex items-center justify-center rounded-full hover:bg-muted text-muted-foreground shrink-0"
                  @click="searchQuery = ''"
                >
                  <X class="size-2.5" />
                </button>
              </div>
            </div>
          </template>

          <template #sidebar-content>
            <!-- Same NavItem rows as the settings sidebar; search narrows the
               groups in place instead of swapping to a separate result list. -->
            <div class="px-2 pb-2">
              <template v-if="displayGroups.length">
                <div
                  v-for="(group, idx) in displayGroups"
                  :key="group.key"
                  :class="idx > 0 ? 'mt-4' : ''"
                >
                  <SidebarMenu class="m-0 gap-1 p-0">
                    <SidebarMenuItem
                      v-for="tab in group.items"
                      :key="tab.value"
                    >
                      <NavItem
                        :active="activeTab === tab.value"
                        :aria-current="activeTab === tab.value ? 'page' : undefined"
                        @click="selectTab(tab.value)"
                      >
                        <component
                          :is="tab.icon"
                          v-if="tab.icon"
                          :stroke-width="1.75"
                          class="size-4 shrink-0"
                        />
                        <span class="whitespace-nowrap">{{ $t(tab.label) }}</span>
                      </NavItem>
                    </SidebarMenuItem>
                  </SidebarMenu>
                </div>
              </template>
              <div
                v-else
                class="px-3 py-6 text-center text-xs text-muted-foreground"
              >
                {{ $t('common.noData') }}
              </div>
            </div>
          </template>

          <template #sidebar-footer />

          <template #detail>
            <!-- scrollbar-gutter: stable reserves the scrollbar track on every tab,
                 scrolling or not. Without it a long tab (e.g. General) shows a
                 scrollbar that narrows the pane, so PageShell's mx-auto column
                 re-centers and the title + card edges shift vs a short tab (e.g.
                 Platforms). Reserving the gutter keeps the content width — and thus
                 every tab's alignment — identical. -->
            <div class="absolute inset-0 overflow-y-auto bg-background [scrollbar-gutter:stable]">
              <!-- Top drag strip over the detail pane only (mac desktop), so the
                   window stays draggable beside the sidebar. No fill/border — it
                   shares --background with the content, so the sidebar's vertical
                   edge stays the only divider, unbroken to the top. -->
              <div
                v-if="macTrafficReserve"
                class="h-8 shrink-0 [-webkit-app-region:drag]"
              />
              <!-- Ensure consistent padding matching Box-in-Box bento architecture.
                   The <md step mirrors PageShell's page gutter (px-4 md:px-6) so a
                   phone keeps a 16px margin; the 'tab' PageShell variant inside
                   adds no horizontal padding of its own. -->
              <div class="px-4 md:px-6 pt-4 pb-4">
                <KeepAlive>
                  <component
                    :is="activeComponent?.component"
                    v-bind="activeComponent?.params"
                  />
                </KeepAlive>
              </div>
            </div>
          </template>
        </MasterDetailSidebarLayout>
      </div>

      <AvatarEditDialog
        v-model:open="avatarDialogOpen"
        v-model:avatar-url="avatarUrlModel"
        :fallback-text="avatarFallback"
      />
    </section>
  </Transition>
</template>

<script setup lang="ts">
import {
  Avatar, AvatarImage, AvatarFallback, Input,
  SidebarMenu, SidebarMenuItem,
} from '@felinic/ui'
import {
  SquarePen, LoaderCircle, Check, Search, X, LayoutDashboard, Settings, MessageSquare,
  BrainCircuit, ShieldAlert, Database, Mail, Link, Clock, Server, FileBox, Zap,
  Monitor, Globe, Bot as BotIcon, ChevronLeft, Workflow, Laptop, Plug
} from 'lucide-vue-next'
import { computed, ref, watch, onMounted, toValue, nextTick, inject } from 'vue'
import { useRoute, useRouter, onBeforeRouteLeave } from 'vue-router'
import { NavItem, toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { useQuery, useMutation, useQueryCache } from '@pinia/colada'
import {
  getBotsById, putBotsById,
  getBotsByIdChecks,
  getBotsByBotIdContainer,
  getBotsByBotIdContainerSnapshots,
} from '@memohai/sdk'
import { getBotsQueryKey } from '@memohai/sdk/colada'
import type {
  BotsBotCheck, HandlersGetContainerResponse,
  HandlersListSnapshotsResponse,
} from '@memohai/sdk'
import { useCapabilitiesStore } from '@/store/capabilities'

import BotSettings from './components/bot-settings.vue'
import BotToolApproval from './components/bot-tool-approval.vue'
import BotHooks from './components/bot-hooks.vue'
import BotDesktop from './components/bot-desktop.vue'
import BotNetwork from './components/bot-network.vue'
import BotChannels from './components/bot-channels.vue'
import BotMcp from './components/bot-mcp.vue'
import BotConnectors from './components/bot-connectors.vue'
import BotMemory from './components/bot-memory.vue'
import BotSkills from './components/bot-skills.vue'
import BotCompaction from './components/bot-compaction.vue'
import BotEmail from './components/bot-email.vue'
import BotOverview from './components/bot-overview.vue'
import BotSchedule from './components/bot-schedule.vue'
import BotContainer from './components/bot-container.vue'
import BotRemoteRuntime from './components/bot-remote-runtime.vue'
import BotAccess from './components/bot-access.vue'
import BotAgents from './components/bot-agents.vue'
import AvatarEditDialog from './components/avatar-edit-dialog.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useAvatarInitials } from '@/composables/useAvatarInitials'
import { useSyncedQueryParam } from '@/composables/useSyncedQueryParam'
import { useBackAffordance } from '@/composables/useBackOr'
import { useIsMobile } from '@/composables/useIsMobile'
import { registerBotBreadcrumbName } from '@/lib/bot-breadcrumb'
import { useBotStatusMeta } from '@/composables/useBotStatusMeta'
import MasterDetailSidebarLayout from '@/components/master-detail-sidebar-layout/index.vue'
import { DesktopShellKey } from '@/lib/desktop-shell'
import { resolveBotWorkspaceBackend } from '@/utils/bot-workspace'
import { filterBotDetailsTabs, type BotDetailsTabRule } from '@/utils/bot-detail-tabs'
type BotCheck = BotsBotCheck
type BotContainerInfo = HandlersGetContainerResponse
type BotContainerSnapshot = HandlersListSnapshotsResponse extends { snapshots?: (infer T)[] } ? T : never

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const isMobile = useIsMobile()

// macOS desktop: this page renders its own full-height sidebar (no full-width
// topbar above it), so the sidebar header clears the traffic lights and the
// detail pane gets its own top drag strip. Same computation as settings-section.
const desktopShell = inject(DesktopShellKey, false)
const macTrafficReserve = computed(() =>
  desktopShell
  && typeof navigator !== 'undefined'
  && navigator.platform.toLowerCase().includes('mac'),
)

// Back follows real navigation history (router.back) and only falls back to the
// bots list on a cold load — so entering a bot from Scheduled Jobs, a chat, or
// any future surface returns where the user came from rather than a fixed page.
// The label mirrors that destination instead of always reading "Bots".
const { onBack: goBack, label: backLabel } = useBackAffordance({ name: 'bots' })

// Reuse the settings view's open/close motion (see settings-section): this bot
// view slides in on enter and slides out on leave, holding navigation until the
// leave animation finishes. Tab switches change only the query, not the route
// record, so they don't trigger this — only entering/leaving the bot does.
const show = ref(true)
let pendingRouteLeave: (() => void) | null = null
onBeforeRouteLeave((_to, _from, next) => {
  show.value = false
  pendingRouteLeave = next
})
function onAfterLeave(): void {
  pendingRouteLeave?.()
  pendingRouteLeave = null
}

// The route param may be a name slug or a UUID; resolve it to the canonical
// bot UUID so child tabs keep operating on UUIDs.
const routeIdentifier = computed(() => {
  const id = route.params.botName
  return typeof id === 'string' ? id : ''
})

const { data: bot } = useQuery({
  key: () => ['bot', routeIdentifier.value],
  query: async () => {
    const { data } = await getBotsById({ path: { id: routeIdentifier.value }, throwOnError: true })
    return data
  },
  enabled: () => !!routeIdentifier.value,
})

const botId = computed(() => bot.value?.id ?? '')

const containerInfo = ref<BotContainerInfo | null>(null)

const botWorkspaceBackend = computed(() =>
  resolveBotWorkspaceBackend(containerInfo.value?.workspace_backend),
)

const canManageBot = computed(() => {
  const perms = bot.value?.current_user_permissions
  // Only restrict management when the backend explicitly reports a permission
  // set without "manage". When the field is absent (older backend, cache, etc.)
  // default to allowing management so owners are never locked out of their own
  // bot; the backend still enforces access on every management endpoint.
  if (!perms || perms.length === 0) return true
  return perms.includes('manage')
})

const capabilitiesStore = useCapabilitiesStore()

const tabList = computed(() => {
  const bot_id = toValue(botId)
  const tabs = [
    { value: 'overview', label: 'bots.tabs.overview', icon: LayoutDashboard, component: BotOverview, params: {} },
    { value: 'general', label: 'bots.tabs.general', icon: Settings, component: BotSettings, params: { 'bot-id': bot_id, 'bot-type': bot.value?.type } },
    { value: 'desktop', label: 'bots.tabs.desktop', icon: Monitor, component: BotDesktop, params: { 'bot-id': bot_id }, containerWorkspaceOnly: true },
    { value: 'remote-runtime', label: 'bots.tabs.remoteRuntime', icon: Laptop, component: BotRemoteRuntime, params: { 'bot-id': bot_id } },
    { value: 'container', label: 'bots.tabs.container', icon: Server, component: BotContainer, params: {}, containerWorkspaceOnly: true },
    { value: 'network', label: 'bots.tabs.network', icon: Globe, component: BotNetwork, params: { 'bot-id': bot_id }, containerWorkspaceOnly: true },
    { value: 'memory', label: 'bots.tabs.memory', icon: Database, component: BotMemory, params: { 'bot-id': bot_id } },
    { value: 'channels', label: 'bots.tabs.channels', icon: MessageSquare, component: BotChannels, params: { 'bot-id': bot_id } },
    { value: 'access', label: 'bots.tabs.access', icon: ShieldAlert, component: BotAccess, params: { 'bot-id': bot_id, 'bot-type': bot.value?.type } },
    { value: 'tool-approval', label: 'bots.tabs.toolApproval', icon: Zap, component: BotToolApproval, params: { 'bot-id': bot_id } },
    { value: 'hooks', label: 'bots.tabs.hooks', icon: Workflow, component: BotHooks, params: { 'bot-id': bot_id }, containerWorkspaceOnly: true },
    { value: 'agents', label: 'bots.tabs.agents', icon: BotIcon, component: BotAgents, params: { 'bot-id': bot_id } },
    { value: 'email', label: 'bots.tabs.email', icon: Mail, component: BotEmail, params: { 'bot-id': bot_id } },
    ...(capabilitiesStore.loaded && capabilitiesStore.connectors
      ? [{ value: 'connectors', label: 'bots.tabs.connectors', icon: Plug, component: BotConnectors, params: { 'bot-id': bot_id } }]
      : []),
    { value: 'mcp', label: 'bots.tabs.mcp', icon: Link, component: BotMcp, params: { 'bot-id': bot_id } },
    { value: 'compaction', label: 'bots.tabs.compaction', icon: FileBox, component: BotCompaction, params: { 'bot-id': bot_id } },
    { value: 'schedule', label: 'bots.tabs.schedule', icon: Clock, component: BotSchedule, params: { 'bot-id': bot_id } },
    { value: 'skills', label: 'bots.tabs.skills', icon: BrainCircuit, component: BotSkills, params: { 'bot-id': bot_id } },
  ] satisfies Array<BotDetailsTabRule & {
    label: string
    icon: unknown
    component: unknown
    params: Record<string, unknown>
  }>
  return filterBotDetailsTabs(tabs, {
    canManageBot: canManageBot.value,
    botWorkspaceBackend: botWorkspaceBackend.value,
  })
})

const searchQuery = ref('')

const searchIndex = computed(() => {
  return [
    { tab: 'general', key: 'bots.settings.blocks.global', keywords: ['name', 'avatar', 'description', 'timezone'] },
    { tab: 'general', key: 'bots.settings.blocks.interaction', keywords: ['language', 'chat model', 'reasoning', 'model', 'llm', '模型', '换模型', '更换模型', 'モデル'] },
    { tab: 'general', key: 'bots.settings.blocks.context', keywords: ['browser', 'search', 'provider'] },
    { tab: 'general', key: 'bots.settings.blocks.multimedia', keywords: ['image', 'tts', 'transcription'] },
    { tab: 'general', key: 'bots.settings.dangerZone', keywords: ['delete', 'remove'] },
    { tab: 'container', key: 'bots.container.dataTitle', keywords: ['docker', 'image', 'gpu', 'volume'] },
    { tab: 'container', key: 'bots.container.metricsTitle', keywords: ['cpu', 'ram', 'storage'] },
    { tab: 'remote-runtime', key: 'bots.remoteRuntime.title', keywords: ['files', 'commands', 'computer', 'server', '文件', '命令', '电脑', '服务器', 'ファイル', 'コマンド'] },
    { tab: 'memory', key: 'bots.memory.title', keywords: ['vector', 'database', 'pgvector', 'embed'] },
    { tab: 'channels', key: 'bots.channels.configured', keywords: ['telegram', 'discord', 'wechat', 'slack'] },
    { tab: 'access', key: 'bots.access.title', keywords: ['permissions', 'acl', 'rules', 'allow', 'deny'] },
    { tab: 'tool-approval', key: 'bots.toolApproval.title', keywords: ['mcp', 'tools', 'review', 'bypass', 'approval'] },
    { tab: 'hooks', key: 'bots.hooks.title', keywords: ['hooks', 'events', 'tool calls', 'approval', 'workspace'] },
    { tab: 'agents', key: 'bots.tabs.agents', keywords: ['codex', 'claude code', 'external agent', 'acp'] },
    { tab: 'email', key: 'bots.email.title', keywords: ['smtp', 'imap', 'mailbox', 'bindings'] },
    ...(capabilitiesStore.loaded && capabilitiesStore.connectors
      ? [{ tab: 'connectors', key: 'bots.tabs.connectors', keywords: ['providers', 'apps', 'oauth', 'api', '连接器', 'コネクター'] }]
      : []),
    { tab: 'mcp', key: 'bots.tabs.mcp', keywords: ['servers', 'connect', 'custom mcp'] },
    { tab: 'compaction', key: 'bots.compaction.title', keywords: ['compress', 'summarize', 'context window'] },
    { tab: 'schedule', key: 'bots.schedule.title', keywords: ['cron', 'jobs', 'tasks', 'automation'] },
    { tab: 'skills', key: 'bots.skills.title', keywords: ['prompts', 'instructions', 'system prompt'] },
  ].map(item => ({
    ...item,
    translatedTitle: t(item.key)
  }))
})

const normalizedQuery = computed(() => searchQuery.value.trim().toLowerCase())

// Match a tab against the search box: its title, its tab key, and the keyword
// index so deep settings (e.g. "telegram", "pgvector") still surface the tab.
function tabMatches(tab: { value: string, label: string }): boolean {
  const q = normalizedQuery.value
  if (!q) return true
  if (t(tab.label).toLowerCase().includes(q)) return true
  if (t(`bots.tabs.${tab.value}`).toLowerCase().includes(q)) return true
  if (tab.value.toLowerCase().includes(q)) return true
  return searchIndex.value.some(item =>
    item.tab === tab.value && (
      item.translatedTitle.toLowerCase().includes(q)
      || item.keywords.some(k => k.toLowerCase().includes(q))
    ),
  )
}

function selectTab(value: string): void {
  searchQuery.value = ''
  // Mobile: the tab list and a tab's content are two addressable levels —
  // bare path = list, ?tab=x = content — so the tap is a real push and the
  // system back button pops straight back to the list. The synced-param
  // watcher picks the value up from the new query; the desktop path below
  // stays a replace (in-page state sync, not a navigation).
  if (isMobile.value) {
    tabPushedFromList.value = true
    void router.push({ query: { ...route.query, tab: value } }).catch(() => {})
    return
  }
  activeTab.value = value
}

// Mobile stack state for MasterDetailSidebarLayout, derived from the URL so a
// refresh / deep link (for example, ?tab=schedule) opens straight on the content and the
// KeepAlive'd page can never inherit a stale stack state from another bot.
const mobileDetailOpen = computed(() => {
  const tab = route.query.tab
  return typeof tab === 'string' && tab.length > 0
})
// Whether the current content was reached by tapping a tab in the list (i.e.
// history has the list entry right below). Cleared when the URL loses the tab
// or the bot changes, so deep links / bot switches fall through to replace
// instead of router.back() into an unrelated page.
const tabPushedFromList = ref(false)
watch(() => route.query.tab, (tab) => {
  if (!tab) tabPushedFromList.value = false
})
watch(() => route.params.botName, () => {
  tabPushedFromList.value = false
})
function closeMobileDetail(): void {
  if (tabPushedFromList.value) {
    tabPushedFromList.value = false
    router.back()
    return
  }
  const { tab: _drop, ...rest } = route.query
  void router.replace({ query: rest }).catch(() => {})
}

const groupedTabs = computed(() => {
  const coreKeys = ['overview', 'general', 'channels']
  const capabilityKeys = ['skills', 'hooks', 'tool-approval', 'agents', 'connectors', 'mcp', 'memory']
  const runtimeKeys = ['desktop', 'remote-runtime', 'container', 'network', 'schedule', 'compaction']
  const securityKeys = ['access', 'email']

  return [
    { key: 'core', items: tabList.value.filter(t => coreKeys.includes(t.value)) },
    { key: 'capabilities', items: tabList.value.filter(t => capabilityKeys.includes(t.value)) },
    { key: 'runtime', items: tabList.value.filter(t => runtimeKeys.includes(t.value)) },
    { key: 'security', items: tabList.value.filter(t => securityKeys.includes(t.value)) },
  ].filter(g => g.items.length > 0)
})

// Narrow the grouped nav in place while searching; drop emptied groups.
const displayGroups = computed(() =>
  groupedTabs.value
    .map(group => ({ ...group, items: group.items.filter(tabMatches) }))
    .filter(group => group.items.length > 0),
)

const activeComponent = computed(() => {
  return tabList.value.find(tab => tab.value === activeTab.value)
})

onMounted(() => {
  void capabilitiesStore.load()
})

const queryCache = useQueryCache()
const { mutateAsync: updateBot, isLoading: updateBotLoading } = useMutation({
  mutation: async ({ id, ...body }: Record<string, unknown> & { id: string }) => {
    const { data } = await putBotsById({ path: { id }, body, throwOnError: true })
    return data
  },
  onSettled: () => {
    queryCache.invalidateQueries({ key: getBotsQueryKey() })
    queryCache.invalidateQueries({ key: ['bot'] })
  },
})

async function fetchChecks(id: string): Promise<BotCheck[]> {
  const { data } = await getBotsByIdChecks({ path: { id }, throwOnError: true })
  return data?.items ?? []
}

const isEditingBotName = ref(false)
const botNameDraft = ref('')
const editNameInputRef = ref<InstanceType<typeof Input> | null>(null)

// Register the display name so the breadcrumb / back label reads a real name
// instead of the raw `bot-<uuid>` route param (keyed by both the URL identifier
// and the canonical id so either navigation form resolves).
watch(bot, (val) => {
  if (!val) return
  const currentName = (val.display_name || '').trim()
  if (currentName) {
    registerBotBreadcrumbName(routeIdentifier.value, currentName)
    registerBotBreadcrumbName(val.id, currentName)
  }
  if (!isEditingBotName.value) {
    botNameDraft.value = val.display_name || ''
  }
}, { immediate: true })

const activeTab = useSyncedQueryParam('tab', 'overview')
watch([tabList, activeTab], ([tabs, tab]) => {
  if (tab === 'acp') {
    activeTab.value = 'agents'
    return
  }
  if (tabs.some(item => item.value === tab)) return
  // 'connectors' only joins the list once the capability ping lands; don't
  // bounce a deep link / refresh to overview while that's still in flight.
  if (tab === 'connectors' && !capabilitiesStore.loaded) return
  activeTab.value = 'overview'
}, { immediate: true })
const avatarDialogOpen = ref(false)
const avatarUrlModel = ref('')
const avatarFallback = useAvatarInitials(() => bot.value?.display_name || botId.value || '')
const isSavingBotName = computed(() => updateBotLoading.value)

watch(() => bot.value?.avatar_url, (url) => {
  avatarUrlModel.value = url || ''
}, { immediate: true })

watch(avatarUrlModel, async (nextUrl) => {
  if (!bot.value) return
  const current = (bot.value.avatar_url || '').trim()
  if (nextUrl.trim() === current) return
  try {
    await updateBot({
      id: bot.value.id as string,
      avatar_url: nextUrl || undefined,
    })
    toast.success(t('bots.avatarUpdateSuccess'))
  } catch (error) {
    toast.error(resolveErrorMessage(error, t('bots.avatarUpdateFailed')))
  }
})
const canConfirmBotName = computed(() => {
  if (!bot.value) return false
  const nextName = botNameDraft.value.trim()
  if (!nextName) return false
  return nextName !== (bot.value.display_name || '').trim()
})
const {
  hasIssue,
  isPending: botLifecyclePending,
  issueTitle,
  statusLabel,
  statusVariant,
} = useBotStatusMeta(bot, t)

const botTypeLabel = computed(() => {
  const type = bot.value?.type
  if (type === 'personal' || type === 'public') return t('bots.types.' + type)
  return type ?? ''
})

const checks = ref<BotCheck[]>([])
const checksLoading = ref(false)

const containerMissing = ref(false)
const containerLoading = ref(false)
const snapshotsLoading = ref(false)
const snapshots = ref<BotContainerSnapshot[]>([])

watch(botId, () => {
  isEditingBotName.value = false
  botNameDraft.value = ''
})

// Resolve the live workspace backend as soon as the bot is known: the tab
// policy (container-only tabs hidden for local/remote) must not wait for the
// user to open the Container tab.
watch([botId, canManageBot], async ([id, manage]) => {
  containerInfo.value = null
  if (!id || !manage) return
  const result = await getBotsByBotIdContainer({ path: { bot_id: id } })
  if (result.error === undefined && botId.value === id) {
    containerInfo.value = result.data ?? null
  }
}, { immediate: true })

watch([activeTab, botId, canManageBot], ([tab]) => {
  if (!botId.value) {
    return
  }
  if (tab === 'container') {
    // Container data is management-only; chat-only members never see this tab.
    if (!canManageBot.value) {
      return
    }
    void loadContainerData(true)
    return
  }
  if (tab === 'overview') {
    void loadChecks(true)
  }
}, { immediate: true })

function resolveErrorMessage(error: unknown, fallback: string): string {
  return resolveApiErrorMessage(error, fallback)
}

function handleEditAvatar() {
  if (!bot.value || botLifecyclePending.value) return
  avatarDialogOpen.value = true
}

function handleStartEditBotName() {
  if (!bot.value) return
  isEditingBotName.value = true
  botNameDraft.value = bot.value.display_name || ''
  nextTick(() => {
    const el = editNameInputRef.value?.$el
    if (el) {
      const input = el instanceof HTMLInputElement ? el : el.querySelector('input')
      if (input) input.focus()
    }
  })
}

function handleCancelBotName() {
  isEditingBotName.value = false
  botNameDraft.value = bot.value?.display_name || ''
}

async function handleConfirmBotName() {
  if (!bot.value || !canConfirmBotName.value) {
    handleCancelBotName()
    return
  }
  const nextName = botNameDraft.value.trim()
  try {
    await updateBot({
      id: bot.value.id as string,
      display_name: nextName,
    })
    registerBotBreadcrumbName(routeIdentifier.value, nextName)
    registerBotBreadcrumbName(bot.value.id, nextName)
    isEditingBotName.value = false
    toast.success(t('bots.renameSuccess'))
  } catch (error) {
    toast.error(resolveErrorMessage(error, t('bots.renameFailed')))
  }
}

async function loadChecks(showToast: boolean) {
  checksLoading.value = true
  checks.value = []
  try {
    checks.value = await fetchChecks(botId.value)
  } catch (error) {
    if (showToast) {
      toast.error(resolveErrorMessage(error, t('bots.checks.loadFailed')))
    }
  } finally {
    checksLoading.value = false
  }
}

async function loadContainerData(showLoadingToast: boolean) {
  await capabilitiesStore.load()
  containerLoading.value = true
  try {
    const result = await getBotsByBotIdContainer({ path: { bot_id: botId.value } })
    if (result.error !== undefined) {
      if (result.response?.status === 404) {
        containerInfo.value = null
        containerMissing.value = true
        snapshots.value = []
        return
      }
      throw result.error
    }
    containerInfo.value = result.data
    containerMissing.value = false
    if (capabilitiesStore.snapshotSupported) {
      await loadSnapshots()
    }
  } catch (error) {
    if (showLoadingToast) {
      toast.error(resolveErrorMessage(error, t('bots.container.loadFailed')))
    }
  } finally {
    containerLoading.value = false
  }
}

async function loadSnapshots() {
  if (!containerInfo.value || !capabilitiesStore.snapshotSupported) {
    snapshots.value = []
    return
  }
  snapshotsLoading.value = true
  try {
    const { data } = await getBotsByBotIdContainerSnapshots({ path: { bot_id: botId.value }, throwOnError: true })
    snapshots.value = data.snapshots ?? []
  } catch (error) {
    snapshots.value = []
    toast.error(resolveErrorMessage(error, t('bots.container.snapshotLoadFailed')))
  } finally {
    snapshotsLoading.value = false
  }
}
</script>
