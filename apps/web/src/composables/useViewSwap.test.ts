// @vitest-environment jsdom
import { effectScope, nextTick, ref } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useRoutedViewSwap, useViewSwap } from './useViewSwap'

const mocks = vi.hoisted(() => ({
  route: { query: {} as Record<string, string | undefined> },
  replace: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => mocks.route,
  useRouter: () => ({ replace: mocks.replace }),
}))

interface Item {
  routeValue: string
}

function setupSwap(key = 'provider') {
  const scope = effectScope()
  const swap = scope.run(() => useViewSwap(key))!
  return { scope, swap }
}

function setupRouted(key = 'provider') {
  const items = ref<Item[]>([])
  const selected = ref<Item>()
  const loading = ref(false)
  const ready = ref(false)
  const scope = effectScope()
  const swap = scope.run(() => useRoutedViewSwap({
    key,
    items: () => items.value,
    selected: () => selected.value,
    select: item => selected.value = item,
    getRouteValue: item => item.routeValue,
    isLoading: () => loading.value,
    isReady: () => ready.value,
  }))!

  return { items, selected, loading, ready, scope, swap }
}

describe('useViewSwap', () => {
  beforeEach(() => {
    mocks.route.query = {}
    mocks.replace.mockReset()
  })

  it('writes a non-empty resource id without history', () => {
    mocks.route.query = { tab: 'kept' }
    const { scope, swap } = setupSwap('memoryProvider')

    swap.openDetail('memory-1')

    expect(swap.view.value).toBe('detail')
    expect(mocks.replace).toHaveBeenCalledWith({
      query: { tab: 'kept', memoryProvider: 'memory-1' },
    })
    scope.stop()
  })

  it('refuses to write an empty resource id (no silent 1 fallback)', () => {
    const { scope, swap } = setupSwap('provider')

    swap.openDetail('')
    swap.openDetail(undefined)

    expect(swap.view.value).toBe('list')
    expect(mocks.replace).not.toHaveBeenCalled()
    scope.stop()
  })

  it('opens detail from a non-empty query value on mount', () => {
    mocks.route.query = { provider: 'abc' }
    const { scope, swap } = setupSwap('provider')

    expect(swap.view.value).toBe('detail')
    expect(swap.queryValue.value).toBe('abc')
    scope.stop()
  })

  it('clears only its own query key when returning to the list', async () => {
    mocks.route.query = { provider: 'abc', tab: 'kept' }
    const { scope, swap } = setupSwap('provider')

    swap.backToList()
    await nextTick()

    expect(swap.view.value).toBe('list')
    expect(mocks.replace).toHaveBeenCalledWith({ query: { tab: 'kept' } })
    scope.stop()
  })

  it('allows unkeyed local swaps without writing the URL', () => {
    const scope = effectScope()
    const swap = scope.run(() => useViewSwap())!

    swap.openDetail()
    expect(swap.view.value).toBe('detail')
    expect(mocks.replace).not.toHaveBeenCalled()

    swap.backToList()
    expect(swap.view.value).toBe('list')
    expect(mocks.replace).not.toHaveBeenCalled()
    scope.stop()
  })
})

describe('useRoutedViewSwap', () => {
  beforeEach(() => {
    mocks.route.query = {}
    mocks.replace.mockReset()
  })

  it('waits for fresh data before resolving a resource key', async () => {
    mocks.route.query = { provider: 'fetch:two' }
    const state = setupRouted()
    state.items.value = [{ routeValue: 'search:one' }]
    state.loading.value = true
    state.ready.value = true
    await nextTick()

    expect(state.selected.value).toBeUndefined()
    expect(state.swap.isDetailLoading.value).toBe(true)
    expect(mocks.replace).not.toHaveBeenCalled()

    state.items.value = [
      { routeValue: 'search:one' },
      { routeValue: 'fetch:two' },
    ]
    state.loading.value = false
    await nextTick()

    expect(state.selected.value?.routeValue).toBe('fetch:two')
    expect(state.swap.isDetailLoading.value).toBe(false)
    state.scope.stop()
  })

  it('removes an invalid resource key once its source is ready', async () => {
    mocks.route.query = { provider: 'search:missing', tab: 'voice' }
    const state = setupRouted()
    state.ready.value = true
    await nextTick()

    expect(mocks.replace).toHaveBeenCalledWith({ query: { tab: 'voice' } })
    state.scope.stop()
  })

  it('writes the selected resource key without creating history', () => {
    mocks.route.query = { tab: 'voice' }
    const state = setupRouted()

    state.swap.openDetail({ routeValue: 'speech:one' })

    expect(state.selected.value?.routeValue).toBe('speech:one')
    expect(mocks.replace).toHaveBeenCalledWith({
      query: { tab: 'voice', provider: 'speech:one' },
    })
    state.scope.stop()
  })

  it('does not strip another page key under KeepAlive (unique keys)', async () => {
    // Simulate two settings pages still mounted: providers (cached) and memory (active).
    mocks.route.query = { memoryProvider: 'memory-1' }

    const providers = setupRouted('provider')
    providers.items.value = [{ routeValue: 'llm-1' }]
    providers.ready.value = true
    providers.loading.value = false

    const memory = setupRouted('memoryProvider')
    memory.items.value = [{ routeValue: 'memory-1' }]
    memory.ready.value = true
    memory.loading.value = false
    await nextTick()

    // Providers page sees no `provider` key — must not touch memoryProvider.
    expect(providers.selected.value).toBeUndefined()
    expect(memory.selected.value?.routeValue).toBe('memory-1')
    expect(mocks.replace).not.toHaveBeenCalled()

    providers.scope.stop()
    memory.scope.stop()
  })

  it('would race if two pages shared one key (documents why keys must be unique)', async () => {
    mocks.route.query = { provider: 'memory-1' }

    const providers = setupRouted('provider')
    providers.items.value = [{ routeValue: 'llm-1' }]
    providers.ready.value = true
    await nextTick()

    // Shared key + foreign id → cached page strips the query.
    expect(mocks.replace).toHaveBeenCalledWith({ query: {} })
    providers.scope.stop()
  })
})
