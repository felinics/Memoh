import { nextTick, ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'
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

describe('installTurnEndInvalidation', () => {
  it('invalidates the finished session status once per turn end, however many callers installed it', async () => {
    const streaming = ref<string | null>(null)
    const cache = fakeCache()
    installTurnEndInvalidation(streaming, cache)
    installTurnEndInvalidation(streaming, cache)

    streaming.value = 's1'
    await nextTick()
    expect(cache.invalidateQueries).not.toHaveBeenCalled()

    streaming.value = null
    await nextTick()
    expect(cache.invalidateQueries).toHaveBeenCalledTimes(1)
    const predicate = predicateOf(cache)
    expect(predicate(entry(['session-status', 'b', 's1', '']))).toBe(true)
    expect(predicate(entry(['session-status', 'b', 's1', 'model-x']))).toBe(true)
    expect(predicate(entry(['session-status', 'b', 's2', '']))).toBe(false)
    expect(predicate(entry(['context-lifecycle', 'b', 's1', 50]))).toBe(true)
    expect(predicate(entry(['context-lifecycle', 'b', 's2', 50]))).toBe(false)
    expect(predicate(entry(['bot', 's1']))).toBe(false)
  })

  it('treats a switch straight to another streaming session as the first one finishing', async () => {
    const streaming = ref<string | null>('s1')
    const cache = fakeCache()
    installTurnEndInvalidation(streaming, cache)

    streaming.value = 's2'
    await nextTick()
    expect(cache.invalidateQueries).toHaveBeenCalledTimes(1)
    expect(predicateOf(cache)(entry(['session-status', 'b', 's1', '']))).toBe(true)
  })

  it('installs independently per query cache', async () => {
    const streaming = ref<string | null>('s1')
    const a = fakeCache()
    const b = fakeCache()
    installTurnEndInvalidation(streaming, a)
    installTurnEndInvalidation(streaming, b)

    streaming.value = null
    await nextTick()
    expect(a.invalidateQueries).toHaveBeenCalledTimes(1)
    expect(b.invalidateQueries).toHaveBeenCalledTimes(1)
  })
})
