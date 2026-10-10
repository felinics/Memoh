import { effectScope, nextTick, ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import type { ComposerPrefillRequest } from '@/store/composer-prefill'
import { useComposerPrefillResolver } from './useComposerPrefillResolver'

function setup(state: { botId?: string | null, dockReady?: boolean, activeChatWritable?: boolean } = {}) {
  const pending = ref<ComposerPrefillRequest | null>(null)
  const currentBotId = ref<string | null>(state.botId === undefined ? 'bot-a' : state.botId)
  const dockReady = ref(state.dockReady ?? true)
  const activeChatWritable = ref(state.activeChatWritable ?? true)
  const openDraftChat = vi.fn()
  const scope = effectScope()
  scope.run(() => useComposerPrefillResolver({
    pending: () => pending.value,
    currentBotId: () => currentBotId.value,
    dockReady: () => dockReady.value,
    activeChatWritable: () => activeChatWritable.value,
    openDraftChat,
  }))
  const request = (botId = 'bot-a') => { pending.value = { id: Date.now(), botId, text: 'hi' } }
  return { pending, currentBotId, dockReady, activeChatWritable, openDraftChat, request, scope }
}

describe('useComposerPrefillResolver', () => {
  it('leaves the request to the active writable chat', async () => {
    const ctx = setup()
    ctx.request()
    await nextTick()
    expect(ctx.openDraftChat).not.toHaveBeenCalled()
  })

  it('opens a new chat when the active chat is read-only or not a chat', async () => {
    const ctx = setup({ activeChatWritable: false })
    ctx.request()
    await nextTick()
    expect(ctx.openDraftChat).toHaveBeenCalledOnce()
  })

  it('waits until the requested bot is the current bot', async () => {
    const ctx = setup({ activeChatWritable: false })
    ctx.request('bot-b')
    await nextTick()
    expect(ctx.openDraftChat).not.toHaveBeenCalled()
    ctx.currentBotId.value = 'bot-b'
    await nextTick()
    expect(ctx.openDraftChat).toHaveBeenCalledOnce()
  })

  it('waits until the dock is mounted', async () => {
    const ctx = setup({ dockReady: false, activeChatWritable: false })
    ctx.request()
    await nextTick()
    expect(ctx.openDraftChat).not.toHaveBeenCalled()
    ctx.dockReady.value = true
    await nextTick()
    expect(ctx.openDraftChat).toHaveBeenCalledOnce()
  })

  it('opens at most one new chat per request', async () => {
    const ctx = setup({ activeChatWritable: false })
    ctx.request()
    await nextTick()
    ctx.dockReady.value = false
    await nextTick()
    ctx.dockReady.value = true
    await nextTick()
    expect(ctx.openDraftChat).toHaveBeenCalledOnce()
  })

  it('does nothing without a pending request', async () => {
    const ctx = setup({ activeChatWritable: false })
    ctx.activeChatWritable.value = false
    await nextTick()
    expect(ctx.openDraftChat).not.toHaveBeenCalled()
  })
})
