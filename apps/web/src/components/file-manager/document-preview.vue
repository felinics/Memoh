<script setup lang="ts">
import { onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { File } from 'lucide-vue-next'
import { PanePlaceholder } from '@felinic/ui'
import { DOCX_PREVIEW_MAX_BYTES, formatFileSize, type DocumentPreviewKind } from './utils'
import { DOCX_EXTERNAL_LINK_MESSAGE_TYPE, normalizeSafeExternalHref, renderDocxBlobToSrcdoc } from './docx-sandbox'

const props = defineProps<{ blob: Blob, kind: DocumentPreviewKind, filename: string }>()
const { t } = useI18n()
const channelToken = typeof crypto?.randomUUID === 'function' ? crypto.randomUUID() : `${Math.random().toString(36).slice(2)}-${Date.now().toString(36)}`

const pdfUrl = ref('')
const docxSrcdoc = ref('')
const docxLoading = ref(false)
const docxError = ref<'too-large' | 'invalid' | null>(null)
const docxIframeRef = ref<HTMLIFrameElement | null>(null)
let renderGeneration = 0
let isDeactivated = false
let pendingRenderWhileDeactivated = false

function revokePdfUrl() {
  if (!pdfUrl.value) return
  URL.revokeObjectURL(pdfUrl.value)
  pdfUrl.value = ''
}

function handleFrameMessage(event: MessageEvent) {
  const frameWindow = docxIframeRef.value?.contentWindow
  if (!frameWindow || event.source !== frameWindow || !event.data || typeof event.data !== 'object') return
  const payload = event.data as { type?: unknown, token?: unknown, href?: unknown }
  if (payload.type !== DOCX_EXTERNAL_LINK_MESSAGE_TYPE || payload.token !== channelToken) return
  const safeHref = normalizeSafeExternalHref(payload.href)
  if (!safeHref) return
  const desktopApi = (window as unknown as { api?: { desktop?: { openExternalUrl?: (url: string) => Promise<void> } } }).api?.desktop
  if (typeof desktopApi?.openExternalUrl === 'function') void desktopApi.openExternalUrl(safeHref)
  else Object.assign(document.createElement('a'), { href: safeHref, target: '_blank', rel: 'noopener noreferrer' }).click()
}

async function syncPreview() {
  if (isDeactivated && props.kind === 'docx') { pendingRenderWhileDeactivated = true; return }
  const generation = ++renderGeneration
  if (props.kind === 'pdf') {
    docxSrcdoc.value = ''
    docxLoading.value = false
    docxError.value = null
    const nextUrl = URL.createObjectURL(props.blob.type === 'application/pdf' ? props.blob : new Blob([props.blob], { type: 'application/pdf' }))
    revokePdfUrl()
    pdfUrl.value = nextUrl
    return
  }
  revokePdfUrl()
  docxError.value = null
  if (!docxSrcdoc.value) docxLoading.value = true
  const result = await renderDocxBlobToSrcdoc(props.blob, { channelToken })
  if (generation !== renderGeneration) return
  docxLoading.value = false
  docxSrcdoc.value = result.ok ? result.srcdoc : ''
  docxError.value = result.ok ? null : result.reason
}

watch([() => props.blob, () => props.kind], () => { void syncPreview() }, { immediate: true })
onMounted(() => window.addEventListener('message', handleFrameMessage))
onActivated(() => {
  isDeactivated = false
  if (pendingRenderWhileDeactivated) { pendingRenderWhileDeactivated = false; void syncPreview() }
})
onDeactivated(() => { isDeactivated = true })
onBeforeUnmount(() => { renderGeneration++; window.removeEventListener('message', handleFrameMessage); revokePdfUrl() })
</script>

<template>
  <div class="flex h-full w-full flex-col overflow-hidden bg-surface-editor">
    <PanePlaceholder
      v-if="kind === 'docx' && docxLoading && !docxSrcdoc"
      loading
    >
      {{ t('common.loading') }}
    </PanePlaceholder>
    <PanePlaceholder v-else-if="kind === 'docx' && docxError">
      <template #icon>
        <File class="size-12 opacity-30" />
      </template>
      {{ docxError === 'too-large' ? t('bots.files.previewTooLarge', { size: formatFileSize(DOCX_PREVIEW_MAX_BYTES) }) : t('bots.files.previewFailed') }}
      <template
        v-if="$slots.action"
        #action
      >
        <slot name="action" />
      </template>
    </PanePlaceholder>
    <iframe
      v-else-if="kind === 'pdf' && pdfUrl"
      key="pdf-frame"
      :src="pdfUrl"
      :title="filename"
      class="h-full w-full border-0 bg-surface-editor"
    />
    <iframe
      v-else-if="kind === 'docx' && docxSrcdoc"
      key="docx-frame"
      ref="docxIframeRef"
      :srcdoc="docxSrcdoc"
      :title="filename"
      sandbox="allow-scripts"
      referrerpolicy="no-referrer"
      class="h-full w-full border-0 bg-transparent"
    />
  </div>
</template>
