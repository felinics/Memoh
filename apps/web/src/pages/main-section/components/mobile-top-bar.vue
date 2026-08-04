<template>
  <!-- Mobile shell top bar (rendered only below the JS breakpoint): left ≡ to
       open the nav sheet when chat is active, ← to re-activate the chat panel
       when a secondary panel (terminal/browser/…) is on top. The ← never
       closes the secondary panel — 'always' renderers keep it alive behind
       chat. The right "+" menu mirrors the desktop tab-strip add actions with
       the same permission gates. -->
  <header class="flex h-11 shrink-0 items-center border-b border-border bg-background px-1.5">
    <Button
      variant="ghost"
      size="icon"
      shape="circle"
      :class="iconButtonClass"
      :title="leftLabel"
      :aria-label="leftLabel"
      @click="onLeft"
    >
      <Menu
        v-if="activePanelIsChat"
        :stroke-width="1.75"
      />
      <ChevronLeft
        v-else
        :stroke-width="1.75"
      />
    </Button>

    <!-- The flanks are equal-width boxes (icon button / spacer or menu), so a
         centered flex-1 title stays optically centered. -->
    <div class="min-w-0 flex-1 truncate text-center text-control font-medium">
      {{ title }}
    </div>

    <DropdownMenu v-if="hasAnyAction">
      <DropdownMenuTrigger as-child>
        <Button
          variant="ghost"
          size="icon"
          shape="circle"
          :class="iconButtonClass"
          :title="menuLabel"
          :aria-label="menuLabel"
        >
          <Plus :stroke-width="1.75" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem
          v-if="canWorkspaceExec"
          @select="workspaceTabs.openTerminal()"
        >
          <Terminal class="mr-2 size-3.5" />
          {{ t('chat.tabBarToolkit.newTerminal') }}
        </DropdownMenuItem>
        <DropdownMenuItem
          v-if="canManage"
          @select="workspaceTabs.openBrowser()"
        >
          <Globe class="mr-2 size-3.5" />
          {{ t('chat.tabBarToolkit.openBrowser') }}
        </DropdownMenuItem>
        <DropdownMenuItem
          v-if="canManage"
          @select="workspaceTabs.openDisplay()"
        >
          <Monitor class="mr-2 size-3.5" />
          {{ t('chat.tabBarToolkit.openDesktop') }}
        </DropdownMenuItem>
        <template v-if="!activePanelIsChat">
          <DropdownMenuSeparator v-if="canWorkspaceExec || canManage" />
          <DropdownMenuItem @select="closeActivePanel">
            <X class="mr-2 size-3.5" />
            {{ t('chat.topBar.closePanel') }}
          </DropdownMenuItem>
        </template>
      </DropdownMenuContent>
    </DropdownMenu>
    <!-- Spacer matching the icon-button box so the title keeps its centering
         when no panel action is permitted. -->
    <div
      v-else
      class="size-9 shrink-0"
    />
  </header>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { ChevronLeft, Globe, Menu, Monitor, Plus, Terminal, X } from 'lucide-vue-next'
import {
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@felinic/ui'
import { useChatStore } from '@/store/chat-list'
import { useChatSelectionStore } from '@/store/chat-selection'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { hasBotPermission } from '@/utils/bot-permissions'

const { t } = useI18n()

// Same chrome as the rail / dock-header icon buttons (muted at rest →
// foreground on hover); one owner so the two top-bar buttons can't drift.
// data-[state=open] mirrors the desktop "+" trigger's stay-lit-while-open
// philosophy (header-add-actions.vue) — reka sets it only on menu triggers.
const iconButtonClass = 'shrink-0 text-muted-foreground hover:text-foreground data-[state=open]:text-foreground' /* ui-allow-style */

const workspaceTabs = useWorkspaceTabsStore()
const { activePanelIsChat, activeId, api } = storeToRefs(workspaceTabs)
const chatStore = useChatStore()
const { currentBotId, bots } = storeToRefs(chatStore)
const selectionStore = useChatSelectionStore()

const currentBot = computed(() =>
  bots.value.find(bot => bot.id === currentBotId.value) ?? null,
)
const currentPermissions = computed(() => currentBot.value?.current_user_permissions ?? [])
// Same gates as the desktop tab-strip "+" menu (header-add-actions).
const canWorkspaceExec = computed(() => hasBotPermission(currentPermissions.value, 'workspace_exec'))
const canManage = computed(() => hasBotPermission(currentPermissions.value, 'manage'))
const hasAnyAction = computed(() =>
  canWorkspaceExec.value || canManage.value || !activePanelIsChat.value,
)

const leftLabel = computed(() =>
  activePanelIsChat.value ? t('chat.topBar.openNav') : t('chat.topBar.goBack'),
)
// "New panel" stops being accurate once the menu also carries Close panel
// (secondary panel active) — fall back to the generic "More" then.
const menuLabel = computed(() =>
  activePanelIsChat.value ? t('chat.tabBarToolkit.openMenu') : t('chat.tabBarToolkit.more'),
)
function onLeft() {
  if (activePanelIsChat.value) workspaceTabs.openMobileNav()
  else workspaceTabs.activateChatPanel()
}

const botLabel = computed(() =>
  currentBot.value?.display_name || currentBot.value?.name || '',
)

// Secondary-panel titles live on the dock panel, outside the reactive stores;
// subscribe to the active panel's title event so renames / browser address
// edits reach the bar. Chat titles are NOT read from here — they stay live
// through chatStore.activeSession (server-assigned titles arrive late).
const activePanelTitle = ref('')
let titleSub: { dispose: () => void } | null = null
watch([activeId, api], ([id, dock]) => {
  titleSub?.dispose()
  titleSub = null
  const panel = id ? dock?.getPanel(id) : undefined
  activePanelTitle.value = panel?.api.title ?? ''
  if (panel) {
    titleSub = panel.api.onDidTitleChange(() => {
      activePanelTitle.value = panel.api.title ?? ''
    })
  }
}, { immediate: true })
onBeforeUnmount(() => titleSub?.dispose())

const title = computed(() => {
  if (!activePanelIsChat.value) return activePanelTitle.value || botLabel.value
  if (selectionStore.sessionId) {
    return chatStore.activeSession?.title?.trim() || t('chat.untitledSession')
  }
  return botLabel.value
})

function closeActivePanel() {
  const id = activeId.value
  if (id) workspaceTabs.requestCloseTab(id)
}
</script>
