import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref } from 'vue'
import type { BotsBot } from '@memohai/sdk'
import { usePendingBotsRefresh } from './usePendingBotsRefresh'

const invalidateQueries = vi.fn()

vi.mock('@pinia/colada', () => ({
  useQueryCache: () => ({ invalidateQueries }),
}))

vi.mock('@memohai/sdk/colada', () => ({
  getBotsQueryKey: () => ['bots'],
}))

function bot(id: string, status: string): BotsBot {
  return { id, status } as BotsBot
}

describe('usePendingBotsRefresh', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    invalidateQueries.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('refetches the bots list while a bot is being deleted', () => {
    const bots = ref([bot('a', 'ready'), bot('b', 'deleting')])
    const scope = effectScope()
    scope.run(() => usePendingBotsRefresh(bots))

    vi.advanceTimersByTime(2000)
    expect(invalidateQueries).toHaveBeenCalledWith({ key: ['bots'] })

    scope.stop()
  })

  it('refetches while a bot is being created', () => {
    const bots = ref([bot('a', 'creating')])
    const scope = effectScope()
    scope.run(() => usePendingBotsRefresh(bots))

    vi.advanceTimersByTime(2000)
    expect(invalidateQueries).toHaveBeenCalledTimes(1)

    scope.stop()
  })

  it('stops once no bot is pending', async () => {
    const bots = ref([bot('b', 'deleting')])
    const scope = effectScope()
    scope.run(() => usePendingBotsRefresh(bots))

    bots.value = [bot('a', 'ready')]
    await nextTick()
    vi.advanceTimersByTime(10_000)
    expect(invalidateQueries).not.toHaveBeenCalled()

    scope.stop()
  })

  it('stops when its owner is disposed', () => {
    const bots = ref([bot('b', 'deleting')])
    const scope = effectScope()
    scope.run(() => usePendingBotsRefresh(bots))

    scope.stop()
    vi.advanceTimersByTime(10_000)
    expect(invalidateQueries).not.toHaveBeenCalled()
  })

  it('does nothing while every bot is settled', () => {
    const bots = ref([bot('a', 'ready'), bot('c', 'error')])
    const scope = effectScope()
    scope.run(() => usePendingBotsRefresh(bots))

    vi.advanceTimersByTime(10_000)
    expect(invalidateQueries).not.toHaveBeenCalled()

    scope.stop()
  })
})
