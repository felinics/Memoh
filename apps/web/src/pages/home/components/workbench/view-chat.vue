<template>
  <KeepAlive>
    <ChatPane
      v-if="visible && effectiveBotId"
      :key="`chat-pane:${effectiveBotId}:${tabId}`"
      :bot-id="effectiveBotId"
      :tab-id="tabId"
      :session-id="sessionId"
      :visible="visible"
      :active="focused"
    />
  </KeepAlive>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useChatStore } from '@/store/chat-list'
import ChatPane from '../chat-pane.vue'

const props = defineProps<{
  botId?: string
  tabId: string
  sessionId: string | null
  visible: boolean
  focused: boolean
}>()

const chatStore = useChatStore()
const { currentBotId } = storeToRefs(chatStore)
const effectiveBotId = computed(() => (props.botId ?? currentBotId.value ?? '').trim())

watch(
  [effectiveBotId, () => props.sessionId, () => props.visible],
  ([bid, sessionId, isVisible]) => {
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

onBeforeUnmount(() => chatStore.unbindChatView(props.tabId, effectiveBotId.value || undefined))
</script>
