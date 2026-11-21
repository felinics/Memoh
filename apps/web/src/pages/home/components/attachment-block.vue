<template>
  <div class="flex flex-wrap gap-2">
    <template
      v-for="(att, i) in block.attachments"
      :key="i"
    >
      <!-- Content image/video (assistant body): an image the bot posts into the
           reply is content, not an attachment chip, so it keeps its natural
           aspect ratio (a screenshot stays a rectangle) — bounded by the column
           width and a max height, never cropped square. -->
      <button
        v-if="variant === 'content' && (isImage(att) || isVideo(att)) && getUrl(att)"
        type="button"
        class="block w-fit max-w-[min(28rem,100%)] cursor-pointer overflow-hidden rounded-lg border border-border focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
        @click="handleMediaClick(att)"
      >
        <video
          v-if="isVideo(att)"
          :src="getUrl(att)"
          class="block h-auto max-h-80 w-auto max-w-full object-contain"
          preload="metadata"
          muted
          playsinline
        />
        <img
          v-else
          :src="getUrl(att)"
          :alt="String(att.name ?? '')"
          class="block h-auto max-h-80 w-auto max-w-full object-contain"
          loading="eager"
          decoding="async"
        >
      </button>

      <!-- Image / video thumbnail (uploaded attachment chip) -->
      <button
        v-else-if="(isImage(att) || isVideo(att)) && getUrl(att)"
        type="button"
        class="cursor-pointer rounded-lg focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
        @click="handleMediaClick(att)"
      >
        <ChatAttachmentCard
          kind="media"
          :src="getUrl(att)"
          :video="isVideo(att)"
          :name="String(att.name ?? '')"
          interactive
        />
      </button>

      <!-- Audio player -->
      <div
        v-else-if="isAudio(att) && getUrl(att)"
        class="rounded-lg border bg-muted/30 px-3 py-2 min-w-[280px] max-w-[400px]"
      >
        <audio
          controls
          preload="metadata"
          class="w-full"
          :src="getUrl(att)"
        />
      </div>

      <!-- Container file attachment — open in file manager -->
      <button
        v-else-if="getContainerPath(att)"
        type="button"
        class="cursor-pointer rounded-lg focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
        :title="getContainerPath(att)"
        @click="handleOpenContainerFile(att)"
      >
        <ChatAttachmentCard
          kind="file"
          :name="getDisplayName(att)"
          :ext="getExt(att)"
          interactive
        />
      </button>

      <!-- Uploaded file — open in an in-app preview tab -->
      <button
        v-else-if="getUrl(att)"
        type="button"
        class="cursor-pointer rounded-lg focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
        :title="getDisplayName(att)"
        @click="handleOpenFile(att)"
      >
        <ChatAttachmentCard
          kind="file"
          :name="getDisplayName(att)"
          :ext="getExt(att)"
          :lines="linesFor(att)"
          :loading="isCounting(att)"
          interactive
        />
      </button>

      <!-- Non-accessible attachment -->
      <ChatAttachmentCard
        v-else
        kind="file"
        :name="getDisplayName(att)"
        :ext="getExt(att)"
      />
    </template>
  </div>
</template>

<script lang="ts">
import { onAuthSessionCleared } from '@/lib/auth-session'

// Line counts for sent text attachments, shared at module scope so a reconcile
// (optimistic → persisted) or a virtual-scroll re-mount doesn't refetch. Keyed
// by content hash when known (stable), else the source URL. Oversized text
// attachments record 'skipped' so re-mounts never re-attempt downloading them.
const MAX_LINE_COUNT_TEXT_BYTES = 256 * 1024
const lineCountCache = new Map<string, number | 'skipped'>()

onAuthSessionCleared(() => {
  lineCountCache.clear()
})
</script>

<script setup lang="ts">
import { inject, reactive, watch } from 'vue'
import type { AttachmentBlock, AttachmentItem } from '@/store/chat-list'
import { useChatStore } from '@/store/chat-list'
import { isTextFile } from '@/components/file-manager/utils'
import { resolveUrl } from '../composables/useMediaGallery'
import { openInFileManagerKey, openAssetPreviewKey } from '../composables/useFileManagerProvider'
import ChatAttachmentCard from './chat-attachment-card.vue'

const props = withDefaults(defineProps<{
  block: AttachmentBlock
  onOpenMedia?: (src: string) => void
  // 'attachment' (default) = uploaded-file chips (square media cards); 'content'
  // = images the assistant posts in its reply body, rendered inline at their
  // natural aspect ratio.
  variant?: 'attachment' | 'content'
}>(), {
  variant: 'attachment',
})

const chatStore = useChatStore()
const openInFileManager = inject(openInFileManagerKey, undefined)
const openAssetPreview = inject(openAssetPreviewKey, undefined)

// A sent text file should read exactly like its composer preview did — same line
// count, same "load then appear" shimmer. The composer had the File object to
// read; here the file lives in the media store, so we fetch the asset once and
// count its lines the same way (split on '\n'). 'counting' shimmers the card
// until the fetch resolves; a number reveals it with the count.
const lineCounts = reactive<Record<string, number | 'counting'>>({})

function attachmentKey(att: AttachmentItem): string {
  const hash = String(att.content_hash ?? '').trim()
  return hash || getUrl(att)
}

function isTextAttachment(att: AttachmentItem): boolean {
  if (isImage(att) || isVideo(att) || isAudio(att)) return false
  return isTextFile(getDisplayName(att))
}

function knownAttachmentBytes(att: AttachmentItem): number | null {
  if (typeof att.size === 'number' && Number.isFinite(att.size) && att.size >= 0) {
    return att.size
  }
  const metaSize = (att.metadata as Record<string, unknown> | undefined)?.size
  if (typeof metaSize === 'number' && Number.isFinite(metaSize) && metaSize >= 0) {
    return metaSize
  }
  if (typeof att.base64 === 'string' && att.base64.length > 0) {
    return Math.floor(att.base64.length * 0.75)
  }
  return null
}

async function readBoundedTextLines(res: Response): Promise<number | 'skipped'> {
  const headerLength = Number(res.headers.get('content-length'))
  if (Number.isFinite(headerLength) && headerLength > MAX_LINE_COUNT_TEXT_BYTES) {
    void res.body?.cancel().catch(() => {})
    return 'skipped'
  }
  const reader = res.body?.getReader()
  if (!reader) {
    const text = await res.text()
    if (text.length > MAX_LINE_COUNT_TEXT_BYTES) return 'skipped'
    return text.split('\n').length
  }
  let totalBytes = 0
  let newlineCount = 0
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      if (!value) continue
      totalBytes += value.byteLength
      if (totalBytes > MAX_LINE_COUNT_TEXT_BYTES) {
        await reader.cancel().catch(() => {})
        return 'skipped'
      }
      for (let i = 0; i < value.length; i++) {
        if (value[i] === 0x0a) newlineCount++
      }
    }
    return newlineCount + 1
  } finally {
    reader.releaseLock()
  }
}

async function ensureLineCount(att: AttachmentItem) {
  if (!isTextAttachment(att)) return
  const knownBytes = knownAttachmentBytes(att)
  if (knownBytes !== null && knownBytes > MAX_LINE_COUNT_TEXT_BYTES) return
  const url = getUrl(att)
  if (!url) return
  const key = attachmentKey(att)
  if (!key || key in lineCounts) return
  const cached = lineCountCache.get(key)
  if (cached === 'skipped') return
  if (cached != null) {
    lineCounts[key] = cached
    return
  }
  lineCounts[key] = 'counting'
  try {
    const res = await fetch(url)
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const count = await readBoundedTextLines(res)
    lineCountCache.set(key, count)
    if (count === 'skipped') {
      delete lineCounts[key]
      return
    }
    lineCounts[key] = count
  } catch {
    // Counting failed — drop the shimmer and show the file without a count.
    delete lineCounts[key]
  }
}

watch(() => props.block.attachments, (atts) => {
  for (const att of atts) void ensureLineCount(att)
}, { immediate: true })

function linesFor(att: AttachmentItem): number | null {
  const v = lineCounts[attachmentKey(att)]
  return typeof v === 'number' ? v : null
}

function isCounting(att: AttachmentItem): boolean {
  return lineCounts[attachmentKey(att)] === 'counting'
}

function getUrl(att: AttachmentItem): string {
  return resolveUrl(att)
}

function isImage(att: AttachmentItem): boolean {
  const type = String(att.type ?? '').toLowerCase()
  if (type === 'image' || type === 'gif') return true
  const mime = String(att.mime ?? '').toLowerCase()
  return mime.startsWith('image/')
}

function isVideo(att: AttachmentItem): boolean {
  const type = String(att.type ?? '').toLowerCase()
  if (type === 'video') return true
  const mime = String(att.mime ?? '').toLowerCase()
  return mime.startsWith('video/')
}

function isAudio(att: AttachmentItem): boolean {
  const type = String(att.type ?? '').toLowerCase()
  if (type === 'audio' || type === 'voice') return true
  const mime = String(att.mime ?? '').toLowerCase()
  return mime.startsWith('audio/')
}

function getContainerPath(att: AttachmentItem): string {
  const direct = String(att.path ?? '').trim()
  if (direct) return direct
  const meta = att.metadata as Record<string, unknown> | undefined
  return String(meta?.source_path ?? '').trim()
}

function getDisplayName(att: AttachmentItem): string {
  if (att.name) return String(att.name)
  const p = getContainerPath(att)
  if (p) return p.split('/').pop() || p
  if (att.storage_key) return String(att.storage_key)
  return 'file'
}

function getExt(att: AttachmentItem): string {
  const name = getDisplayName(att)
  const dot = name.lastIndexOf('.')
  return dot > 0 ? name.slice(dot + 1).toUpperCase() : ''
}

function handleMediaClick(att: AttachmentItem) {
  const src = getUrl(att)
  if (src && props.onOpenMedia) {
    props.onOpenMedia(src)
  }
}

function handleOpenContainerFile(att: AttachmentItem) {
  const path = getContainerPath(att)
  if (path && openInFileManager) {
    openInFileManager(path, false)
  }
}

function resolveAttBotId(att: AttachmentItem): string {
  const direct = String(att.bot_id ?? '').trim()
  if (direct) return direct
  const meta = att.metadata as Record<string, unknown> | undefined
  const fromMeta = String(meta?.bot_id ?? '').trim()
  if (fromMeta) return fromMeta
  return (chatStore.currentBotId ?? '').trim()
}

// Stable fallback key for an attachment that has no content hash yet (an
// optimistic, just-sent file): derive one from the name + source so reopening
// the same file refocuses its tab instead of stacking duplicates.
function stableKey(input: string): string {
  let hash = 5381
  for (let i = 0; i < input.length; i++) {
    hash = ((hash << 5) + hash + input.charCodeAt(i)) >>> 0
  }
  return hash.toString(36)
}

function handleOpenFile(att: AttachmentItem) {
  const url = getUrl(att)
  if (!url) return
  const name = getDisplayName(att)
  const contentHash = String(att.content_hash ?? '').trim()
  if (openAssetPreview) {
    openAssetPreview({
      key: contentHash || stableKey(`${name}:${url}`),
      name,
      botId: contentHash ? resolveAttBotId(att) : undefined,
      contentHash: contentHash || undefined,
      src: contentHash ? undefined : url,
    })
    return
  }
  // Rendered outside a dock (no preview host): download rather than hand a
  // data:/blob URL to the OS, which would prompt to pick an external app.
  const a = document.createElement('a')
  a.href = url
  a.download = name || 'file'
  a.click()
}
</script>
