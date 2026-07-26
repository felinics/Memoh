import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import type { BotCreateStreamEvent } from '@/composables/api/useBotCreateStream'

const postBotsStream = vi.fn()
const putBotsByBotIdSettings = vi.fn()

vi.mock('@/composables/api/useBotCreateStream', async (importActual) => {
  const actual = await importActual<typeof import('@/composables/api/useBotCreateStream')>()
  return { ...actual, postBotsStream: (...args: unknown[]) => postBotsStream(...args) }
})

vi.mock('@memohai/sdk', () => ({
  putBotsByBotIdSettings: (...args: unknown[]) => putBotsByBotIdSettings(...args),
}))

const { useBotCreateProgressStore } = await import('./bot-create-progress')

function streamOf(events: BotCreateStreamEvent[]) {
  return {
    stream: (async function* () {
      for (const event of events) yield event
    })(),
  }
}

describe('useBotCreateProgressStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    postBotsStream.mockReset()
    putBotsByBotIdSettings.mockReset()
    putBotsByBotIdSettings.mockResolvedValue({ data: {} })
  })

  it('streams the happy path to a ready state with a terminal log', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'bot_created', bot },
      { type: 'pulling', image: 'img' },
      { type: 'pull_progress', layers: [{ ref: 'a', offset: 100, total: 100 }] },
      { type: 'creating' },
      {
        type: 'complete',
        container: {
          container_id: 'workspace-bot-1',
          workspace_backend: 'container',
          runtime_backend: 'io.containerd.runc.v2',
          started: true,
        },
      },
      { type: 'ready', bot },
    ]))

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })

    expect(store.status).toBe('ready')
    expect(store.bot).toEqual(bot)
    expect(store.lines.map(l => l.kind)).toEqual(['command', 'bot-created', 'pulling', 'creating', 'ready'])
    expect(store.lines.at(-1)).toMatchObject({ kind: 'ready', status: 'done' })
  })

  it('treats a hard failure with no bot as an error status', async () => {
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'pulling', image: 'img' },
      { type: 'error', message: 'image pull failed' },
    ]))

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })

    expect(store.status).toBe('error')
    expect(store.bot).toBeNull()
    expect(store.setupError).toBe('image pull failed')
    expect(store.lines.at(-1)).toMatchObject({ kind: 'error', status: 'error', message: 'image pull failed' })
  })

  it('treats a setup failure after the bot exists as a ready-with-warning state', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'bot_created', bot },
      { type: 'creating' },
      { type: 'error', message: 'container setup failed' },
    ]))

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })

    expect(store.status).toBe('ready')
    expect(store.bot).toEqual(bot)
    expect(store.setupError).toBe('container setup failed')
  })

  it('rethrows-as-error when the stream fails before any bot is created', async () => {
    postBotsStream.mockResolvedValue({
      stream: (async function* (): AsyncGenerator<BotCreateStreamEvent, void, unknown> {
        throw new Error('connection reset')
      })(),
    })

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })

    expect(store.status).toBe('error')
    expect(store.bot).toBeNull()
    expect(store.setupError).toBe('connection reset')
    expect(store.lines.at(-1)).toMatchObject({ kind: 'error', status: 'error' })
  })

  it('keeps the stable code when bot creation returns an HTTP problem', async () => {
    postBotsStream.mockResolvedValue({
      stream: (async function* (): AsyncGenerator<BotCreateStreamEvent, void, unknown> {
        throw {
          code: 'bot.name_taken',
          args: { field: 'name' },
          detail: 'This name is already taken.',
          status: 409,
        }
      })(),
    })

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })

    expect(store.status).toBe('error')
    expect(store.errorCode).toBe('bot.name_taken')
    expect(store.setupError).toBe('This name is already taken.')
  })

  it('recovers the name conflict from a legacy code-less 409 rejection', async () => {
    postBotsStream.mockResolvedValue({
      stream: (async function* (): AsyncGenerator<BotCreateStreamEvent, void, unknown> {
        // Older hosted servers reject with echo's plain body; fetchSSEProblem
        // attaches the HTTP status but there is no stable code to parse.
        throw { message: 'bot name already taken', status: 409 }
      })(),
    })

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })

    expect(store.status).toBe('error')
    expect(store.errorCode).toBe('bot.name_taken')
    expect(store.setupError).toBe('bot name already taken')
  })

  it('applies model and memory settings after the bot is ready', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'bot_created', bot },
      { type: 'ready', bot },
    ]))

    const store = useBotCreateProgressStore()
    await store.start(
      { name: 'ada', display_name: 'Ada' },
      { settings: { chat_model_id: 'm1', memory_provider_id: 'p1' } },
    )

    expect(putBotsByBotIdSettings).toHaveBeenCalledWith(expect.objectContaining({
      path: { bot_id: 'bot-1' },
      body: { chat_model_id: 'm1', memory_provider_id: 'p1' },
    }))
    expect(store.status).toBe('ready')
    expect(store.lines.some(l => l.kind === 'applying-settings' && l.status === 'done')).toBe(true)
  })

  it('keeps the bot ready when settings application fails', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([
      { type: 'bot_created', bot },
      { type: 'ready', bot },
    ]))
    putBotsByBotIdSettings.mockRejectedValue(new Error('settings boom'))

    const store = useBotCreateProgressStore()
    await store.start(
      { name: 'ada', display_name: 'Ada' },
      { settings: { chat_model_id: 'm1' } },
    )

    expect(store.status).toBe('ready')
    expect(store.bot).toEqual(bot)
  })

  it('reset returns the store to idle', async () => {
    const bot = { id: 'bot-1', name: 'ada' }
    postBotsStream.mockResolvedValue(streamOf([{ type: 'ready', bot }]))

    const store = useBotCreateProgressStore()
    await store.start({ name: 'ada', display_name: 'Ada' })
    store.reset()

    expect(store.status).toBe('idle')
    expect(store.lines).toEqual([])
    expect(store.bot).toBeNull()
    expect(store.setupError).toBeNull()
    expect(store.errorCode).toBeNull()
  })
})
