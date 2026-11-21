// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import type { AttachmentBlock as AttachmentBlockModel, AttachmentItem } from '@/store/chat-list'

/**
 * Failure Modes covered by this behavioral suite:
 * - FM-AB1: Text files within the 256KB budget fail to count `\n` accurately across streamed chunks.
 * - FM-AB2: Oversized text attachments (`att.size > 256KB` or `metadata.size > 256KB`) still trigger `fetch()`.
 * - FM-AB3: Oversized HTTP response (`Content-Length > 256KB`) reads full body instead of cancelling `res.body`
 *           immediately and caching `'skipped'` across re-mounts.
 * - FM-AB4: Chunked response without `Content-Length` whose streamed bytes exceed `256KB` fails to call
 *           `reader.cancel()` and hangs waiting on remaining chunks, or leaves `loading` shimmer stuck.
 */

vi.mock('@/store/chat-list', () => ({
  useChatStore: () => ({ currentBotId: 'bot-1' }),
}))

vi.mock('../composables/useMediaGallery', () => ({
  resolveUrl: (att: AttachmentItem) => String(att.url ?? ''),
}))

vi.mock('./chat-attachment-card.vue', () => ({
  default: (props: { name?: string; lines?: number | null; loading?: boolean }) => h('div', {
    'data-testid': 'attachment-card',
    'data-name': props.name ?? '',
    'data-lines': props.lines == null ? '' : String(props.lines),
    'data-loading': String(Boolean(props.loading)),
  }),
}))

const AttachmentBlock = (await import('./attachment-block.vue')).default

async function flushAsync(turns = 6) {
  for (let i = 0; i < turns; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

describe('attachment-block bounded text line counting', () => {
  const fetchMock = vi.fn<typeof fetch>()
  let container: HTMLElement
  let mountedApps: Array<ReturnType<typeof createApp>>

  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
    container = document.createElement('div')
    document.body.append(container)
    mountedApps = []
  })

  afterEach(() => {
    for (const app of mountedApps) app.unmount()
    container.remove()
    vi.unstubAllGlobals()
  })

  function mountBlock(attachments: AttachmentItem[]) {
    const host = document.createElement('div')
    container.append(host)
    const block: AttachmentBlockModel = {
      id: 1,
      type: 'attachments',
      attachments,
    }
    const app = createApp(AttachmentBlock, { block })
    app.mount(host)
    mountedApps.push(app)
    return host
  }

  it('counts lines accurately across streamed chunks when within the 256KB limit (FM-AB1)', async () => {
    const encoder = new TextEncoder()
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(encoder.encode('first line\nsec'))
        controller.enqueue(encoder.encode('ond line\nthird line'))
        controller.close()
      },
    })
    fetchMock.mockResolvedValueOnce(new Response(stream, {
      status: 200,
      headers: { 'content-length': '31' },
    }))

    const host = mountBlock([{
      type: 'file',
      name: 'notes.md',
      url: 'https://example.test/assets/notes.md',
      content_hash: 'hash-small-md',
    }])
    await flushAsync()

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const card = host.querySelector('[data-testid="attachment-card"]')!
    expect(card.getAttribute('data-lines')).toBe('3')
    expect(card.getAttribute('data-loading')).toBe('false')
  })

  it('skips fetch entirely when att.size or metadata.size exceeds 256KB (FM-AB2)', async () => {
    const host = mountBlock([
      {
        type: 'file',
        name: 'huge-dump.log',
        url: 'https://example.test/assets/huge-dump.log',
        content_hash: 'hash-known-size',
        size: 256 * 1024 + 1,
      },
      {
        type: 'file',
        name: 'huge-meta.json',
        url: 'https://example.test/assets/huge-meta.json',
        content_hash: 'hash-meta-size',
        metadata: { size: 512 * 1024 },
      },
    ])
    await flushAsync()

    expect(fetchMock).not.toHaveBeenCalled()
    const cards = [...host.querySelectorAll('[data-testid="attachment-card"]')]
    expect(cards).toHaveLength(2)
    for (const card of cards) {
      expect(card.getAttribute('data-lines')).toBe('')
      expect(card.getAttribute('data-loading')).toBe('false')
    }
  })

  it('cancels response body immediately when Content-Length exceeds 256KB and caches skipped across remounts (FM-AB3)', async () => {
    const cancelSpy = vi.fn(async () => {})
    const pullSpy = vi.fn()
    const stream = new ReadableStream<Uint8Array>(
      {
        pull: pullSpy,
        cancel: cancelSpy,
      },
      { highWaterMark: 0 },
    )
    fetchMock.mockResolvedValueOnce(new Response(stream, {
      status: 200,
      headers: { 'content-length': String(1024 * 1024) },
    }))

    const att: AttachmentItem = {
      type: 'file',
      name: 'server-trace.txt',
      url: 'https://example.test/assets/server-trace.txt',
      content_hash: 'hash-oversized-header',
    }
    const host1 = mountBlock([att])
    await flushAsync()

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(cancelSpy).toHaveBeenCalledTimes(1)
    expect(pullSpy).not.toHaveBeenCalled()
    const card1 = host1.querySelector('[data-testid="attachment-card"]')!
    expect(card1.getAttribute('data-lines')).toBe('')
    expect(card1.getAttribute('data-loading')).toBe('false')

    // Virtual-scroll remount with the same content_hash must hit `'skipped'` cache without refetching.
    mountBlock([att])
    await flushAsync()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('aborts streaming reader immediately once cumulative chunks exceed 256KB without hanging on unread tail (FM-AB4)', async () => {
    const cancelSpy = vi.fn(async () => {})
    let pullIndex = 0
    const chunk160KB = new Uint8Array(160 * 1024).fill(0x61)
    chunk160KB[10] = 0x0a

    // If the reader failed to abort after chunk 2 (320KB > 256KB), pull #3 would hang forever.
    const stream = new ReadableStream<Uint8Array>(
      {
        pull(controller) {
          pullIndex += 1
          if (pullIndex <= 2) {
            controller.enqueue(chunk160KB)
            return
          }
          return new Promise<void>(() => {})
        },
        cancel: cancelSpy,
      },
      { highWaterMark: 0 },
    )
    fetchMock.mockResolvedValueOnce(new Response(stream, { status: 200 }))

    const att: AttachmentItem = {
      type: 'file',
      name: 'chunked-giant.csv',
      url: 'https://example.test/assets/chunked-giant.csv',
      content_hash: 'hash-oversized-stream',
    }
    const host = mountBlock([att])
    await flushAsync(10)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(pullIndex).toBe(2)
    expect(cancelSpy).toHaveBeenCalledTimes(1)
    const card = host.querySelector('[data-testid="attachment-card"]')!
    expect(card.getAttribute('data-lines')).toBe('')
    expect(card.getAttribute('data-loading')).toBe('false')

    // Remounting also respects the cached `'skipped'` marker.
    mountBlock([att])
    await flushAsync()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
