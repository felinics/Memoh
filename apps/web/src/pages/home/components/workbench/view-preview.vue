<template>
  <div class="relative flex h-full w-full flex-col bg-surface-editor">
    <WorkbenchBreadcrumb :path="filePath" />
    <div class="min-h-0 flex-1">
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
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { FileText, FileX, RefreshCw } from 'lucide-vue-next'
import { Button, PanePlaceholder } from '@felinic/ui'
import { withStylesheetRecovery } from '@/lib/chunk-load-error'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { isContainerFileNotFound, useContainerTextFileQuery } from '@/components/file-manager/use-container-text-file'
import { isMarkdownFile, isHtmlFile } from '@/components/file-manager/utils'
import { useChatStore } from '@/store/chat-list'
import { workbenchFileBaseName } from '@/workbench/titles'
import WorkbenchBreadcrumb from './workbench-breadcrumb.vue'

const MarkdownPreview = defineAsyncComponent(withStylesheetRecovery(() => import('@/components/markdown-preview/index.vue').then(m => m.default)))
const HtmlPreview = defineAsyncComponent(withStylesheetRecovery(() => import('@/components/html-preview/index.vue').then(m => m.default)))

const props = defineProps<{
  botId?: string
  filePath: string
  visible: boolean
}>()

const { t } = useI18n()
const chatStore = useChatStore()
const { currentBotId, fsChangedAt } = storeToRefs(chatStore)
const effectiveBotId = computed(() => (props.botId ?? currentBotId.value ?? '').trim())

const filename = computed(() => workbenchFileBaseName(props.filePath))
const isMd = computed(() => isMarkdownFile(filename.value))
const isHtml = computed(() => isHtmlFile(filename.value))
const canPreview = computed(() => isMd.value || isHtml.value)

const { data, error, isPending, refetch } = useContainerTextFileQuery(
  () => effectiveBotId.value,
  () => props.filePath,
  { visible: () => props.visible, enabled: canPreview },
)

const isDeletedError = computed(() => Boolean(error.value && isContainerFileNotFound(error.value)))

watch(fsChangedAt, () => {
  if (!props.visible || !canPreview.value || !effectiveBotId.value) return
  if (props.botId !== undefined && currentBotId.value?.trim() !== effectiveBotId.value) return
  if (!chatStore.affectsPath(props.filePath)) return
  void refetch()
})
</script>
