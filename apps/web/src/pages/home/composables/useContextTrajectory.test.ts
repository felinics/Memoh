// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, createApp, defineComponent, h, KeepAlive, nextTick, shallowRef } from 'vue'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import { useContextTrajectory } from './useContextTrajectory'
import { useContextTrajectoryEvent } from './useContextTrajectoryEvent'

const { fetchPage, fetchEvent } = vi.hoisted(() => ({ fetchPage: vi.fn(), fetchEvent: vi.fn() }))
const target = shallowRef({ botId: 'bot', sessionId: 'a', viewId: 'view' })
const active = shallowRef(true)
vi.mock('@memohai/sdk', () => ({ getBotsByBotIdSessionsBySessionIdContextTrajectory: fetchPage, getBotsByBotIdSessionsBySessionIdContextTrajectoryByEventId: fetchEvent }))
vi.mock('./useChatViewContext', () => ({ useChatViewTarget: () => target }))

let unmount: (() => void) | undefined

function flushPromises(): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, 0))
}

function setup() {
  let result!: ReturnType<typeof useContextTrajectory>
  const app = createApp(defineComponent({
    setup() {
      result = useContextTrajectory(active)
      return () => h('div')
    },
  }))
  app.use(createPinia())
  app.use(PiniaColada)
  const container = document.createElement('div')
  document.body.append(container)
  app.mount(container)
  unmount = () => { app.unmount(); container.remove() }
  return result
}

beforeEach(() => {
  target.value = { botId: 'bot', sessionId: 'a', viewId: 'view' }
  active.value = true
  fetchPage.mockReset()
  fetchEvent.mockReset()
})
afterEach(() => { unmount?.(); vi.useRealTimers() })

describe('context trajectory pages', () => {
  it('removes cached captures when a refreshed session has been cleared', async () => {
    let cleared = false
    fetchPage.mockImplementation(({ query }) => Promise.resolve({ data: cleared
      ? { events: [], has_more: false }
      : query.before ? { events: [{ id: '8' }], has_more: false } : { events: [{ id: '10' }, { id: '9' }], has_more: true, next_cursor: '9' },
    }))
    const result = setup()
    await flushPromises()
    await result.loadOlder()
    expect(result.events.value.map(event => event.id)).toEqual(['8', '9', '10'])
    cleared = true
    await result.refresh()
    await flushPromises()
    expect(result.events.value).toEqual([])
    expect(result.canLoadOlder.value).toBe(false)
  })

  it('does not restore a cleared session from a late older-page response', async () => {
    let cleared = false
    let resolveOlder!: (value: unknown) => void
    const older = new Promise(resolve => { resolveOlder = resolve })
    fetchPage.mockImplementation(({ query }) => query.before ? older : Promise.resolve({ data: cleared
      ? { events: [], has_more: false }
      : { events: [{ id: '10' }], has_more: true, next_cursor: '10' },
    }))
    const result = setup()
    await flushPromises()
    const loading = result.loadOlder()
    cleared = true
    await result.refresh()
    await flushPromises()
    expect(result.events.value).toEqual([])
    resolveOlder({ data: { events: [{ id: '9' }], has_more: false } })
    await loading
    await flushPromises()
    expect(result.events.value).toEqual([])
    expect(result.loadingOlder.value).toBe(false)
  })

  it('reports a malformed response instead of treating it as an empty session', async () => {
    fetchPage.mockResolvedValue({ data: { events: [{ id: '10' }], has_more: false } })
    const result = setup()
    await flushPromises()
    fetchPage.mockResolvedValue({ data: {} })
    await result.refresh()
    await flushPromises()
    expect(result.error.value).toBeTruthy()
    expect(result.events.value.map(event => event.id)).toEqual(['10'])
  })

  it('does not fetch detail for a hidden KeepAlive pane when its target changes', async () => {
    fetchEvent.mockResolvedValue({ data: { event: { id: '2' }, blocks: [], complete: true } })
    const visible = shallowRef(true)
    const reader = { setup: () => { useContextTrajectoryEvent(computed(() => '2')); return () => h('div') } }
    const app = createApp({ setup: () => () => h(KeepAlive, null, { default: () => visible.value ? h(reader) : h('div') }) })
    app.use(createPinia())
    app.use(PiniaColada)
    const container = document.createElement('div')
    app.mount(container)
    unmount = () => app.unmount()
    await flushPromises()
    expect(fetchEvent).toHaveBeenCalledOnce()
    visible.value = false
    await flushPromises()
    target.value = { ...target.value, sessionId: 'b' }
    await flushPromises()
    expect(fetchEvent).toHaveBeenCalledOnce()
  })
  it('keeps an older-page failure visible and retries that cursor', async () => {
    let failed = true
    fetchPage.mockImplementation(({ query }) => query.before
      ? failed ? Promise.reject(new Error('older unavailable')) : Promise.resolve({ data: { events: [{ id: '9' }], has_more: false } })
      : Promise.resolve({ data: { events: [{ id: '10' }], has_more: true, next_cursor: '10' } }))
    const result = setup()
    await flushPromises()
    await result.loadOlder()
    expect(result.error.value).toBeTruthy()
    await result.refresh()
    await flushPromises()
    expect(result.error.value).toBeTruthy()
    failed = false
    await result.refresh()
    await flushPromises()
    expect(result.error.value).toBeNull()
    expect(result.events.value.map(event => event.id)).toEqual(['9', '10'])
  })

  it('never polls without a session target', async () => {
    target.value = { ...target.value, sessionId: '' }
    vi.useFakeTimers()
    setup()
    await vi.advanceTimersByTimeAsync(6000)
    expect(fetchPage).not.toHaveBeenCalled()
  })
  it('exposes initial failure and retries it', async () => {
    fetchPage.mockRejectedValueOnce(new Error('offline'))
    const result = setup()
    await flushPromises()
    expect(result.error.value).toBeTruthy()
    fetchPage.mockResolvedValue({ data: { events: [{ id: '1', run_id: 'run' }], has_more: false } })
    await result.refresh()
    await flushPromises()
    expect(result.error.value).toBeNull()
    expect(result.events.value.map(event => event.id)).toEqual(['1'])
  })

  it('fills a moving first-page gap without discarding older captures', async () => {
    let refreshed = false
    fetchPage.mockImplementation(({ query }) => Promise.resolve({ data: query.before
      ? { events: [{ id: '10' }, { id: '9' }, { id: '8' }, { id: '7' }], has_more: true, next_cursor: '7' }
      : refreshed
        ? { events: [{ id: '12' }, { id: '11' }], has_more: true, next_cursor: '11' }
        : { events: [{ id: '7' }, { id: '6' }], has_more: true, next_cursor: '6' },
    }))
    const result = setup()
    await flushPromises()
    refreshed = true
    await result.refresh()
    await flushPromises()
    expect(result.events.value.map(event => event.id)).toEqual(['6', '7', '8', '9', '10', '11', '12'])
    expect(result.hasGap.value).toBe(false)
  })

  it('rejects an old pagination response after switching away and back', async () => {
    let resolveOlder!: (value: unknown) => void
    const older = new Promise(resolve => { resolveOlder = resolve })
    fetchPage.mockImplementation(({ path, query }) => query.before ? older : Promise.resolve({ data: {
      events: [{ id: path.session_id === 'a' ? '10' : '20' }], has_more: true, next_cursor: path.session_id === 'a' ? '10' : '20',
    } }))
    const result = setup()
    await flushPromises()
    const loading = result.loadOlder()
    target.value = { ...target.value, sessionId: 'b' }
    await flushPromises()
    target.value = { ...target.value, sessionId: 'a' }
    await flushPromises()
    resolveOlder({ data: { events: [{ id: '9' }], has_more: false } })
    await loading
    await flushPromises()
    expect(result.events.value.map(event => event.id)).toEqual(['10'])
  })

  it('stops polling while the pane is hidden', async () => {
    fetchPage.mockResolvedValue({ data: { events: [], has_more: false } })
    setup()
    await flushPromises()
    vi.useFakeTimers()
    active.value = false
    await nextTick()
    const calls = fetchPage.mock.calls.length
    await vi.advanceTimersByTimeAsync(6000)
    expect(fetchPage).toHaveBeenCalledTimes(calls)
  })
})
