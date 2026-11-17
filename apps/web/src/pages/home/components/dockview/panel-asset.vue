<template>
  <DockPanelFrame editor-surface>
    <PanePlaceholder
      v-if="loading"
      loading
    >
      {{ t('common.loading') }}
    </PanePlaceholder>

    <MonacoEditor
      v-else-if="isText && oversizedMaxBytes === null && !loadFailed"
      v-model="content"
      :filename="name"
      readonly
      class="h-full"
    />

    <div
      v-else-if="isImage && renderUrl"
      class="flex h-full items-center justify-center overflow-auto bg-muted/30 p-4"
    >
      <img
        :src="renderUrl"
        :alt="name"
        class="max-h-full max-w-full rounded-md object-contain"
      >
    </div>

    <DocumentPreview
      v-else-if="documentKind && documentBlob"
      :key="assetIdentityKey"
      :blob="documentBlob"
      :kind="documentKind"
      :filename="name"
    >
      <template
        v-if="renderUrl"
        #action
      >
        <Button
          variant="outline"
          size="sm"
          @click="handleDownload"
        >
          <Download class="mr-1.5 size-3" />
          {{ t('bots.files.download') }}
        </Button>
      </template>
    </DocumentPreview>

    <PanePlaceholder v-else>
      <template #icon>
        <File class="size-12 opacity-30" />
      </template>
      {{ oversizedMaxBytes !== null ? t('bots.files.previewTooLarge', { size: formatFileSize(oversizedMaxBytes) }) : loadFailed ? t('bots.files.previewFailed') : t('bots.files.previewNotAvailable') }}
      <template
        v-if="renderUrl"
        #action
      >
        <Button
          variant="outline"
          size="sm"
          @click="handleDownload"
        >
          <Download class="mr-1.5 size-3" />
          {{ t('bots.files.download') }}
        </Button>
      </template>
    </PanePlaceholder>
  </DockPanelFrame>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { File, Download } from 'lucide-vue-next'
import { Button, PanePlaceholder, toast } from '@felinic/ui'
import type { DockviewApi, DockviewPanelApi } from 'dockview-vue'
import MonacoEditor from '@/components/monaco-editor/index.vue'
import DocumentPreview from '@/components/file-manager/document-preview.vue'
import DockPanelFrame from './panel-frame.vue'
import { TEXT_PREVIEW_MAX_BYTES, formatFileSize, getDocumentMimeType, getDocumentPreviewKind, getDocumentPreviewMaxBytes, isImageFile, isTextFile } from '@/components/file-manager/utils'
import { sdkApiUrl, sdkAuthQuery } from '@/lib/api-client'
import { fetchBoundedBlob, fetchBoundedText, type BoundedFetchTarget } from '@/lib/bounded-fetch'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { usePanelVisible } from './use-panel-visible'

// Renders a message attachment (a stored media asset) inside its own dock tab,
// instead of handing the source URL to the OS. The source is re-resolved from
// the content hash on every render so a reload picks up a fresh auth token; an
// attachment that has no hash yet (an optimistic, just-sent file) falls back to
// the direct source URL it was opened with.
const props = defineProps<{
  params: {
    params: { name?: string, botId?: string, contentHash?: string, src?: string }
    api: DockviewPanelApi
    containerApi: DockviewApi
  }
}>()

const { t } = useI18n()

const visible = usePanelVisible(props.params.api)

const name = computed(() => props.params.params.name ?? '')
const isText = computed(() => isTextFile(name.value))
const isImage = computed(() => isImageFile(name.value))
const documentKind = computed(() => getDocumentPreviewKind(name.value))

const renderUrl = computed(() => {
  const { botId, contentHash, src } = props.params.params
  const hash = (contentHash ?? '').trim()
  const owner = (botId ?? '').trim()
  if (hash && owner) {
    return sdkApiUrl({
      url: '/bots/{bot_id}/media/{content_hash}',
      path: { bot_id: owner, content_hash: hash },
      query: sdkAuthQuery(),
    })
  }
  return (src ?? '').trim()
})

const assetSource = computed<{ key: string, target: BoundedFetchTarget } | null>(() => {
  const hash = (props.params.params.contentHash ?? '').trim()
  const owner = (props.params.params.botId ?? '').trim()
  if (hash && owner) return { key: `media:${owner}:${hash}:${name.value}`, target: { url: '/bots/{bot_id}/media/{content_hash}', path: { bot_id: owner, content_hash: hash } } }
  const directSrc = (props.params.params.src ?? '').trim()
  return directSrc ? { key: `src:${directSrc}:${name.value}`, target: { src: directSrc } } : null
})
const assetIdentityKey = computed(() => assetSource.value?.key ?? '')

const content = ref('')
const documentBlob = ref<Blob | null>(null)
const oversizedMaxBytes = ref<number | null>(null)
const loadFailed = ref(false)
const loading = ref(false)
let loadedIdentityKey = ''
let activeLoadController: AbortController | null = null

async function loadAsset() {
  const src = assetSource.value
  const docKind = documentKind.value
  if (!src || (!isText.value && !docKind) || loadedIdentityKey === src.key) return
  activeLoadController?.abort()
  const controller = new AbortController()
  activeLoadController = controller
  loading.value = true
  loadFailed.value = false
  oversizedMaxBytes.value = null
  try {
    const res = isText.value
      ? await fetchBoundedText({ target: src.target, maxBytes: TEXT_PREVIEW_MAX_BYTES, signal: controller.signal })
      : await fetchBoundedBlob({ target: src.target, maxBytes: getDocumentPreviewMaxBytes(docKind!), mimeType: getDocumentMimeType(docKind!), signal: controller.signal })
    if (controller.signal.aborted) return
    content.value = res.ok && 'text' in res ? res.text : ''
    documentBlob.value = res.ok && 'blob' in res ? res.blob : null
    oversizedMaxBytes.value = res.ok ? null : res.maxBytes
    loadedIdentityKey = src.key
  } catch (error) {
    if (controller.signal.aborted) return
    loadedIdentityKey = ''
    content.value = ''
    documentBlob.value = null
    loadFailed.value = true
    toast.error(resolveApiErrorMessage(error, t('bots.files.readFailed')))
  } finally {
    if (activeLoadController === controller) { activeLoadController = null; loading.value = false }
  }
}

function handleDownload() {
  const url = renderUrl.value
  if (!url) return
  const a = document.createElement('a')
  a.href = url
  a.download = name.value || 'file'
  a.click()
}

watch(assetIdentityKey, () => {
  activeLoadController?.abort()
  activeLoadController = null
  loadedIdentityKey = ''
  content.value = ''
  documentBlob.value = null
  oversizedMaxBytes.value = null
  loadFailed.value = false
  loading.value = false
  if (visible.value) void loadAsset()
}, { immediate: true })
watch(visible, (isVisible) => { if (isVisible) void loadAsset() })
onBeforeUnmount(() => { activeLoadController?.abort(); documentBlob.value = null })
</script>
