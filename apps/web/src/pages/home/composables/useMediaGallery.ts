import { computed, ref, type Ref } from 'vue'
import { sdkApiUrl, sdkAuthQuery } from '@/lib/api-client'
import { useChatStore } from '@/store/chat-list'
import type { AttachmentItem, ChatMessage } from '@/store/chat-list'
import type { MediaGalleryItem } from '../components/media-gallery-lightbox.vue'

function isMediaType(att: AttachmentItem): boolean {
  const type = String(att.type ?? '').toLowerCase()
  if (type === 'image' || type === 'gif' || type === 'video') return true
  const mime = String(att.mime ?? '').toLowerCase()
  return mime.startsWith('image/') || mime.startsWith('video/')
}

function isBrowserAccessibleUrl(url: string): boolean {
  if (!url) return false
  const lower = url.toLowerCase()
  return lower.startsWith('http://') || lower.startsWith('https://') || lower.startsWith('data:')
}

function resolveBotId(att: AttachmentItem): string {
  let botId = String(att.bot_id ?? '').trim()
  if (botId) return botId
  const meta = att.metadata
  botId = String(meta?.bot_id ?? '').trim()
  if (botId) return botId
  // Fall back to the currently active bot in the chat store.
  try {
    const store = useChatStore()
    return (store.currentBotId ?? '').trim()
  } catch {
    return ''
  }
}

function resolveAssetApiUrl(att: AttachmentItem): string {
  const contentHash = String(att.content_hash ?? '').trim()
  if (!contentHash) return ''
  const botId = resolveBotId(att)
  if (!botId) return ''
  return sdkApiUrl({
    url: '/bots/{bot_id}/media/{content_hash}',
    path: { bot_id: botId, content_hash: contentHash },
    query: sdkAuthQuery(),
  })
}

function resolveUrl(att: AttachmentItem): string {
  // Prefer asset API when content_hash is available (reliable, auth-aware).
  const assetUrl = resolveAssetApiUrl(att)
  if (assetUrl) return assetUrl
  // Fall back to direct URL if browser-accessible (http/https/data).
  const url = String(att.url ?? '').trim()
  if (isBrowserAccessibleUrl(url)) return url
  const base64 = String(att.base64 ?? '').trim()
  if (isBrowserAccessibleUrl(base64)) return base64
  // Container-internal paths or other non-HTTP URLs cannot be loaded directly.
  // Return empty so the attachment-block shows the fallback (file name display).
  return ''
}

function normalizeSrc(src: string): string {
  if (!src || src.startsWith('data:')) return src
  try {
    const u = new URL(src, window.location.origin)
    return u.pathname
  } catch {
    return src
  }
}

export function useMediaGallery(messages: Ref<ChatMessage[]>) {
  const openIndex = ref<number | null>(null)

  const items = computed((): MediaGalleryItem[] => {
    const result: MediaGalleryItem[] = []
    for (const msg of messages.value) {
      if (msg.role === 'user') {
        for (const att of msg.attachments) {
          if (!isMediaType(att)) continue
          const src = resolveUrl(att)
          if (!src) continue
          const type = String(att.type ?? '').toLowerCase()
          result.push({
            src,
            type: type === 'video' ? 'video' : 'image',
            name: String(att.name ?? '').trim() || undefined,
          })
        }
        continue
      }

      if (msg.role !== 'assistant') continue

      for (const block of msg.messages) {
        if (block.type !== 'attachments') continue
        for (const att of block.attachments) {
          if (!isMediaType(att)) continue
          const src = resolveUrl(att)
          if (!src) continue
          const type = String(att.type ?? '').toLowerCase()
          result.push({
            src,
            type: type === 'video' ? 'video' : 'image',
            name: String(att.name ?? '').trim() || undefined,
          })
        }
      }
    }
    return result
  })

  function findIndexBySrc(src: string): number {
    const norm = normalizeSrc(src)
    if (!norm) return -1
    return items.value.findIndex((item) => normalizeSrc(item.src) === norm)
  }

  function openBySrc(src: string) {
    const idx = findIndexBySrc(src)
    if (idx >= 0) {
      openIndex.value = idx
    }
  }

  function setOpenIndex(v: number | null) {
    openIndex.value = v
  }

  return {
    items,
    openIndex,
    setOpenIndex,
    openBySrc,
    findIndexBySrc,
  }
}

export { resolveUrl, isMediaType }
