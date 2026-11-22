<template>
  <div class="relative flex h-full w-full flex-col bg-surface-editor">
    <WorkbenchBreadcrumb :path="filePath" />
    <div class="min-h-0 flex-1">
      <FilePane
        v-if="hasActivated && filePath && effectiveBotId"
        :key="`file-pane:${effectiveBotId}:${tabId}:${filePath}`"
        :bot-id="effectiveBotId"
        :tab-id="tabId"
        :file-path="filePath"
        :visible="visible"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useChatStore } from '@/store/chat-list'
import WorkbenchBreadcrumb from './workbench-breadcrumb.vue'
import { lazyPanelContent } from './lazy-panel-content'

// Keep the Monaco-bearing FilePane behind an inner async boundary so workspace
// idle prefetching of panel shells never pulls the 700 KB editor bundle into
// chat-only sessions until a file tab actually activates.
const FilePane = lazyPanelContent(() => import('../file-pane.vue').then(m => m.default))

const props = defineProps<{
  botId?: string
  tabId: string
  filePath: string
  visible: boolean
}>()

const { currentBotId } = storeToRefs(useChatStore())
const effectiveBotId = computed(() => (props.botId ?? currentBotId.value ?? '').trim())

const hasActivated = ref(props.visible)
watch(() => props.visible, (isVisible) => {
  if (isVisible) hasActivated.value = true
})
</script>
