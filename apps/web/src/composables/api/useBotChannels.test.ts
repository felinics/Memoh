// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref, type Component } from 'vue'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import type { HandlersChannelMeta } from '@memohai/sdk'

const mocks = vi.hoisted(() => ({
  getChannels: vi.fn(),
  getBotsByIdChannels: vi.fn(),
}))

vi.mock('@memohai/sdk', () => ({
  getChannels: mocks.getChannels,
  getBotsByIdChannels: mocks.getBotsByIdChannels,
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
  it('merges batch channel configs and filters out configless channels', async () => {
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
    ['404 not_found', { status: 404, message: 'Not Found' }],
  ])('propagates %s from the batch endpoint instead of reporting unconfigured', async (_label, problem) => {
    mocks.getChannels.mockResolvedValue({ data: channelMetas })
    mocks.getBotsByIdChannels.mockRejectedValue(problem)

    const { result } = mountUseBotChannels()
    await flushMicrotasks()

    expect(result.data.value).toBeUndefined()
    expect(result.error.value).toEqual(problem)
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
