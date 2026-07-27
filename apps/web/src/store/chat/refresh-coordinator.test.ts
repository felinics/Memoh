import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import type { FetchSessionsResult } from '@/composables/api/useChat'
import { createChatRefreshCoordinator } from './refresh-coordinator'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

function page(id: string): FetchSessionsResult {
  return {
    items: [{ id, bot_id: 'bot-1', title: id, type: 'chat' }],
    nextCursor: null,
  }
}

function makeCoordinator() {
  const currentBotId = ref<string | null>('bot-1')
  const fetchSessions = vi.fn<(_botId: string) => Promise<FetchSessionsResult>>()
  const applySessionsSnapshot = vi.fn()
  return {
    currentBotId,
    fetchSessions,
    applySessionsSnapshot,
    coordinator: createChatRefreshCoordinator({
      currentBotId,
      fetchSessions,
      applySessionsSnapshot,
    }),
  }
}

describe('chat refresh coordinator', () => {
  it('deduplicates session-list refreshes for the same bot', async () => {
    const { coordinator, fetchSessions, applySessionsSnapshot } = makeCoordinator()
    const request = deferred<FetchSessionsResult>()
    fetchSessions.mockReturnValue(request.promise)

    const first = coordinator.refreshSessionsList('bot-1')
    const second = coordinator.refreshSessionsList('bot-1')
    expect(fetchSessions).toHaveBeenCalledOnce()

    request.resolve(page('session-current'))
    await Promise.all([first, second])
    expect(applySessionsSnapshot).toHaveBeenCalledOnce()
  })

  it('invalidates old responses and lets a new scope request win', async () => {
    const { coordinator, fetchSessions, applySessionsSnapshot } = makeCoordinator()
    const oldRequest = deferred<FetchSessionsResult>()
    const newRequest = deferred<FetchSessionsResult>()
    fetchSessions
      .mockReturnValueOnce(oldRequest.promise)
      .mockReturnValueOnce(newRequest.promise)

    const oldRefresh = coordinator.refreshSessionsList('bot-1')
    coordinator.resetRefreshCoordinator()
    const newRefresh = coordinator.refreshSessionsList('bot-1')
    newRequest.resolve(page('session-new'))
    await newRefresh
    oldRequest.resolve(page('session-old'))
    await oldRefresh

    expect(applySessionsSnapshot).toHaveBeenCalledOnce()
    expect(applySessionsSnapshot).toHaveBeenCalledWith(page('session-new'))
  })

  it('ignores a response when its bot is no longer active', async () => {
    const { coordinator, currentBotId, fetchSessions, applySessionsSnapshot } = makeCoordinator()
    const request = deferred<FetchSessionsResult>()
    fetchSessions.mockReturnValue(request.promise)

    const refresh = coordinator.refreshSessionsList('bot-1')
    currentBotId.value = 'bot-2'
    request.resolve(page('session-old'))
    await refresh

    expect(applySessionsSnapshot).not.toHaveBeenCalled()
  })
})
