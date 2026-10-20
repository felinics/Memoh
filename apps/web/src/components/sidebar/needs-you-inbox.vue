<template>
  <section
    v-if="unread.total"
    class="px-2 pt-3 pb-2"
    :aria-label="t('notifications.inbox')"
    data-slot="needs-you-inbox"
  >
    <SidebarPanelHeader
      :label="t('notifications.inbox')"
      label-class="pl-[11px]"
      class="pb-1 pr-3"
    >
      <span class="text-xs tabular-nums text-muted-foreground">{{ unread.total }}</span>
    </SidebarPanelHeader>
    <Item
      v-for="entry in visibleEntries"
      :key="entry.sessionId"
      as="button"
      type="button"
      size="sm"
      class="w-full flex-nowrap px-3 py-2 text-left"
      :title="entry.title || t('chat.untitledSession')"
      @click="openSession(entry)"
    >
      <Avatar class="size-6 shrink-0">
        <AvatarImage
          v-if="botFor(entry.botId)?.avatar_url"
          :src="botFor(entry.botId)?.avatar_url ?? ''"
          :alt="botName(entry.botId)"
        />
        <AvatarFallback class="text-xs">
          {{ botName(entry.botId).slice(0, 1) }}
        </AvatarFallback>
      </Avatar>
      <div class="min-w-0 flex-1">
        <div class="truncate text-control font-semibold">
          {{ entry.title || t('chat.untitledSession') }}
        </div>
        <div class="flex min-w-0 items-center gap-1 text-xs text-muted-foreground">
          <span class="truncate">{{ botName(entry.botId) }}</span>
          <span aria-hidden="true">·</span>
          <span class="shrink-0">{{ t(`notifications.kind.${entry.kind}`) }}</span>
        </div>
      </div>
      <StatusDot
        :status="entry.kind === 'needs_input' ? 'warning' : 'error'"
        class="shrink-0"
        aria-hidden="true"
      />
    </Item>
    <Button
      v-if="unread.total > 5"
      variant="ghost"
      size="sm"
      block
      @click="expanded = !expanded"
    >
      {{ expanded ? t('notifications.showLess') : t('notifications.showAll', { count: unread.total }) }}
    </Button>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Avatar, AvatarFallback, AvatarImage, Button, Item, StatusDot } from '@felinic/ui'
import { useRouter } from 'vue-router'
import { useChatStore } from '@/store/chat-list'
import { useUnreadStore, type UnreadEntry } from '@/store/unread'
import { sessionAttentionPath } from '@/utils/session-attention'
import SidebarPanelHeader from './panel-header.vue'

const { t } = useI18n()
const unread = useUnreadStore()
const chat = useChatStore()
const router = useRouter()
const expanded = ref(false)
const visibleEntries = computed(() => expanded.value ? unread.inbox : unread.inbox.slice(0, 5))
watch(() => unread.total, (total) => { if (!total) expanded.value = false })
const botFor = (id: string) => chat.bots.find(bot => bot.id === id)
const botName = (id: string) => botFor(id)?.display_name || botFor(id)?.name || t('sidebar.bots')

function openSession(entry: UnreadEntry) {
  void router.push(sessionAttentionPath(entry.botId, entry.sessionId))
}
</script>
