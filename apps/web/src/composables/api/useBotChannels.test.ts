// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref, type Component } from 'vue'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import type { HandlersChannelMeta } from '@memohai/sdk'

const mocks = vi.hoisted(() => ({
  getChannels: vi.fn(),
  getBotsByIdChannels: vi.fn(),
  getBotsByIdChannelByPlatform: vi.fn(),
}))

vi.mock('@memohai/sdk', () => ({
  getChannels: mocks.getChannels,
  getBotsByIdChannels: mocks.getBotsByIdChannels,
  getBotsByIdChannelByPlatform: mocks.getBotsByIdChannelByPlatform,
}))

import { useBotChannels } from './useBotChannels'

const channelMetas: HandlersChannelMeta[] = [
  { type: 'local', display_name: 'Local', configless: true },
  { type: 'telegram', display_name: 'Telegram', configless: false },
  { type: 'discord', display_name: 'Discord', configless: false },
]

let unmount = () => {}

afterEach(() => {
  unmount()
  unmount = () => {}
  vi.clearAllMocks()
})

async function flushMicrotasks(rounds = 6) {
  for (let i = 0; i < rounds; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

function mountUseBotChannels(enabled?: () => boolean) {
  const botId = ref('bot-1')
  let result!: ReturnType<typeof useBotChannels>

  const Host: Component = {
    setup() {
      result = useBotChannels(botId, enabled ? { enabled } : undefined)
      return () => h('div')
    },
  }

  const app = createApp(Host)
  app.use(createPinia())
  app.use(PiniaColada)
  const el = document.createElement('div')
  app.mount(el)
  unmount = () => app.unmount()

  return { botId, result }
}

describe('useBotChannels', () => {
  it('uses the single batch endpoint and skips per-platform requests', async () => {
    mocks.getChannels.mockResolvedValue({ data: channelMetas })
    mocks.getBotsByIdChannels.mockResolvedValue({
      data: {
        items: [
          { id: 'cfg-1', bot_id: 'bot-1', channel_type: 'telegram', disabled: false },
        ],
      },
    })

    const { result } = mountUseBotChannels()
    await flushMicrotasks()

    expect(mocks.getBotsByIdChannels).toHaveBeenCalledTimes(1)
    expect(mocks.getBotsByIdChannelByPlatform).not.toHaveBeenCalled()
    expect(result.error.value).toBeNull()
    expect(result.data.value).toEqual([
      {
        meta: channelMetas[1],
        config: { id: 'cfg-1', bot_id: 'bot-1', channel_type: 'telegram', disabled: false },
        configured: true,
      },
      {
        meta: channelMetas[2],
        config: null,
        configured: false,
      },
    ])
  })

  it.each([
    ['403 forbidden', { status: 403, code: 'http.forbidden' }],
    ['500 internal error', { status: 500, code: 'internal' }],
    ['404 bot.not_found', { status: 404, code: 'bot.not_found' }],
  ])('propagates %s from the batch endpoint instead of reporting unconfigured', async (_label, problem) => {
    mocks.getChannels.mockResolvedValue({ data: channelMetas })
    mocks.getBotsByIdChannels.mockRejectedValue(problem)

    const { result } = mountUseBotChannels()
    await flushMicrotasks()

    expect(mocks.getBotsByIdChannelByPlatform).not.toHaveBeenCalled()
    expect(result.data.value).toBeUndefined()
    expect(result.error.value).toEqual(problem)
  })

  it('falls back to per-platform probes on older servers missing the batch route (generic 404 or HTML fallback)', async () => {
    for (const batchOutcome of [
      () => mocks.getBotsByIdChannels.mockRejectedValueOnce({ status: 404, detail: 'Not Found' }),
      () => mocks.getBotsByIdChannels.mockResolvedValueOnce({ data: '<!doctype html><html></html>' }),
    ]) {
      vi.clearAllMocks()
      mocks.getChannels.mockResolvedValueOnce({ data: channelMetas })
      batchOutcome()
      mocks.getBotsByIdChannelByPlatform.mockImplementation(async ({ path }: { path: { platform: string } }) => {
        if (path.platform === 'telegram') {
          return { data: { id: 'cfg-1', bot_id: 'bot-1', channel_type: 'telegram', disabled: true } }
        }
        throw { status: 404, code: 'channel.config_not_found' }
      })

      const { result } = mountUseBotChannels()
      await flushMicrotasks()

      expect(mocks.getBotsByIdChannelByPlatform).toHaveBeenCalledTimes(2)
      expect(result.error.value).toBeNull()
      expect(result.data.value).toEqual([
        {
          meta: channelMetas[1],
          config: { id: 'cfg-1', bot_id: 'bot-1', channel_type: 'telegram', disabled: true },
          configured: true,
        },
        {
          meta: channelMetas[2],
          config: null,
          configured: false,
        },
      ])
      unmount()
    }
  })

  it('rethrows non-404 errors during legacy per-platform fallback instead of returning configured: false', async () => {
    mocks.getChannels.mockResolvedValue({ data: channelMetas })
    mocks.getBotsByIdChannels.mockRejectedValue({ status: 404 })
    mocks.getBotsByIdChannelByPlatform.mockImplementation(async ({ path }: { path: { platform: string } }) => {
      if (path.platform === 'telegram') {
        throw { status: 404, code: 'channel.config_not_found' }
      }
      throw { status: 500, code: 'internal' }
    })

    const { result } = mountUseBotChannels()
    await flushMicrotasks()

    expect(result.data.value).toBeUndefined()
    expect(result.error.value).toEqual({ status: 500, code: 'internal' })
  })

  it('respects options.enabled when caller lacks manage access', async () => {
    const canManage = ref(false)
    mountUseBotChannels(() => canManage.value)
    await flushMicrotasks()

    expect(mocks.getChannels).not.toHaveBeenCalled()
    expect(mocks.getBotsByIdChannels).not.toHaveBeenCalled()

    canManage.value = true
    mocks.getChannels.mockResolvedValue({ data: [] })
    mocks.getBotsByIdChannels.mockResolvedValue({ data: { items: [] } })
    await flushMicrotasks()

    expect(mocks.getChannels).toHaveBeenCalledTimes(1)
    expect(mocks.getBotsByIdChannels).toHaveBeenCalledTimes(1)
  })
})
