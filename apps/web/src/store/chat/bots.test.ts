import { afterEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { createChatBots } from './bots'
import { fetchBots } from '@/composables/api/useChat'
import type { Bot } from '@/composables/api/useChat'

vi.mock('@/composables/api/useChat', () => ({
  fetchBots: vi.fn(),
}))

const fetchBotsMock = vi.mocked(fetchBots)

function bot(id: string, status = 'ready'): Bot {
  return { id, status } as Bot
}

function makeBots(initialBotId: string | null = null) {
  const currentBotId = ref<string | null>(initialBotId)
  let generation = 0
  const selectBot = vi.fn(async (id: string) => { currentBotId.value = id })
  const store = createChatBots({
    currentBotId,
    userScopeGeneration: () => generation,
    selectBot,
  })
  return {
    currentBotId,
    selectBot,
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

  it('does not select a bot that is being deleted', async () => {
    fetchBotsMock.mockResolvedValue([bot('bot-1', 'deleting')])
    const { ensureBot, currentBotId } = makeBots('bot-1')

    await expect(ensureBot()).resolves.toBeNull()
    expect(currentBotId.value).toBeNull()
  })

  it('returns null for a genuinely empty bot list', async () => {
    fetchBotsMock.mockResolvedValue([])
    const { ensureBot, currentBotId } = makeBots('bot-1')

    await expect(ensureBot()).resolves.toBeNull()
    expect(currentBotId.value).toBeNull()
  })
})

describe('chat bots refreshBots', () => {
  afterEach(() => {
    fetchBotsMock.mockReset()
  })

  it('falls back to another ready bot when the current bot was deleted', async () => {
    fetchBotsMock.mockResolvedValue([bot('bot-1')])
    const { refreshBots, currentBotId, selectBot } = makeBots('bot-2')

    await refreshBots()

    expect(selectBot).toHaveBeenCalledWith('bot-1')
    expect(currentBotId.value).toBe('bot-1')
  })

  it('falls back while the current bot is still being deleted server-side', async () => {
    fetchBotsMock.mockResolvedValue([bot('bot-1'), bot('bot-2', 'deleting')])
    const { refreshBots, currentBotId, selectBot } = makeBots('bot-2')

    await refreshBots()

    expect(selectBot).toHaveBeenCalledWith('bot-1')
    expect(currentBotId.value).toBe('bot-1')
  })

  it('never falls back onto another bot that is being deleted', async () => {
    fetchBotsMock.mockResolvedValue([bot('bot-1', 'deleting'), bot('bot-3', 'creating'), bot('bot-2', 'deleting')])
    const { refreshBots, selectBot } = makeBots('bot-2')

    await refreshBots()

    expect(selectBot).toHaveBeenCalledWith('bot-3')
  })

  it('clears the selection when no usable bot remains', async () => {
    fetchBotsMock.mockResolvedValue([bot('bot-2', 'deleting')])
    const { refreshBots, currentBotId, selectBot } = makeBots('bot-2')

    await refreshBots()

    expect(selectBot).not.toHaveBeenCalled()
    expect(currentBotId.value).toBeNull()
  })

  it('keeps the selection when a different bot was deleted', async () => {
    fetchBotsMock.mockResolvedValue([bot('bot-1'), bot('bot-3', 'deleting')])
    const { refreshBots, currentBotId, selectBot } = makeBots('bot-1')

    await refreshBots()

    expect(selectBot).not.toHaveBeenCalled()
    expect(currentBotId.value).toBe('bot-1')
  })

  it('keeps a current bot that is still being created', async () => {
    fetchBotsMock.mockResolvedValue([bot('bot-1'), bot('bot-2', 'creating')])
    const { refreshBots, currentBotId, selectBot } = makeBots('bot-2')

    await refreshBots()

    expect(selectBot).not.toHaveBeenCalled()
    expect(currentBotId.value).toBe('bot-2')
  })

  it('ignores a stale list fetched before the user scope changed', async () => {
    let resolveFetch!: (list: Bot[]) => void
    fetchBotsMock.mockImplementation(() => new Promise<Bot[]>((resolve) => { resolveFetch = resolve }))
    const { refreshBots, currentBotId, selectBot, bumpGeneration } = makeBots('bot-2')

    const pending = refreshBots()
    bumpGeneration()
    resolveFetch([])
    await pending

    expect(selectBot).not.toHaveBeenCalled()
    expect(currentBotId.value).toBe('bot-2')
  })
})
