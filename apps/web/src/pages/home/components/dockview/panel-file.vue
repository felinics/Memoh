<template>
  <DockPanelFrame editor-surface>
    <template #header>
      <PanelBreadcrumb :path="filePath" />
    </template>
    <FilePane
      v-if="hasActivated && filePath"
      :key="`file-pane:${currentBotId}:${filePath}`"
      :tab-id="props.params.api.id"
      :file-path="filePath"
      :visible="visible"
    />
  </DockPanelFrame>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import type { DockviewApi, DockviewPanelApi } from 'dockview-vue'
import { withStylesheetRecovery } from '@/lib/chunk-load-error'
import { useChatStore } from '@/store/chat-list'
import { usePanelVisible } from './use-panel-visible'
import PanelBreadcrumb from './panel-breadcrumb.vue'
import DockPanelFrame from './panel-frame.vue'

// Keep the Monaco-bearing FilePane behind an inner async boundary so workspace
// idle prefetching of panel shells never pulls the 700 KB editor bundle into
// chat-only sessions until a file tab actually activates.
const FilePane = defineAsyncComponent(withStylesheetRecovery(() => import('../file-pane.vue').then(m => m.default)))

const props = defineProps<{
  params: {
    params: { filePath?: string }
    api: DockviewPanelApi
    containerApi: DockviewApi
  }
}>()

const chatStore = useChatStore()
const { currentBotId } = storeToRefs(chatStore)

const visible = usePanelVisible(props.params.api)
const hasActivated = ref(visible.value)
watch(visible, (isVisible) => {
  if (isVisible) hasActivated.value = true
})

const filePath = computed(() => props.params.params.filePath ?? '')
</script>
