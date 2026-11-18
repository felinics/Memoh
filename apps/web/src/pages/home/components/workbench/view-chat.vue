<template>
  <div class="relative flex h-full w-full flex-col">
    <div class="min-h-0 flex-1">
      <KeepAlive>
        <ChatPane
          v-if="visible"
          :key="`chat-pane:${currentBotId}:${tabId}`"
          :tab-id="tabId"
          :session-id="sessionId"
          :visible="visible"
          :active="focused"
        />
      </KeepAlive>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useChatStore } from '@/store/chat-list'
import ChatPane from '../chat-pane.vue'

const props = defineProps<{
  tabId: string
  sessionId: string | null
  visible: boolean
  focused: boolean
}>()

const chatStore = useChatStore()
const { currentBotId } = storeToRefs(chatStore)

watch(
  [currentBotId, () => props.sessionId, () => props.visible],
  ([botId, sessionId, isVisible]) => {
    const bid = botId?.trim() ?? ''
    if (!bid) return
    chatStore.bindChatView(
      props.tabId,
      {
        botId: bid,
        sessionId,
        viewId: props.tabId,
      },
      isVisible,
    )
  },
  { immediate: true },
)

watch(
  () => props.focused,
  (isFocused) => {
    if (isFocused) chatStore.focusChatView(props.tabId)
  },
  { immediate: true },
)

onBeforeUnmount(() => chatStore.unbindChatView(props.tabId))
</script>
