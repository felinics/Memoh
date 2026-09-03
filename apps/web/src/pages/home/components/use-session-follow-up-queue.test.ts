import { effectScope, nextTick, ref } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  fetchSessionQueues: vi.fn(),
  updateFollowUpQueueItem: vi.fn(),
  updateSteerQueueItem: vi.fn(),
  deleteFollowUpQueueItem: vi.fn(),
  deleteSteerQueueItem: vi.fn(),
  promoteFollowUpQueueItemToSteer: vi.fn(),
  reorderFollowUpQueue: vi.fn(),
  reorderSteerQueue: vi.fn(),
  queueItemText: (item: { text?: string }) => item.text ?? '',
}))
vi.mock('@/composables/api/useChat.chat-api', () => api)

import { QUEUE_FALLBACK_REFRESH_MS, useSessionFollowUpQueue } from './use-session-follow-up-queue'

const item = (id: string, text: string) => ({ item_id: id, status: 'accepted', position: 1, text })

describe('useSessionFollowUpQueue', () => {
  beforeEach(() => vi.clearAllMocks())

  it('edits and removes follow-up items through follow-up APIs only', async () => {
    api.fetchSessionQueues.mockResolvedValue({ follow_up: [item('f1', 'follow')], steer: [] })
    api.updateFollowUpQueueItem.mockResolvedValue(item('f1', 'edited'))
    api.deleteFollowUpQueueItem.mockResolvedValue(undefined)
    const queue = useSessionFollowUpQueue(ref('bot'), ref('session'))
    await nextTick()
    await Promise.resolve()

    queue.items.value[0]!.text = 'edited'
    await queue.update(queue.items.value[0]!)
    expect(api.updateFollowUpQueueItem).toHaveBeenCalledWith('bot', 'session', 'f1', 'edited')

    await queue.remove(queue.items.value[0]!)
    expect(api.deleteFollowUpQueueItem).toHaveBeenCalledWith('bot', 'session', 'f1')
    expect(queue.items.value).toHaveLength(0)
  })

  it('sends the item after the dropped row as the before reference, or empty for append', async () => {
    api.fetchSessionQueues.mockResolvedValue({ follow_up: [item('f1', 'one'), item('f2', 'two'), item('f3', 'three')], steer: [] })
    api.reorderFollowUpQueue.mockResolvedValue([])
    const queue = useSessionFollowUpQueue(ref('bot'), ref('session'))
    await nextTick()
    await Promise.resolve()
    await queue.reorder(0, 2)
    expect(api.reorderFollowUpQueue).toHaveBeenCalledWith('bot', 'session', 'f1', '')
  })

  it('keeps a promoted steer visible through the separate steer queue', async () => {
    api.fetchSessionQueues
      .mockResolvedValueOnce({ follow_up: [item('f1', 'steer this')], steer: [] })
      .mockResolvedValueOnce({ follow_up: [], steer: [item('f1', 'steer this')] })
    api.promoteFollowUpQueueItemToSteer.mockResolvedValue(item('f1', 'steer this'))
    const queue = useSessionFollowUpQueue(ref('bot'), ref('session'))
    await nextTick()
    await Promise.resolve()

    await queue.steer(queue.items.value[0]!)

    expect(api.promoteFollowUpQueueItemToSteer).toHaveBeenCalledWith('bot', 'session', 'f1')
    expect(queue.items.value).toHaveLength(1)
    expect(queue.items.value[0]?.queueKind).toBe('steer')
    expect(api.deleteFollowUpQueueItem).not.toHaveBeenCalled()
  })

  it('keeps the follow-up visible when promotion fails', async () => {
    api.fetchSessionQueues.mockResolvedValue({ follow_up: [item('f1', 'keep me')], steer: [] })
    api.promoteFollowUpQueueItemToSteer.mockRejectedValue(new Error('no active run'))
    const queue = useSessionFollowUpQueue(ref('bot'), ref('session'))
    await nextTick()
    await Promise.resolve()

    await expect(queue.steer(queue.items.value[0]!)).rejects.toThrow('no active run')

    expect(queue.items.value).toHaveLength(1)
    expect(queue.items.value[0]?.item_id).toBe('f1')
  })

  it('refreshes when the change signal moves and falls back to a slow timer', async () => {
    vi.useFakeTimers()
    const scope = effectScope()
    try {
      api.fetchSessionQueues
        .mockResolvedValueOnce({ follow_up: [item('f1', 'follow-up')], steer: [] })
        .mockResolvedValueOnce({ follow_up: [item('f1', 'follow-up')], steer: [] })
        .mockResolvedValueOnce({ follow_up: [], steer: [] })
      const initialFetchCount = api.fetchSessionQueues.mock.calls.length
      const signal = ref('a')

      const queue = scope.run(() => useSessionFollowUpQueue(ref('bot'), ref('session'), ref(true), signal))!
      await nextTick()
      await Promise.resolve()
      expect(queue.items.value).toHaveLength(1)

      // A runtime event refreshes immediately without waiting for the timer.
      signal.value = 'b'
      await nextTick()
      await Promise.resolve()
      expect(api.fetchSessionQueues).toHaveBeenCalledTimes(initialFetchCount + 2)

      // No second-by-second polling: nothing happens before the fallback window.
      await vi.advanceTimersByTimeAsync(QUEUE_FALLBACK_REFRESH_MS - 1000)
      expect(api.fetchSessionQueues).toHaveBeenCalledTimes(initialFetchCount + 2)

      await vi.advanceTimersByTimeAsync(1000)
      await nextTick()
      expect(api.fetchSessionQueues).toHaveBeenCalledTimes(initialFetchCount + 3)
      expect(queue.items.value).toHaveLength(0)

      // Once empty the fallback timer stops.
      await vi.advanceTimersByTimeAsync(QUEUE_FALLBACK_REFRESH_MS)
      expect(api.fetchSessionQueues).toHaveBeenCalledTimes(initialFetchCount + 3)
    } finally {
      scope.stop()
      vi.useRealTimers()
    }
  })

  it('keeps the other queue kind when a reorder response covers one kind', async () => {
    api.fetchSessionQueues.mockResolvedValue({ steer: [item('s1', 'steer')], follow_up: [item('f1', 'one'), item('f2', 'two')] })
    api.reorderFollowUpQueue.mockResolvedValue([item('f2', 'two'), item('f1', 'one')])
    const queue = useSessionFollowUpQueue(ref('bot'), ref('session'))
    await nextTick()
    await Promise.resolve()
    expect(queue.items.value.map(entry => entry.item_id)).toEqual(['s1', 'f1', 'f2'])

    await queue.reorder(2, 1)

    expect(queue.items.value.map(entry => entry.item_id)).toEqual(['s1', 'f2', 'f1'])
    expect(queue.items.value[0]?.queueKind).toBe('steer')
  })
})
