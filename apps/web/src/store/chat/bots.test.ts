import { afterEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { createChatBots } from './bots'
import { fetchBots } from '@/composables/api/useChat'
import type { Bot } from '@/composables/api/useChat'

vi.mock('@/composables/api/useChat', () => ({
  fetchBots: vi.fn(),
}))

const fetchBotsMock = vi.mocked(fetchBots)

function bot(id: string): Bot {
  return { id, status: 'ready' } as Bot
}

function makeBots(initialBotId: string | null = null) {
  const currentBotId = ref<string | null>(initialBotId)
  let generation = 0
  const store = createChatBots({
    currentBotId,
    userScopeGeneration: () => generation,
  })
  return {
    currentBotId,
    bumpGeneration: () => { generation += 1 },
    ...store,
  }
}

describe('chat bots ensureBot', () => {
  afterEach(() => {
    fetchBotsMock.mockReset()
  })

  it('rethrows live fetch failures so bootstrap recovery can retry (#1070)', async () => {
    fetchBotsMock.mockRejectedValue(new Error('server booting'))
    const { ensureBot, currentBotId } = makeBots('bot-1')

    await expect(ensureBot()).rejects.toThrow('server booting')
    // A failed fetch must not rewrite the current selection either.
    expect(currentBotId.value).toBe('bot-1')
  })

  it('returns null quietly when the user scope changed mid-flight', async () => {
    let rejectFetch!: (error: Error) => void
    fetchBotsMock.mockImplementation(() => new Promise<Bot[]>((_, reject) => {
      rejectFetch = reject
    }))
    const { ensureBot, bumpGeneration } = makeBots('bot-1')

    const pending = ensureBot()
    bumpGeneration()
    rejectFetch(new Error('down'))

    await expect(pending).resolves.toBeNull()
  })

  it('keeps the current bot when it is still present and ready', async () => {
    fetchBotsMock.mockResolvedValue([bot('bot-1'), bot('bot-2')])
    const { ensureBot, currentBotId } = makeBots('bot-2')

    await expect(ensureBot()).resolves.toBe('bot-2')
    expect(currentBotId.value).toBe('bot-2')
  })

  it('returns null for a genuinely empty bot list', async () => {
    fetchBotsMock.mockResolvedValue([])
    const { ensureBot, currentBotId } = makeBots('bot-1')

    await expect(ensureBot()).resolves.toBeNull()
    expect(currentBotId.value).toBeNull()
  })
})
