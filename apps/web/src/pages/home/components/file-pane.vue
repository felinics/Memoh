<template>
  <div class="flex flex-col h-full min-w-0 overflow-hidden">
    <FileViewer
      v-if="botId"
      ref="viewerRef"
      :bot-id="botId"
      :file="fileInfo"
      :readonly="!canWrite"
      :keyboard-active="workspaceTabs.activeId === tabId"
      @update:dirty="handleDirty"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { storeToRefs } from 'pinia'
import type { HandlersFsFileInfo } from '@memohai/sdk'
import FileViewer from '@/components/file-manager/file-viewer.vue'
import { useChatStore } from '@/store/chat-list'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { hasBotPermission } from '@/utils/bot-permissions'

const props = defineProps<{
  filePath: string
  tabId: string
}>()

const viewerRef = ref<InstanceType<typeof FileViewer> | null>(null)

const chatStore = useChatStore()
const { currentBotId } = storeToRefs(chatStore)
const workspaceTabs = useWorkspaceTabsStore()

const botId = computed(() => currentBotId.value ?? '')
const currentBot = computed(() =>
  chatStore.bots.find(bot => bot.id === currentBotId.value) ?? null,
)
const canWrite = computed(() =>
  hasBotPermission(currentBot.value?.current_user_permissions, 'workspace_write'),
)

const fileInfo = computed<HandlersFsFileInfo>(() => {
  const path = props.filePath
  const idx = path.lastIndexOf('/')
  const name = idx >= 0 ? path.slice(idx + 1) : path
  return {
    path,
    name,
    isDir: false,
  } as HandlersFsFileInfo
})

function handleDirty(dirty: boolean) {
  workspaceTabs.setFileDirty(props.tabId, dirty)
  // First edit pins the (previously ephemeral) tab: it leaves the preview slot so
  // opening another file no longer replaces it. Never un-pinned on undo.
  if (dirty) workspaceTabs.pinPanel(props.tabId)
}

// Let the tab close-confirm dialog write this file even while it's backgrounded.
// FileViewer stays mounted (renderer:'always' + KeepAlive), so its save() is live
// for the whole panel lifetime.
onMounted(() => {
  workspaceTabs.registerFileSaveHandler(
    props.tabId,
    async () => (await viewerRef.value?.save()) ?? true,
  )
})

onBeforeUnmount(() => {
  workspaceTabs.setFileDirty(props.tabId, false)
  workspaceTabs.unregisterFileSaveHandler(props.tabId)
})
</script>
