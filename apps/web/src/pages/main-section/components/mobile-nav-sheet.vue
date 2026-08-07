<template>
  <!-- Mobile main navigation: the desktop rail's contents (bot switcher, view
       switch, sessions/files/schedule panels, Settings entry) hosted in a left
       Sheet. State lives in the workspace store (mobileNavOpen), not persisted.
       Selection dismisses the sheet on its own (see watchers below); the
       built-in corner close is hidden — dismissal is a selection, a scrim tap,
       or Esc. -->
  <Sheet
    :open="mobileNavOpen"
    @update:open="onSheetOpenChange"
  >
    <SheetContent
      side="left"
      class="w-72 bg-sidebar text-sidebar-foreground p-0 [&>button]:hidden"
    >
      <SheetHeader class="sr-only">
        <SheetTitle>{{ t('chat.mobileNav.title') }}</SheetTitle>
        <SheetDescription>{{ t('chat.mobileNav.description') }}</SheetDescription>
      </SheetHeader>
      <div class="flex h-full w-full min-h-0 flex-col overflow-hidden">
        <header class="flex h-11 shrink-0 items-center pl-3 pr-2">
          <div class="min-w-0 flex-1">
            <BotSwitcher full-width />
          </div>
        </header>

        <!-- View switch rows reuse the rail's nav-button owner so the icon
             column and label x match the desktop sidebar geometry. -->
        <nav class="flex shrink-0 flex-col px-2 pb-1">
          <SidebarNavButton
            v-for="view in availableViews"
            :key="view.id"
            :active="sidebarView === view.id"
            @click="workspaceTabs.selectSidebarView(view.id)"
          >
            <component
              :is="view.icon"
              :stroke-width="1.75"
              class="size-[18px]"
            />
            {{ view.label }}
          </SidebarNavButton>
        </nav>

        <!-- Same panels the desktop rail hosts, with the same bottom fade into
             the Settings row. -->
        <div class="relative min-h-0 flex-1 overflow-hidden">
          <PanelSessions
            v-show="sidebarView === 'sessions'"
            class="h-full"
          />
          <PanelFiles
            v-if="canWorkspaceRead"
            v-show="sidebarView === 'files'"
            class="h-full"
          />
          <PanelSchedule
            v-show="sidebarView === 'schedule'"
            class="h-full"
          />
          <div class="pointer-events-none absolute inset-x-0 bottom-0 h-6 bg-gradient-to-t from-sidebar to-transparent" />
        </div>

        <div class="relative z-1 shrink-0 bg-sidebar px-2 pt-1 pb-2">
          <SidebarNavButton
            :aria-label="t('sidebar.settings')"
            @click="goSettings"
          >
            <Settings
              :stroke-width="1.75"
              class="size-[18px]"
            />
            {{ t('sidebar.settings') }}
          </SidebarNavButton>
        </div>
      </div>
    </SheetContent>
  </Sheet>
</template>

<script setup lang="ts">
import { computed, watch, type Component } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { Calendar, Files, MessageCircle, Settings } from 'lucide-vue-next'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import { useChatSelectionStore } from '@/store/chat-selection'
import { useWorkspaceTabsStore, type SidebarView } from '@/store/workspace-tabs'
import { hasBotPermission } from '@/utils/bot-permissions'
import BotSwitcher from '@/components/sidebar/bot-switcher.vue'
import SidebarNavButton from '@/components/sidebar/nav-button.vue'
import PanelSessions from '@/components/sidebar/panel-sessions.vue'
import PanelFiles from '@/components/sidebar/panel-files.vue'
import PanelSchedule from '@/components/sidebar/panel-schedule.vue'

interface NavView {
  id: SidebarView
  label: string
  icon: Component
}

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const workspaceTabs = useWorkspaceTabsStore()
const { mobileNavOpen, sidebarView, activeId } = storeToRefs(workspaceTabs)
const chatStore = useChatStore()
const { currentBotId, bots } = storeToRefs(chatStore)
const selectionStore = useChatSelectionStore()

const currentBot = computed(() =>
  bots.value.find(bot => bot.id === currentBotId.value) ?? null,
)
const canWorkspaceRead = computed(() =>
  hasBotPermission(currentBot.value?.current_user_permissions, 'workspace_read'),
)

const availableViews = computed<NavView[]>(() => {
  const views: NavView[] = [
    { id: 'sessions', label: t('chat.activityBar.sessions'), icon: MessageCircle },
  ]
  if (canWorkspaceRead.value) {
    views.push({ id: 'files', label: t('chat.activityBar.files'), icon: Files })
  }
  views.push({ id: 'schedule', label: t('chat.activityBar.schedule'), icon: Calendar })
  return views
})

// Same fallback as the desktop rail: a persisted view that lost its permission
// gate must not render an empty content area. Duplicated here because the rail
// (which owns this watch on desktop) is not mounted under the mobile shell.
watch(availableViews, (views) => {
  if (!views.some(view => view.id === sidebarView.value)) {
    workspaceTabs.selectSidebarView('sessions')
  }
}, { immediate: true })

function onSheetOpenChange(open: boolean) {
  if (open) workspaceTabs.openMobileNav()
  else workspaceTabs.closeMobileNav()
}

// Select-to-dismiss: anything that changes what the main area shows closes the
// sheet — a session pick (selection store), a route change (bot switch,
// Settings, schedule manage), or a dock activation (a file/terminal opened
// from one of the hosted panels).
watch(() => selectionStore.sessionId, () => workspaceTabs.closeMobileNav())
watch(() => route.fullPath, () => workspaceTabs.closeMobileNav())
watch(activeId, () => workspaceTabs.closeMobileNav())

function goSettings() {
  workspaceTabs.closeMobileNav()
  void router.push('/settings')
}
</script>
