import { effectScope, nextTick, ref } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useComposerPrefillStore } from '@/store/composer-prefill'
import { useComposerPrefillConsumer } from './useComposerPrefillConsumer'

function mountPane(state: { botId?: string, active?: boolean, writable?: boolean, ready?: boolean } = {}) {
  const botId = ref(state.botId ?? 'bot-a')
  const ready = ref(state.ready ?? true)
  const active = ref(state.active ?? true)
  const writable = ref(state.writable ?? true)
  const apply = vi.fn()
  effectScope().run(() => useComposerPrefillConsumer({
    botId: () => botId.value,
    active: () => active.value,
    writable: () => writable.value,
    ready: () => ready.value,
    apply,
  }))
  return { botId, active, writable, ready, apply }
}

describe('useComposerPrefillConsumer', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('applies a request addressed to its bot while active and writable', async () => {
    const pane = mountPane()
    useComposerPrefillStore().request('bot-a', 'hello')
    await nextTick()
    expect(pane.apply).toHaveBeenCalledWith('hello')
    expect(useComposerPrefillStore().pending).toBeNull()
  })

  it('applies a request that was queued before the pane mounted', () => {
    useComposerPrefillStore().request('bot-a', 'hello')
    const pane = mountPane()
    expect(pane.apply).toHaveBeenCalledWith('hello')
  })

  it('ignores requests for another bot', async () => {
    const pane = mountPane()
    useComposerPrefillStore().request('bot-b', 'hello')
    await nextTick()
    expect(pane.apply).not.toHaveBeenCalled()
    expect(useComposerPrefillStore().pending?.botId).toBe('bot-b')
  })

  it('waits while inactive or read-only, then applies once it can', async () => {
    const pane = mountPane({ active: false, writable: false })
    useComposerPrefillStore().request('bot-a', 'hello')
    await nextTick()
    pane.active.value = true
    await nextTick()
    expect(pane.apply).not.toHaveBeenCalled()
    pane.writable.value = true
    await nextTick()
    expect(pane.apply).toHaveBeenCalledOnce()
  })

  it('waits while the bot is still loading, so a pane of the previous bot cannot take it', async () => {
    const pane = mountPane({ ready: false })
    useComposerPrefillStore().request('bot-a', 'hello')
    await nextTick()
    expect(pane.apply).not.toHaveBeenCalled()
    pane.ready.value = true
    await nextTick()
    expect(pane.apply).toHaveBeenCalledWith('hello')
  })

  it('lets only the active pane of two consume the request', async () => {
    const background = mountPane({ active: false })
    const foreground = mountPane()
    useComposerPrefillStore().request('bot-a', 'hello')
    await nextTick()
    expect(foreground.apply).toHaveBeenCalledOnce()
    expect(background.apply).not.toHaveBeenCalled()
  })
})
