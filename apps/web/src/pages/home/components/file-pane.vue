<template>
  <div class="flex flex-col h-full min-w-0 overflow-hidden">
    <FileViewer
      v-if="botId"
      ref="viewerRef"
      :bot-id="botId"
      :file="fileInfo"
      :readonly="!canWrite"
      :visible="visible"
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

const props = withDefaults(defineProps<{
  botId?: string
  filePath: string
  tabId: string
  visible?: boolean
}>(), {
  botId: undefined,
  visible: true,
})

const viewerRef = ref<InstanceType<typeof FileViewer> | null>(null)

const chatStore = useChatStore()
const { currentBotId } = storeToRefs(chatStore)
const workspaceTabs = useWorkspaceTabsStore()

const botId = computed(() => (props.botId ?? currentBotId.value ?? '').trim())
const currentBot = computed(() =>
  chatStore.bots.find(bot => bot.id === botId.value) ?? null,
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
  // setFileDirty guards by botId and promotes the preview tab on first edit.
  workspaceTabs.setFileDirty(props.tabId, dirty, botId.value)
}

const saveHandler = async () => (await viewerRef.value?.save()) ?? true

// Let the tab close-confirm dialog write this file even while it's backgrounded.
// FileViewer stays mounted for the whole panel lifetime (view-file.vue never
// unmounts it once shown), so its save() is live while the tab is hidden.
onMounted(() => {
  workspaceTabs.registerFileSaveHandler(props.tabId, saveHandler, botId.value)
})

onBeforeUnmount(() => {
  workspaceTabs.setFileDirty(props.tabId, false, botId.value)
  workspaceTabs.unregisterFileSaveHandler(props.tabId, saveHandler)
})
</script>
