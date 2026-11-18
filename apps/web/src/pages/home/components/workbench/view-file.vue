<template>
  <div class="relative flex h-full w-full flex-col bg-surface-editor">
    <WorkbenchBreadcrumb :path="filePath">
      <template
        v-if="canPreview"
        #actions
      >
        <Button
          variant="ghost"
          tone="muted"
          size="icon-sm"
          shape="circle"
          class="size-6 shrink-0 p-0"
          :title="t('chat.openPreviewToSide')"
          :aria-label="t('chat.openPreviewToSide')"
          @click.stop="openPreviewToSide"
        >
          <Columns2 class="size-3.5" />
        </Button>
      </template>
    </WorkbenchBreadcrumb>
    <div class="min-h-0 flex-1">
      <FilePane
        v-if="hasActivated && filePath"
        :key="`file-pane:${currentBotId}:${filePath}`"
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
import { useI18n } from 'vue-i18n'
import { Columns2 } from 'lucide-vue-next'
import { Button } from '@felinic/ui'
import { isHtmlFile, isMarkdownFile } from '@/components/file-manager/utils'
import { useChatStore } from '@/store/chat-list'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { findGroupOfTab } from '@/workbench/tree'
import WorkbenchBreadcrumb from './workbench-breadcrumb.vue'
import { lazyPanelContent } from './lazy-panel-content'

// Keep the Monaco-bearing FilePane behind an inner async boundary so workspace
// idle prefetching of panel shells never pulls the 700 KB editor bundle into
// chat-only sessions until a file tab actually activates.
const FilePane = lazyPanelContent(() => import('../file-pane.vue').then(m => m.default))

const props = defineProps<{
  tabId: string
  filePath: string
  visible: boolean
}>()

const { t } = useI18n()
const { currentBotId } = storeToRefs(useChatStore())
const store = useWorkspaceTabsStore()

const hasActivated = ref(props.visible)
watch(() => props.visible, (isVisible) => {
  if (isVisible) hasActivated.value = true
})

const fileName = computed(() => {
  const idx = props.filePath.lastIndexOf('/')
  return idx >= 0 ? props.filePath.slice(idx + 1) : props.filePath
})

const canPreview = computed(() => isMarkdownFile(fileName.value) || isHtmlFile(fileName.value))

function openPreviewToSide() {
  const ownerGroup = findGroupOfTab(store.workbench.root, props.tabId)
  store.openPreview(props.filePath, t('chat.previewTab', { name: fileName.value }), ownerGroup?.id)
}
</script>
