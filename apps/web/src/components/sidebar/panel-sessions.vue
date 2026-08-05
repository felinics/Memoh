<template>
  <div class="flex flex-col h-full min-w-0">
    <SidebarPanelHeader
      :label="t('chat.quickActions')"
      label-class="pl-[11px]"
      class="px-2 pb-0.5 pt-2"
    />
    <!-- Action rows share the sidebar icon column: px-[11px] + 18px icon puts the
         glyph at x=19 and the label at x=45, matching the nav tab, session rows
         and Settings so icons line up vertically and labels share one x. -->
    <div class="flex flex-col px-2 pb-0.5 shrink-0">
      <SidebarNavButton
        :disabled="!currentBotId"
        @click="handleNewSession"
      >
        <SquarePen
          :stroke-width="1.75"
          class="size-[18px]"
        />
        {{ t('chat.newSession') }}
      </SidebarNavButton>
      <SidebarNavButton
        :disabled="!currentBotId"
        @click="handleBotSettings"
      >
        <Settings2
          :stroke-width="1.75"
          class="size-[18px]"
        />
        {{ t('chat.botSettings') }}
      </SidebarNavButton>
    </div>

    <!--
    EXPERIMENT (archived): no Quick Actions heading — New Session / Bot Settings ride
    straight up under the Chat nav tab, icons aligned in the same x=19 column.
    The core problem: the Chat tab uses font-[550] and looks like a "header" for
    whatever is below it, but New Session / Bot Settings are ACTIONS not sub-items,
    so they need visual separation. Reducing font weight to font-normal made the
    actions look too faint; keeping font-medium made them read as peers of "Chat".
    There's no in-between that works without the Quick Actions heading providing
    the structural break. Keeping this block here for future reference.

    <div class="flex flex-col px-2 pb-0.5 pt-0.5 shrink-0">
      <Button variant="ghost" block
        class="h-9 justify-start gap-[11px] px-[11px] text-control font-normal text-foreground/92 dark:text-[color:oklch(0.86_0_0)]"
        :disabled="!currentBotId" @click="handleNewSession">
        <span class="relative size-[18px] shrink-0">
          <span class="absolute -inset-[3px] flex items-center justify-center rounded-full
                       bg-[color:oklch(0.93_0_0)] text-[color:oklch(0.2_0_0)]
                       dark:bg-[color:oklch(0.27_0_0)] dark:text-[color:oklch(0.82_0_0)]">
            <Plus :stroke-width="2.2" class="size-[14px]" />
          </span>
        </span>
        {{ t('chat.newSession') }}
      </Button>
      <Button variant="ghost" block
        class="h-9 justify-start gap-3 px-[11px] text-control font-normal text-foreground/92 dark:text-[color:oklch(0.86_0_0)]"
        :disabled="!currentBotId" @click="handleBotSettings">
        <Settings2 :stroke-width="1.75" class="size-[17px]" />
        {{ t('chat.botSettings') }}
      </Button>
    </div>

    Color calibration for the disc (dark mode):
      sidebar bg oklch(0.185) ≈ rgb 37
      disc target: rgb 37 + Δ18 ≈ rgb 55 → oklch(0.27)   (ref app had Δ18 between bg and disc)
      plus: oklch(0.82) off-white (ref app ≈ rgb 194 → oklch 0.80)
    Light mode: disc oklch(0.93) one step off white, plus oklch(0.2) near-black.
    Icon alignment: 18px outer layout box (= same slot as all other icons) with
    the 24px visual disc via absolute -inset-[3px]; icon center x=28, text x=48.
    -->

    <!-- Folders is a SIBLING section of Recents: folders of workdir-bound
         chats above, the ungrouped timeline below. -->
    <FoldersSection />

    <Recents class="flex-1 min-h-0" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { SquarePen, Settings2 } from 'lucide-vue-next'
import { useChatStore } from '@/store/chat-list'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import SidebarPanelHeader from './panel-header.vue'
import SidebarNavButton from './nav-button.vue'
import FoldersSection from './folders-section.vue'
import Recents from './recents.vue'

const { t } = useI18n()
const router = useRouter()
const chatStore = useChatStore()
const workspaceTabs = useWorkspaceTabsStore()
const { currentBotId, bots } = storeToRefs(chatStore)

const currentBot = computed(() =>
  bots.value.find(bot => bot.id === currentBotId.value) ?? null,
)

function handleNewSession() {
  if (!currentBotId.value) return
  // Opens (or focuses) the single draft tab; its activation resets the view to a
  // fresh draft (selectDraft), so no separate createNewSession is needed.
  workspaceTabs.openDraftChat({ title: t('chat.newSession'), explicitSelection: false })
}

// Navigate to the current bot's settings overview.
function handleBotSettings() {
  const botId = currentBotId.value
  if (!botId) return
  void router.push({ name: 'bot-detail', params: { botName: currentBot.value?.name ?? botId } })
}
</script>
