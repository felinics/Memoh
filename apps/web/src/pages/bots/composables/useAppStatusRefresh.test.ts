// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, KeepAlive, nextTick, ref, type App } from 'vue'
import { useAppStatusRefresh } from './useAppStatusRefresh'

let app: App | undefined
const flush = async () => { for (let i = 0; i < 5; i++) await nextTick() }

function setup(refresh = vi.fn().mockResolvedValue(undefined)) {
  const botId = ref('bot-a')
  const detailKey = ref('codex')
  const inProgress = ref(false)
  const shown = ref(true)
  const content = {
    setup() {
      useAppStatusRefresh({
        botId: () => botId.value,
        detailKey: () => detailKey.value,
        inProgress: () => inProgress.value,
        refresh,
      })
      return () => h('div')
    },
  }
  app = createApp({ render: () => h(KeepAlive, null, { default: () => shown.value ? h(content) : h('span') }) })
  app.mount(document.createElement('div'))
  return { botId, detailKey, inProgress, shown, refresh }
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
})
afterEach(() => { app?.unmount(); vi.useRealTimers(); vi.restoreAllMocks() })

describe('foreground App status observation', () => {
  it('discovers recovery that started after the cached ready state, then reads its terminal state', async () => {
    const { refresh, inProgress } = setup()
    await flush()
    expect(refresh).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(15_000)
    expect(refresh).toHaveBeenCalledTimes(2)

    inProgress.value = true
    await flush()
    await vi.advanceTimersByTimeAsync(3_000)
    expect(refresh).toHaveBeenCalledTimes(3)
    inProgress.value = false
    await flush()
    await vi.advanceTimersByTimeAsync(3_000)
    expect(refresh).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(12_000)
    expect(refresh).toHaveBeenCalledTimes(4)
  })

  it('refreshes a stale manual-required result when returning to another App detail', async () => {
    let serverStatus = 'manual_required'
    let displayedStatus = ''
    const refresh = vi.fn(async () => { displayedStatus = serverStatus })
    const { detailKey } = setup(refresh)
    await flush()
    expect(displayedStatus).toBe('manual_required')
    serverStatus = 'ready'
    detailKey.value = ''
    await flush()
    detailKey.value = 'node'
    await flush()
    expect(displayedStatus).toBe('ready')
    expect(refresh).toHaveBeenCalledTimes(3)
  })

  it('suspends while hidden or deactivated and refreshes immediately when the page returns', async () => {
    const { refresh, shown } = setup()
    await flush()
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(60_000)
    expect(refresh).toHaveBeenCalledTimes(1)
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    document.dispatchEvent(new Event('visibilitychange'))
    await flush()
    expect(refresh).toHaveBeenCalledTimes(2)

    shown.value = false
    await flush()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(refresh).toHaveBeenCalledTimes(2)
    shown.value = true
    await flush()
    expect(refresh).toHaveBeenCalledTimes(3)
  })

  it('does not overlap reads and revisits a changed bot when the previous read completes', async () => {
    let complete!: () => void
    const refresh = vi.fn().mockReturnValueOnce(new Promise<void>(resolve => { complete = resolve }))
      .mockResolvedValue(undefined)
    const { botId } = setup(refresh)
    botId.value = 'bot-b'
    await flush()
    window.dispatchEvent(new Event('focus'))
    await vi.advanceTimersByTimeAsync(30_000)
    expect(refresh).toHaveBeenCalledTimes(1)
    complete()
    await flush()
    expect(refresh).toHaveBeenCalledTimes(2)
  })

  it('continues observation after a read failure and cleans up on unmount', async () => {
    const refresh = vi.fn().mockRejectedValueOnce(new Error('network unavailable')).mockResolvedValue(undefined)
    setup(refresh)
    await flush()
    await vi.advanceTimersByTimeAsync(15_000)
    expect(refresh).toHaveBeenCalledTimes(2)
    app?.unmount()
    app = undefined
    window.dispatchEvent(new Event('focus'))
    await vi.advanceTimersByTimeAsync(60_000)
    expect(refresh).toHaveBeenCalledTimes(2)
  })
})
