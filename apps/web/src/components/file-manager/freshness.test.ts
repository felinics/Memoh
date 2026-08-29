import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createFsWatchReporter, createSequentialLoader, dirsFromChangedPaths, nodeNeedsRefresh } from './freshness'

describe('dirsFromChangedPaths', () => {
  it('maps changed paths to their parents plus themselves', () => {
    const dirs = dirsFromChangedPaths(['/data/a.txt', '/data/sub/b.txt'])
    expect(dirs).toEqual(['/data', '/data/a.txt', '/data/sub', '/data/sub/b.txt'])
  })

  it('re-lists a changed directory itself, not only its parent', () => {
    // A stale-directory signal names the directory whose CONTENTS may have
    // changed; matching only the parent would skip re-listing it.
    const dirs = dirsFromChangedPaths(['/data/sub'])
    expect(dirs).toContain('/data/sub')
    expect(dirs).toContain('/data')
  })

  it('returns null for wildcard input', () => {
    expect(dirsFromChangedPaths(null)).toBeNull()
    expect(dirsFromChangedPaths(undefined)).toBeNull()
  })

  it('ignores empty paths', () => {
    expect(dirsFromChangedPaths(['', '/data/a.txt'])).toEqual(['/data', '/data/a.txt'])
  })
})

describe('nodeNeedsRefresh', () => {
  it('matches wildcard signals', () => {
    expect(nodeNeedsRefresh('/data/sub', { dirs: null })).toBe(true)
  })

  it('matches when the node dir is listed', () => {
    expect(nodeNeedsRefresh('/data/sub', { dirs: ['/data', '/data/sub'] })).toBe(true)
  })

  it('skips unrelated dirs', () => {
    expect(nodeNeedsRefresh('/data/other', { dirs: ['/data/sub'] })).toBe(false)
  })
})

describe('createSequentialLoader', () => {
  it('runs a single load immediately', async () => {
    const load = vi.fn().mockResolvedValue(undefined)
    const loader = createSequentialLoader(load)
    loader.request(true)
    await Promise.resolve()
    expect(load).toHaveBeenCalledTimes(1)
    expect(load).toHaveBeenCalledWith(true)
  })

  it('coalesces requests while a load is in flight and reruns once', async () => {
    let release: () => void = () => {}
    const first = new Promise<void>((resolve) => { release = resolve })
    const load = vi.fn().mockReturnValueOnce(first).mockResolvedValue(undefined)
    const loader = createSequentialLoader(load)
    loader.request(true)
    loader.request(true)
    loader.request(true)
    expect(load).toHaveBeenCalledTimes(1)
    release()
    await first
    await Promise.resolve()
    expect(load).toHaveBeenCalledTimes(2)
  })

  it('a foreground request during flight makes the rerun foreground', async () => {
    let release: () => void = () => {}
    const first = new Promise<void>((resolve) => { release = resolve })
    const load = vi.fn().mockReturnValueOnce(first).mockResolvedValue(undefined)
    const loader = createSequentialLoader(load)
    loader.request(true)
    loader.request(false)
    loader.request(true)
    release()
    await first
    await Promise.resolve()
    expect(load).toHaveBeenLastCalledWith(false)
  })

  it('keeps working after a load rejects', async () => {
    const load = vi.fn()
      .mockRejectedValueOnce(new Error('boom'))
      .mockResolvedValue(undefined)
    const loader = createSequentialLoader(load)
    loader.request(false)
    await Promise.resolve()
    await Promise.resolve()
    loader.request(true)
    await Promise.resolve()
    expect(load).toHaveBeenCalledTimes(2)
  })
})

describe('createFsWatchReporter', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('debounces updates and sends the latest set', () => {
    const send = vi.fn()
    const reporter = createFsWatchReporter({ send, debounceMs: 300 })
    reporter.update(['/data'])
    reporter.update(['/data', '/data/sub'])
    expect(send).not.toHaveBeenCalled()
    vi.advanceTimersByTime(300)
    expect(send).toHaveBeenCalledTimes(1)
    expect(send).toHaveBeenCalledWith(['/data', '/data/sub'])
  })

  it('skips resending an unchanged set', () => {
    const send = vi.fn()
    const reporter = createFsWatchReporter({ send, debounceMs: 300 })
    reporter.update(['/data'])
    vi.advanceTimersByTime(300)
    reporter.update(['/data'])
    vi.advanceTimersByTime(300)
    expect(send).toHaveBeenCalledTimes(1)
  })

  it('sends an empty set when cleared', () => {
    const send = vi.fn()
    const reporter = createFsWatchReporter({ send, debounceMs: 300 })
    reporter.update(['/data'])
    vi.advanceTimersByTime(300)
    reporter.update([])
    vi.advanceTimersByTime(300)
    expect(send).toHaveBeenLastCalledWith([])
  })

  it('stop cancels pending sends', () => {
    const send = vi.fn()
    const reporter = createFsWatchReporter({ send, debounceMs: 300 })
    reporter.update(['/data'])
    reporter.stop()
    vi.advanceTimersByTime(1000)
    expect(send).not.toHaveBeenCalled()
  })
})
