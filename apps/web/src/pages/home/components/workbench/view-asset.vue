<template>
  <div class="relative flex h-full w-full flex-col bg-surface-editor">
    <div class="min-h-0 flex-1">
      <!-- A restored background tab loads nothing until it is first shown. -->
      <PanePlaceholder
        v-if="loading || !hasActivated"
        loading
      >
        {{ t('common.loading') }}
      </PanePlaceholder>

      <MonacoEditor
        v-else-if="isText && oversizedMaxBytes === null && !loadFailed"
        v-model="content"
        :filename="name"
        :active="visible"
        readonly
        class="h-full"
      />

      <div
        v-else-if="isImage && renderUrl"
        class="flex h-full items-center justify-center overflow-auto bg-muted p-4"
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
        :active="visible"
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
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Download, File } from 'lucide-vue-next'
import { Button, PanePlaceholder, toast } from '@felinic/ui'
import DocumentPreview from '@/components/file-manager/document-preview.vue'
import { lazyPanelContent } from './lazy-panel-content'
import {
  TEXT_PREVIEW_MAX_BYTES,
  formatFileSize,
  getDocumentMimeType,
  getDocumentPreviewKind,
  getDocumentPreviewMaxBytes,
  isImageFile,
  isTextFile,
} from '@/components/file-manager/utils'
import { sdkApiUrl, sdkAuthQuery } from '@/lib/api-client'
import { fetchBoundedBlob, fetchBoundedText, type BoundedFetchTarget } from '@/lib/bounded-fetch'
import { resolveApiErrorMessage } from '@/utils/api-error'

const MonacoEditor = lazyPanelContent(() => import('@/components/monaco-editor/index.vue').then(m => m.default))

const props = defineProps<{
  name: string
  botId?: string
  contentHash?: string
  src?: string
  visible: boolean
}>()

const { t } = useI18n()

const hasActivated = ref(props.visible)

const isText = computed(() => isTextFile(props.name))
const isImage = computed(() => isImageFile(props.name))
const documentKind = computed(() => getDocumentPreviewKind(props.name))

const renderUrl = computed(() => {
  const hash = (props.contentHash ?? '').trim()
  const owner = (props.botId ?? '').trim()
  if (hash && owner) {
    return sdkApiUrl({
      url: '/bots/{bot_id}/media/{content_hash}',
      path: { bot_id: owner, content_hash: hash },
      query: sdkAuthQuery(),
    })
  }
  return (props.src ?? '').trim()
})

const assetSource = computed<{ key: string, target: BoundedFetchTarget } | null>(() => {
  const hash = (props.contentHash ?? '').trim()
  const owner = (props.botId ?? '').trim()
  if (hash && owner) {
    return {
      key: `media:${owner}:${hash}:${props.name}`,
      target: { url: '/bots/{bot_id}/media/{content_hash}', path: { bot_id: owner, content_hash: hash } },
    }
  }
  const directSrc = (props.src ?? '').trim()
  return directSrc ? { key: `src:${directSrc}:${props.name}`, target: { src: directSrc } } : null
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
  const source = assetSource.value
  const docKind = documentKind.value
  if (!source || (!isText.value && !docKind) || loadedIdentityKey === source.key) return
  activeLoadController?.abort()
  const controller = new AbortController()
  activeLoadController = controller
  loading.value = true
  loadFailed.value = false
  oversizedMaxBytes.value = null
  try {
    const res = isText.value
      ? await fetchBoundedText({ target: source.target, maxBytes: TEXT_PREVIEW_MAX_BYTES, signal: controller.signal })
      : await fetchBoundedBlob({
          target: source.target,
          maxBytes: getDocumentPreviewMaxBytes(docKind!),
          mimeType: getDocumentMimeType(docKind!),
          signal: controller.signal,
        })
    if (controller.signal.aborted) return
    content.value = res.ok && 'text' in res ? res.text : ''
    documentBlob.value = res.ok && 'blob' in res ? res.blob : null
    oversizedMaxBytes.value = res.ok ? null : res.maxBytes
    loadedIdentityKey = source.key
  } catch (error) {
    if (controller.signal.aborted) return
    loadedIdentityKey = ''
    content.value = ''
    documentBlob.value = null
    loadFailed.value = true
    toast.error(resolveApiErrorMessage(error, t('bots.files.readFailed')))
  } finally {
    if (activeLoadController === controller) {
      activeLoadController = null
      loading.value = false
    }
  }
}

function handleDownload() {
  const url = renderUrl.value
  if (!url) return
  const a = document.createElement('a')
  a.href = url
  a.download = props.name || 'file'
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
  if (props.visible) void loadAsset()
}, { immediate: true })

watch(() => props.visible, (isVisible) => {
  if (!isVisible) return
  hasActivated.value = true
  void loadAsset()
})

onBeforeUnmount(() => {
  activeLoadController?.abort()
  documentBlob.value = null
})
</script>
