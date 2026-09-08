// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, shallowRef } from 'vue'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import { useContextTrajectory } from './useContextTrajectory'

const { fetchPage } = vi.hoisted(() => ({ fetchPage: vi.fn() }))
const target = shallowRef({ botId: 'bot', sessionId: 'a', viewId: 'view' })
const active = shallowRef(true)
vi.mock('@memohai/sdk', () => ({ getBotsByBotIdSessionsBySessionIdContextTrajectory: fetchPage }))
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
})
afterEach(() => { unmount?.(); vi.useRealTimers() })

describe('context trajectory pages', () => {
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
