<template>
  <DockPanelFrame editor-surface>
    <template #header>
      <PanelBreadcrumb :path="filePath" />
    </template>

    <PanePlaceholder v-if="!canPreview">
      <template #icon>
        <FileText class="size-10 opacity-30" />
      </template>
      {{ t('bots.files.previewNotAvailable') }}
    </PanePlaceholder>

    <!-- Full-area spinner only on the first load (`isPending`). Background
         refetches keep the rendered markdown/html mounted and swap in place. -->
    <PanePlaceholder
      v-else-if="isPending"
      loading
    >
      {{ t('common.loading') }}
    </PanePlaceholder>

    <PanePlaceholder v-else-if="error && (!data || isDeletedError)">
      <template #icon>
        <FileX
          v-if="isDeletedError"
          class="size-10 text-destructive opacity-40"
        />
        <FileText
          v-else
          class="size-10 opacity-30"
        />
      </template>
      {{ isDeletedError ? t('bots.files.externalChange.deleted') : resolveApiErrorMessage(error, t('bots.files.readFailed')) }}
      <template #action>
        <Button
          variant="outline"
          size="sm"
          @click="() => void refetch()"
        >
          <RefreshCw class="mr-1.5 size-3" />
          {{ t('common.retry') }}
        </Button>
      </template>
    </PanePlaceholder>

    <MarkdownPreview
      v-else-if="isMd"
      :content="data?.content ?? ''"
      class="h-full"
    />
    <HtmlPreview
      v-else-if="isHtml"
      :content="data?.content ?? ''"
      class="h-full"
    />
  </DockPanelFrame>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { FileText, FileX, RefreshCw } from 'lucide-vue-next'
import { Button, PanePlaceholder } from '@felinic/ui'
import type { DockviewApi, DockviewPanelApi } from 'dockview-vue'
import { withStylesheetRecovery } from '@/lib/chunk-load-error'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { isContainerFileNotFound, useContainerTextFileQuery } from '@/components/file-manager/use-container-text-file'
import { isMarkdownFile, isHtmlFile } from '@/components/file-manager/utils'
import { useChatStore } from '@/store/chat-list'
import { usePanelVisible } from './use-panel-visible'
import PanelBreadcrumb from './panel-breadcrumb.vue'
import DockPanelFrame from './panel-frame.vue'

const MarkdownPreview = defineAsyncComponent(withStylesheetRecovery(() => import('@/components/markdown-preview/index.vue').then(m => m.default)))
const HtmlPreview = defineAsyncComponent(withStylesheetRecovery(() => import('@/components/html-preview/index.vue').then(m => m.default)))

const props = defineProps<{
  params: {
    params: { filePath?: string }
    api: DockviewPanelApi
    containerApi: DockviewApi
  }
}>()

const { t } = useI18n()
const chatStore = useChatStore()
const { currentBotId, fsChangedAt } = storeToRefs(chatStore)

const visible = usePanelVisible(props.params.api)
const filePath = computed(() => props.params.params.filePath ?? '')
const filename = computed(() => {
  const path = filePath.value
  const idx = path.lastIndexOf('/')
  return idx >= 0 ? path.slice(idx + 1) : path
})
const isMd = computed(() => isMarkdownFile(filename.value))
const isHtml = computed(() => isHtmlFile(filename.value))
const canPreview = computed(() => isMd.value || isHtml.value)

const { data, error, isPending, refetch } = useContainerTextFileQuery(
  () => currentBotId.value ?? '',
  filePath,
  { visible, enabled: canPreview },
)

const isDeletedError = computed(() => Boolean(error.value && isContainerFileNotFound(error.value)))

watch(fsChangedAt, () => {
  if (!visible.value || !canPreview.value || !currentBotId.value) return
  if (!chatStore.affectsPath(filePath.value)) return
  void refetch()
})
</script>
