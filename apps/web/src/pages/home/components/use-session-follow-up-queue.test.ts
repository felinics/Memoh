import { effectScope, nextTick, ref } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  fetchFollowUpQueue: vi.fn(),
  fetchSteerQueue: vi.fn(),
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

import { useSessionFollowUpQueue } from './use-session-follow-up-queue'

const item = (id: string, text: string) => ({ item_id: id, status: 'accepted', position: 1, text })

describe('useSessionFollowUpQueue', () => {
  beforeEach(() => vi.clearAllMocks())

  it('edits and removes follow-up items through follow-up APIs only', async () => {
    api.fetchFollowUpQueue.mockResolvedValue([item('f1', 'follow')])
    api.fetchSteerQueue.mockResolvedValue([])
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
    api.fetchFollowUpQueue.mockResolvedValue([item('f1', 'one'), item('f2', 'two'), item('f3', 'three')])
    api.fetchSteerQueue.mockResolvedValue([])
    api.reorderFollowUpQueue.mockResolvedValue([])
    const queue = useSessionFollowUpQueue(ref('bot'), ref('session'))
    await nextTick()
    await Promise.resolve()
    await queue.reorder(0, 2)
    expect(api.reorderFollowUpQueue).toHaveBeenCalledWith('bot', 'session', 'f1', '')
  })

  it('keeps a promoted steer visible through the separate steer queue', async () => {
    api.fetchFollowUpQueue
      .mockResolvedValueOnce([item('f1', 'steer this')])
      .mockResolvedValueOnce([])
    api.fetchSteerQueue
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([item('f1', 'steer this')])
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
    api.fetchFollowUpQueue.mockResolvedValue([item('f1', 'keep me')])
    api.fetchSteerQueue.mockResolvedValue([])
    api.promoteFollowUpQueueItemToSteer.mockRejectedValue(new Error('no active run'))
    const queue = useSessionFollowUpQueue(ref('bot'), ref('session'))
    await nextTick()
    await Promise.resolve()

    await expect(queue.steer(queue.items.value[0]!)).rejects.toThrow('no active run')

    expect(queue.items.value).toHaveLength(1)
    expect(queue.items.value[0]?.item_id).toBe('f1')
  })

  it('refreshes the pending queue until consumed items disappear', async () => {
    vi.useFakeTimers()
    const scope = effectScope()
    try {
      api.fetchFollowUpQueue
        .mockResolvedValueOnce([item('f1', 'follow-up')])
        .mockResolvedValueOnce([])
      api.fetchSteerQueue.mockResolvedValue([])
      const initialFetchCount = api.fetchFollowUpQueue.mock.calls.length

      const queue = scope.run(() => useSessionFollowUpQueue(ref('bot'), ref('session'), ref(true)))!
      await nextTick()
      await Promise.resolve()
      expect(queue.items.value).toHaveLength(1)

      await vi.advanceTimersByTimeAsync(1000)
      await nextTick()
      expect(api.fetchFollowUpQueue).toHaveBeenCalledTimes(initialFetchCount + 2)
      expect(queue.items.value).toHaveLength(0)

      await vi.advanceTimersByTimeAsync(1000)
      expect(api.fetchFollowUpQueue).toHaveBeenCalledTimes(initialFetchCount + 2)
    } finally {
      scope.stop()
      vi.useRealTimers()
    }
  })
})
