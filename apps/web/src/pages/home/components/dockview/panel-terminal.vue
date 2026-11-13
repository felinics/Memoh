<template>
  <DockPanelFrame>
    <TerminalPane
      v-if="mounted && currentBotId"
      :key="`terminal-pane:${currentBotId}:${props.params.api.id}`"
      :bot-id="currentBotId"
      :tab-id="props.params.api.id"
      :active="visible"
    />
  </DockPanelFrame>
</template>

<script setup lang="ts">
import { defineAsyncComponent, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import type { DockviewApi, DockviewPanelApi } from 'dockview-vue'
import { withStylesheetRecovery } from '@/lib/chunk-load-error'
import { useChatStore } from '@/store/chat-list'
import { usePanelVisible } from './use-panel-visible'
import DockPanelFrame from './panel-frame.vue'

const TerminalPane = defineAsyncComponent(withStylesheetRecovery(() => import('../terminal-pane.vue').then(m => m.default)))

const props = defineProps<{
  params: {
    params: Record<string, unknown>
    api: DockviewPanelApi
    containerApi: DockviewApi
  }
}>()

const chatStore = useChatStore()
const { currentBotId } = storeToRefs(chatStore)

const visible = usePanelVisible(props.params.api)

// Mount lazily on first reveal, then keep the pane mounted for the panel's whole
// life. A KeepAlive/v-if toggle would DETACH the xterm DOM on every tab switch,
// and xterm does not survive detach/reattach: its renderer dimensions reset, so
// reattaching forces a reflow + refit each time — the visible switch lag. Staying
// mounted lets dockview's own display:none hide the inactive panel instead, so
// switching tabs is a pure visibility flip (the `active` prop drives fit/connect).
const mounted = ref(props.params.api.isVisible)
watch(visible, (isVisible) => {
  if (isVisible) mounted.value = true
})
</script>
