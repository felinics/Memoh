import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  push: vi.fn(),
  toastInfo: vi.fn(),
  route: { name: 'home' as string },
  chat: {
    bots: [] as Array<{ id: string }>,
    currentBotId: null as string | null,
    refreshBots: vi.fn(),
  },
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mocks.push, currentRoute: { value: mocks.route } }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('@felinic/ui', () => ({
  toast: { info: mocks.toastInfo },
}))

vi.mock('@/store/chat-list', () => ({
  useChatStore: () => mocks.chat,
}))

const { useComposerPrefill } = await import('./useComposerPrefill')
const { useComposerPrefillStore } = await import('@/store/composer-prefill')

describe('prefillComposer', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    mocks.route.name = 'home'
    mocks.chat.bots = [{ id: 'bot-a' }, { id: 'bot-b' }]
    mocks.chat.currentBotId = 'bot-a'
    mocks.chat.refreshBots.mockResolvedValue(undefined)
  })

  /** Stand in for the chat pane: wait for the request, then settle it. */
  async function consumeAs(outcome: 'applied' | 'cancelled') {
    await vi.waitFor(() => expect(useComposerPrefillStore().pending).not.toBeNull())
    const request = useComposerPrefillStore().take(useComposerPrefillStore().pending!.botId)!
    request.settle(outcome)
    return request
  }

  it('resolves with what the chat pane did with the text', async () => {
    const result = useComposerPrefill().prefillComposer('hello')
    const request = await consumeAs('applied')
    expect(request).toMatchObject({ botId: 'bot-a', text: 'hello' })
    await expect(result).resolves.toBe('applied')
    expect(mocks.push).not.toHaveBeenCalled()
  })

  it('reports a declined replacement as cancelled', async () => {
    const result = useComposerPrefill().prefillComposer('hello')
    await consumeAs('cancelled')
    await expect(result).resolves.toBe('cancelled')
  })

  it('falls back to the first bot when none is selected', async () => {
    mocks.chat.currentBotId = null
    void useComposerPrefill().prefillComposer('hello')
    expect((await consumeAs('applied')).botId).toBe('bot-a')
  })

  it('opens the chat page when called from elsewhere', async () => {
    mocks.route.name = 'bot-detail'
    void useComposerPrefill().prefillComposer('hello')
    await consumeAs('applied')
    expect(mocks.push).toHaveBeenCalledWith({ name: 'bot', params: { botName: 'bot-a' } })
  })

  it('switches to the requested bot', async () => {
    void useComposerPrefill().prefillComposer('hello', { botId: 'bot-b' })
    expect((await consumeAs('applied')).botId).toBe('bot-b')
    expect(mocks.push).toHaveBeenCalledWith({ name: 'bot', params: { botName: 'bot-b' } })
  })

  it('reloads the bot list before deciding there is no bot', async () => {
    mocks.chat.bots = []
    mocks.chat.refreshBots.mockImplementation(async () => { mocks.chat.bots = [{ id: 'bot-c' }] })
    mocks.chat.currentBotId = null
    const result = useComposerPrefill().prefillComposer('hello')
    expect((await consumeAs('applied')).botId).toBe('bot-c')
    expect(mocks.chat.refreshBots).toHaveBeenCalledOnce()
    await expect(result).resolves.toBe('applied')
  })

  it('asks the user to create a bot when there is none', async () => {
    mocks.chat.bots = []
    mocks.chat.currentBotId = null
    const result = await useComposerPrefill().prefillComposer('hello')
    expect(result).toBe('no-bot')
    expect(useComposerPrefillStore().pending).toBeNull()
    expect(mocks.toastInfo).toHaveBeenCalledOnce()
    const [, options] = mocks.toastInfo.mock.calls[0]!
    options.action.onClick()
    expect(mocks.push).toHaveBeenCalledWith({ name: 'bot-new' })
  })
})
