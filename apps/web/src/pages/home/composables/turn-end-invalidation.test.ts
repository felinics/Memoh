import { nextTick, ref } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { QueryCache } from '@pinia/colada'
import { installTurnEndInvalidation } from './turn-end-invalidation'

type FakeCache = QueryCache & { invalidateQueries: ReturnType<typeof vi.fn> }

function fakeCache(): FakeCache {
  return { invalidateQueries: vi.fn() } as unknown as FakeCache
}

function predicateOf(cache: FakeCache, call = 0): (entry: { key: unknown[] }) => boolean {
  const filter = cache.invalidateQueries.mock.calls[call]?.[0] as { predicate: (entry: { key: unknown[] }) => boolean } | undefined
  expect(filter?.predicate).toBeTypeOf('function')
  return filter!.predicate
}

const entry = (key: unknown[]) => ({ key })

afterEach(() => {
  vi.useRealTimers()
})

describe('installTurnEndInvalidation', () => {
  it('invalidates the finished session once per turn end, however many callers installed it', async () => {
    vi.useFakeTimers()
    const streaming = ref<string[]>([])
    const cache = fakeCache()
    installTurnEndInvalidation(streaming, cache)
    installTurnEndInvalidation(streaming, cache)

    streaming.value = ['s1']
    await nextTick()
    expect(cache.invalidateQueries).not.toHaveBeenCalled()

    streaming.value = []
    await nextTick()
    expect(cache.invalidateQueries).toHaveBeenCalledTimes(1)
    const predicate = predicateOf(cache)
    expect(predicate(entry(['session-status', 'b', 's1', '']))).toBe(true)
    expect(predicate(entry(['session-status', 'b', 's1', 'model-x']))).toBe(true)
    expect(predicate(entry(['context-lifecycle', 'b', 's1', 50]))).toBe(true)
    expect(predicate(entry(['session-status', 'b', 's2', '']))).toBe(false)
    expect(predicate(entry(['context-lifecycle', 'b', 's2', 50]))).toBe(false)
    expect(predicate(entry(['bot', 's1']))).toBe(false)
  })

  it('refetches once more shortly after, when the lifecycle row has had time to land', async () => {
    vi.useFakeTimers()
    const streaming = ref<string[]>(['s1'])
    const cache = fakeCache()
    installTurnEndInvalidation(streaming, cache)

    streaming.value = []
    await nextTick()
    expect(cache.invalidateQueries).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(1500)
    expect(cache.invalidateQueries).toHaveBeenCalledTimes(2)
    expect(predicateOf(cache, 1)(entry(['session-status', 'b', 's1', '']))).toBe(true)
  })

  it('only invalidates the session that finished when another pane keeps streaming', async () => {
    vi.useFakeTimers()
    const streaming = ref<string[]>(['s1', 's2'])
    const cache = fakeCache()
    installTurnEndInvalidation(streaming, cache)

    streaming.value = ['s2']
    await nextTick()
    expect(cache.invalidateQueries).toHaveBeenCalledTimes(1)
    const predicate = predicateOf(cache)
    expect(predicate(entry(['session-status', 'b', 's1', '']))).toBe(true)
    expect(predicate(entry(['session-status', 'b', 's2', '']))).toBe(false)
  })

  it('installs independently per query cache', async () => {
    vi.useFakeTimers()
    const streaming = ref<string[]>(['s1'])
    const a = fakeCache()
    const b = fakeCache()
    installTurnEndInvalidation(streaming, a)
    installTurnEndInvalidation(streaming, b)

    streaming.value = []
    await nextTick()
    expect(a.invalidateQueries).toHaveBeenCalledTimes(1)
    expect(b.invalidateQueries).toHaveBeenCalledTimes(1)
  })
})
