<template>
  <DockPanelFrame>
    <KeepAlive>
      <TrajectoryPane v-if="visible" />
    </KeepAlive>
  </DockPanelFrame>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import type { DockviewApi, DockviewPanelApi } from 'dockview-vue'
import { useChatStore } from '@/store/chat-list'
import { provideChatViewTarget } from '../../composables/useChatViewContext'
import { usePanelVisible } from './use-panel-visible'
import DockPanelFrame from './panel-frame.vue'

// A trajectory tab attaches to the session's shared chat view, so it reads
// the same transcript window as the chat tab and keeps the runtime
// subscription alive while it is visible.
const props = defineProps<{
  params: {
    params: { sessionId?: string }
    api: DockviewPanelApi
    containerApi: DockviewApi
  }
}>()

const TrajectoryPane = defineAsyncComponent(() => import('../trajectory/trajectory-pane.vue'))

const { t } = useI18n()
const chatStore = useChatStore()
const { currentBotId } = storeToRefs(chatStore)
const visible = usePanelVisible(props.params.api)
const panelId = props.params.api.id
const sessionId = computed(() => (typeof props.params.params.sessionId === 'string' ? props.params.params.sessionId.trim() : ''))

provideChatViewTarget(computed(() => ({
  botId: currentBotId.value?.trim() ?? '',
  sessionId: sessionId.value || null,
  viewId: panelId,
})))

watch([currentBotId, sessionId, visible], ([botId, session, isVisible]) => {
  const bid = botId?.trim() ?? ''
  if (!bid || !session) return
  chatStore.bindChatView(panelId, { botId: bid, sessionId: session, viewId: panelId }, isVisible)
}, { immediate: true })

watch(() => t('chat.trajectory.title'), title => props.params.api.setTitle(title), { immediate: true })

onBeforeUnmount(() => chatStore.unbindChatView(panelId))
</script>
