<template>
  <DockPanelFrame editor-surface>
    <PanePlaceholder
      v-if="loading"
      loading
    >
      {{ t('common.loading') }}
    </PanePlaceholder>

    <MonacoEditor
      v-else-if="isText"
      v-model="content"
      :filename="name"
      :active="visible"
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

    <PanePlaceholder v-else>
      <template #icon>
        <File class="size-12 opacity-30" />
      </template>
      {{ t('bots.files.previewNotAvailable') }}
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
import { computed, defineAsyncComponent, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { File, Download } from 'lucide-vue-next'
import { Button, PanePlaceholder, toast } from '@felinic/ui'
import type { DockviewApi, DockviewPanelApi } from 'dockview-vue'
import { withStylesheetRecovery } from '@/lib/chunk-load-error'
import DockPanelFrame from './panel-frame.vue'
import { isTextFile, isImageFile } from '@/components/file-manager/utils'
import { sdkApiUrl, sdkAuthQuery } from '@/lib/api-client'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { usePanelVisible } from './use-panel-visible'

const MonacoEditor = defineAsyncComponent(withStylesheetRecovery(() => import('@/components/monaco-editor/index.vue').then(m => m.default)))

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

const content = ref('')
const loading = ref(false)
let loadedUrl = ''
let loadController: AbortController | null = null

async function loadText() {
  const url = renderUrl.value
  if (!isText.value || !url || loadedUrl === url) return
  loadController?.abort()
  const controller = new AbortController()
  loadController = controller
  loading.value = true
  try {
    const res = await fetch(url, { signal: controller.signal })
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const text = await res.text()
    if (controller.signal.aborted) return
    content.value = text
    loadedUrl = url
  } catch (error) {
    if (controller.signal.aborted) return
    toast.error(resolveApiErrorMessage(error, t('bots.files.readFailed')))
  } finally {
    if (loadController === controller) {
      loadController = null
      loading.value = false
    }
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

// Text is fetched lazily the first time the tab is shown (an image renders
// straight from the URL, so it needs no fetch). Content is immutable per asset,
// so it is fetched once and kept.
watch([visible, renderUrl], ([isVisible]) => {
  if (isVisible) void loadText()
}, { immediate: true })

onBeforeUnmount(() => {
  loadController?.abort()
  loadController = null
})
</script>
