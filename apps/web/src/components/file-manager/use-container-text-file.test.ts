// @vitest-environment jsdom
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import { createPinia } from 'pinia'
import { PiniaColada, useQueryCache } from '@pinia/colada'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  createContainerFileStatTracker,
  syncContainerTextFileCache,
  useContainerTextFileQuery,
} from './use-container-text-file'

const sdk = vi.hoisted(() => ({
  stat: vi.fn(),
  read: vi.fn(),
}))

vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdContainerFs: sdk.stat,
  getBotsByBotIdContainerFsRead: sdk.read,
}))

describe('createContainerFileStatTracker', () => {
  it('skips FSRead on the very first poll when disk modTime settled before initial sync', () => {
    const tracker = createContainerFileStatTracker()
    tracker.recordSync(120, 10_000)

    const decision = tracker.shouldRead({
      isDir: false,
      size: 120,
      mode: '-rw-r--r--',
      modTime: '1970-01-01T00:00:05Z', // 5_000ms < 10_000 - 1_500
    })
    expect(decision).toBe(false)
  })

  it('catches same-second same-byte-length consecutive edits despite 1s RFC3339 modTime', () => {
    const tracker = createContainerFileStatTracker()
    tracker.recordSync(4, 10_100)

    // External write #1 ("AAAA", 4 bytes) at 10.2s -> modTime is 10.0s (10_000ms).
    const sameSecondStat = {
      isDir: false,
      size: 4,
      mode: '-rw-r--r--',
      modTime: '1970-01-01T00:00:10Z',
    }

    // Poll #1 at 10.3s: 10_000 is within the 1.5s unsettled window of 10_100 -> must read.
    expect(tracker.shouldRead(sameSecondStat)).toBe(true)
    // FSRead #1 finishes at 10_350ms; modTime (10_000) is STILL within 1.5s of 10_350 -> stays unlocked!
    tracker.commitRead(sameSecondStat, 4, 10_350)

    // External write #2 ("BBBB", 4 bytes) at 10.8s -> still produces exact same stat!
    // Poll #2 at 12.3s: because fingerprint was kept unlocked, we read again and catch "BBBB".
    expect(tracker.shouldRead(sameSecondStat)).toBe(true)
    // FSRead #2 finishes at 12_350ms; now 10_000 < 12_350 - 1_500 -> locks fingerprint!
    tracker.commitRead(sameSecondStat, 4, 12_350)

    // Poll #3 at 14.3s: zero further FSRead calls.
    expect(tracker.shouldRead(sameSecondStat)).toBe(false)
  })

  it('forces FSRead when directory replaces file or read previously failed to commit', () => {
    const tracker = createContainerFileStatTracker()
    tracker.recordSync(10, 20_000)
    expect(tracker.shouldRead({ isDir: true, size: 10, mode: 'drwxr-xr-x', modTime: '1970-01-01T00:00:01Z' })).toBe(true)

    const updatedStat = { isDir: false, size: 25, mode: '-rw-r--r--', modTime: '1970-01-01T00:00:15Z' }
    expect(tracker.shouldRead(updatedStat)).toBe(true)
    // Simulate failed FSRead (commitRead not called): next poll must still request FSRead.
    expect(tracker.shouldRead(updatedStat)).toBe(true)
  })
})

describe('useContainerTextFileQuery', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('deduplicates concurrent mounts via Pinia Colada and updates instantly on syncContainerTextFileCache', async () => {
    sdk.read.mockResolvedValue({
      data: { path: '/data/doc.md', content: '# v1', revision: 'rev-1', size: 4 },
    })
    sdk.stat.mockResolvedValue({
      data: { path: '/data/doc.md', name: 'doc.md', isDir: false, size: 4, mode: '-rw-r--r--', modTime: '1970-01-01T00:00:01Z' },
    })

    const visible = ref(true)
    let cache!: ReturnType<typeof useQueryCache>
    const Consumer = defineComponent({
      setup() {
        cache = useQueryCache()
        const query = useContainerTextFileQuery('bot-1', '/data/doc.md', { visible })
        return () => h('p', query.data.value?.content ?? '')
      },
    })

    const host = document.createElement('div')
    const app = createApp({
      setup: () => () => h('div', [h(Consumer), h(Consumer)]),
    })
    app.use(createPinia()).use(PiniaColada)
    app.mount(host)

    await vi.waitFor(() => {
      expect(host.querySelectorAll('p')[0]?.textContent).toBe('# v1')
      expect(host.querySelectorAll('p')[1]?.textContent).toBe('# v1')
    })
    expect(sdk.read).toHaveBeenCalledTimes(1)

    // Syncing a save from FileViewer updates both consumers synchronously with zero extra HTTP reads.
    syncContainerTextFileCache(cache, 'bot-1', '/data/doc.md', {
      content: '# v2 saved',
      revision: 'rev-2',
      size: 10,
    })
    await nextTick()
    expect(host.querySelectorAll('p')[0]?.textContent).toBe('# v2 saved')
    expect(host.querySelectorAll('p')[1]?.textContent).toBe('# v2 saved')
    expect(sdk.read).toHaveBeenCalledTimes(1)

    app.unmount()
  })
})
