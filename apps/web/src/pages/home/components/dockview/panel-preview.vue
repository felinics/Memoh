<template>
  <DockPanelFrame editor-surface>
    <template #header>
      <PanelBreadcrumb :path="filePath" />
    </template>
    <!-- Full-area spinner only on the first load. Reloads keep the rendered
         markdown/html mounted and swap content in place. -->
    <PanePlaceholder
      v-if="loading && !loaded"
      loading
    >
      {{ t('common.loading') }}
    </PanePlaceholder>

    <MarkdownPreview
      v-else-if="isMd"
      :content="content"
      class="h-full"
    />
    <HtmlPreview
      v-else-if="isHtml"
      :content="content"
      class="h-full"
    />

    <PanePlaceholder v-else>
      <template #icon>
        <FileText class="size-10 opacity-30" />
      </template>
      {{ t('bots.files.previewNotAvailable') }}
    </PanePlaceholder>
  </DockPanelFrame>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { FileText } from 'lucide-vue-next'
import { PanePlaceholder, toast } from '@felinic/ui'
import type { DockviewApi, DockviewPanelApi } from 'dockview-vue'
import { getBotsByBotIdContainerFsRead } from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { isMarkdownFile, isHtmlFile } from '@/components/file-manager/utils'
import { useChatStore } from '@/store/chat-list'
import { usePanelVisible } from './use-panel-visible'
import PanelBreadcrumb from './panel-breadcrumb.vue'
import DockPanelFrame from './panel-frame.vue'

const MarkdownPreview = defineAsyncComponent(() => import('@/components/markdown-preview/index.vue'))
const HtmlPreview = defineAsyncComponent(() => import('@/components/html-preview/index.vue'))

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
const PREVIEW_POLL_MS = 2_000

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

const content = ref('')
const loading = ref(false)
// True once the preview has been rendered at least once for the current path,
// so subsequent reloads can update content in place without flashing a spinner.
const loaded = ref(false)
// One in-flight load at a time; a new load aborts the old one so stale
// fast-fire responses can't clobber newer content.
let activeLoadController: AbortController | null = null
let previewPollTimer: ReturnType<typeof setInterval> | null = null

function isDocumentVisible(): boolean {
  return typeof document === 'undefined' || document.visibilityState === 'visible'
}

async function load(options: { notifyOnError?: boolean } = {}) {
  const botId = currentBotId.value
  if (!botId || !filePath.value || !canPreview.value) return
  activeLoadController?.abort()
  const controller = new AbortController()
  activeLoadController = controller
  loading.value = true
  try {
    const { data } = await getBotsByBotIdContainerFsRead({
      path: { bot_id: botId },
      query: { path: filePath.value },
      signal: controller.signal,
      throwOnError: true,
    })
    if (controller.signal.aborted) return
    content.value = data.content ?? ''
    loaded.value = true
  } catch (error) {
    if (controller.signal.aborted) return
    if (options.notifyOnError !== false) {
      toast.error(resolveApiErrorMessage(error, t('bots.files.readFailed')))
    }
  } finally {
    if (activeLoadController === controller) {
      activeLoadController = null
      loading.value = false
    }
  }
}

function pollPreview() {
  if (!visible.value || !loaded.value || loading.value || !isDocumentVisible()) return
  void load({ notifyOnError: false })
}

function handleVisibilityChange() {
  if (visible.value && isDocumentVisible()) void load({ notifyOnError: false })
}

// Load when the panel first becomes visible / its target changes, and refresh
// when the agent mutates the workspace. Path change starts the "loaded" clock
// from scratch so the user sees the spinner only for the new target's first
// fetch.
watch(filePath, () => {
  loaded.value = false
  content.value = ''
})
watch([visible, filePath], ([isVisible]) => {
  if (isVisible) void load()
}, { immediate: true })

watch(fsChangedAt, () => {
  if (!visible.value) return
  if (!chatStore.affectsPath(filePath.value)) return
  void load()
})

onMounted(() => {
  document.addEventListener('visibilitychange', handleVisibilityChange)
  previewPollTimer = window.setInterval(pollPreview, PREVIEW_POLL_MS)
})

onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', handleVisibilityChange)
  if (previewPollTimer !== null) {
    window.clearInterval(previewPollTimer)
    previewPollTimer = null
  }
  activeLoadController?.abort()
})
</script>
