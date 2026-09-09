<template>
  <!-- Mobile shell top bar (rendered only below the JS breakpoint): left ≡ to
       open the nav sheet when chat is active, ← to re-activate the chat panel
       when a secondary panel (terminal/browser/…) is on top. The ← never
       closes the secondary panel — 'always' renderers keep it alive behind
       chat. The right "+" menu mirrors the desktop tab-strip add actions with
       the same permission gates. -->
  <MobileBar>
    <template #left>
      <MobileBarIconButton
        :icon="activePanelIsChat ? Menu : ChevronLeft"
        :label="leftLabel"
        @click="onLeft"
      />
    </template>

    <!-- The flanks are equal-width boxes (icon button / spacer or menu), so a
         centered flex-1 title stays optically centered. -->
    <div class="min-w-0 flex-1 truncate text-center text-control font-medium">
      {{ title }}
    </div>

    <template #right>
      <DropdownMenu v-if="hasAnyAction">
        <!-- as-child trigger must stay a plain ui Button: a wrapper component
             that declares its own click emit breaks reka's trigger wiring
             (see the warning in mobile-bar/icon-button.vue). -->
        <DropdownMenuTrigger as-child>
          <Button
            variant="ghost"
            size="icon"
            shape="circle"
            :class="mobileBarIconButtonClass"
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
    </template>
  </MobileBar>
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
import MobileBar from '@/components/mobile-bar/index.vue'
import MobileBarIconButton from '@/components/mobile-bar/icon-button.vue'
import { mobileBarIconButtonClass } from '@/components/mobile-bar/icon-button-class'
import { useChatStore } from '@/store/chat-list'
import { routeConversationLabel } from '@/store/chat-list.utils'
import { useChatSelectionStore } from '@/store/chat-selection'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { hasBotPermission } from '@/utils/bot-permissions'

const { t } = useI18n()

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
  activePanelIsChat.value ? t('chat.topBar.openNav') : t('common.back'),
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
    return chatStore.activeSession?.title?.trim() || routeConversationLabel(chatStore.activeSession) || t('chat.untitledSession')
  }
  return botLabel.value
})

// "New panel" stops being accurate once the menu also carries the close item
// (secondary panel active) — fall back to the generic "More" then.
const menuLabel = computed(() =>
  activePanelIsChat.value ? t('chat.tabBarToolkit.openMenu') : t('chat.tabBarToolkit.more'),
)

function closeActivePanel() {
  const id = activeId.value
  if (id) workspaceTabs.requestCloseTab(id)
}
</script>
