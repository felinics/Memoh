// @vitest-environment jsdom
import { createApp, h, nextTick, ref } from 'vue'
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

const file = (size: number, modTime: string) => ({ isDir: false, size, mode: '-rw-r--r--', modTime })

describe('createContainerFileStatTracker', () => {
  it('skips the read when the file was last modified long before the content was synced', () => {
    const tracker = createContainerFileStatTracker()
    tracker.recordSync(120, 600_000)

    expect(tracker.observe(file(120, '1970-01-01T00:00:05Z'), 0)).toBe('skip')
    // Locked: later polls skip as well, however much time has passed.
    expect(tracker.observe(file(120, '1970-01-01T00:00:05Z'), 60_000)).toBe('skip')
  })

  it('does not trust the browser clock for a recently modified file', () => {
    const tracker = createContainerFileStatTracker()
    // Browser clock 30s ahead of the workspace: the file looks 30s old but was
    // written in the same second the content was read.
    tracker.recordSync(4, 40_000)
    const stat = file(4, '1970-01-01T00:00:10Z')

    expect(tracker.observe(stat, 0)).toBe('skip')
    expect(tracker.observe(stat, 2_000)).toBe('read-and-lock')
    tracker.lock(stat)
    expect(tracker.observe(stat, 4_000)).toBe('skip')
  })

  it('catches same-second same-size consecutive edits despite 1s modTime resolution', () => {
    const tracker = createContainerFileStatTracker()
    tracker.recordSync(4, 10_100)
    tracker.lock(file(4, '1970-01-01T00:00:03Z'))

    // External write #1 ("AAAA") lands in second 10; a write #2 ("BBBB") in the
    // same second leaves the stat identical.
    const sameSecond = file(4, '1970-01-01T00:00:10Z')
    expect(tracker.observe(sameSecond, 300)).toBe('read')
    tracker.recordSync(4, 10_350)
    // Still the same stat 2s later: second 10 is over, so this read is final.
    expect(tracker.observe(sameSecond, 2_300)).toBe('read-and-lock')
    tracker.recordSync(4, 12_350)
    tracker.lock(sameSecond)
    expect(tracker.observe(sameSecond, 4_300)).toBe('skip')
  })

  it('reads at most once while a changed fingerprint is still settling', () => {
    const tracker = createContainerFileStatTracker()
    tracker.lock(file(4, '1970-01-01T00:00:03Z'))
    const changed = file(9, '1970-01-01T00:00:10Z')

    expect(tracker.observe(changed, 0)).toBe('read')
    expect(tracker.observe(changed, 500)).toBe('skip')
    expect(tracker.observe(changed, 1_500)).toBe('read-and-lock')
  })

  it('keeps asking for a read until one is locked, and always for a directory', () => {
    const tracker = createContainerFileStatTracker()
    tracker.recordSync(10, 20_000)
    expect(tracker.observe({ isDir: true, size: 10, modTime: '1970-01-01T00:00:01Z' }, 0)).toBe('read')

    const updated = file(25, '1970-01-01T00:00:15Z')
    expect(tracker.observe(updated, 0)).toBe('skip')
    expect(tracker.observe(updated, 2_000)).toBe('read-and-lock')
    // The read failed (lock not called): the next poll asks again.
    expect(tracker.observe(updated, 4_000)).toBe('read-and-lock')
  })

  it('reads immediately after a reset', () => {
    const tracker = createContainerFileStatTracker()
    tracker.recordSync(10, 20_000)
    tracker.reset()
    expect(tracker.observe(file(10, '1970-01-01T00:00:15Z'), 0)).toBe('read')
  })
})

// Two panels showing the same file: each renders the content its own query holds.
function mountTwoConsumers(useQueryInPanel: () => ReturnType<typeof useContainerTextFileQuery>) {
  const Consumer = {
    setup() {
      const query = useQueryInPanel()
      return () => h('p', query.data.value?.content ?? '')
    },
  }
  const host = document.createElement('div')
  const app = createApp({ setup: () => () => h('div', [h(Consumer), h(Consumer)]) })
  app.use(createPinia()).use(PiniaColada)
  app.mount(host)
  return { app, host }
}

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
    const { app, host } = mountTwoConsumers(() => {
      cache = useQueryCache()
      return useContainerTextFileQuery('bot-1', '/data/doc.md', { visible })
    })

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

  it('polls a file once for all its consumers and reads an external change once per settle step', async () => {
    vi.useFakeTimers({ now: 600_000 })
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    sdk.read.mockResolvedValue({
      data: { path: '/data/doc.md', content: '# v1', revision: 'rev-1', size: 4 },
    })
    const stat = (size: number, modTime: string) => ({
      data: { path: '/data/doc.md', name: 'doc.md', isDir: false, size, mode: '-rw-r--r--', modTime },
    })
    sdk.stat.mockResolvedValue(stat(4, '1970-01-01T00:00:01Z'))

    const { app, host } = mountTwoConsumers(() => useContainerTextFileQuery('bot-1', '/data/doc.md'))
    await vi.advanceTimersByTimeAsync(0)
    expect(sdk.read).toHaveBeenCalledTimes(1)

    // Three poll rounds of two consumers: one stat per round, no body read.
    await vi.advanceTimersByTimeAsync(6_000)
    expect(sdk.stat).toHaveBeenCalledTimes(3)
    expect(sdk.read).toHaveBeenCalledTimes(1)

    // External change: read right away, once more when the stat has settled,
    // then back to stats only.
    sdk.stat.mockResolvedValue(stat(9, '1970-01-01T00:09:58Z'))
    sdk.read.mockResolvedValue({
      data: { path: '/data/doc.md', content: '# changed', revision: 'rev-2', size: 9 },
    })
    await vi.advanceTimersByTimeAsync(2_000)
    expect(sdk.read).toHaveBeenCalledTimes(2)
    expect(host.querySelectorAll('p')[1]?.textContent).toBe('# changed')
    await vi.advanceTimersByTimeAsync(2_000)
    expect(sdk.read).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(6_000)
    expect(sdk.read).toHaveBeenCalledTimes(3)

    app.unmount()
  })
})
