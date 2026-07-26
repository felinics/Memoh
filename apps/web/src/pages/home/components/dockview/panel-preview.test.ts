// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { createApp, nextTick, ref } from 'vue'
import PanelPreview from './panel-preview.vue'
import { useChatStore } from '@/store/chat-list'

const sdk = vi.hoisted(() => ({
  getBots: vi.fn(),
  getBotsByBotIdSessions: vi.fn(),
  getBotsByBotIdContainerFsRead: vi.fn(),
}))

vi.mock('@memohai/sdk', async (importOriginal) => {
  const original = await importOriginal<typeof import('@memohai/sdk')>()
  return { ...original, ...sdk }
})
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))
// importOriginal keeps the lifted owner components (PanePlaceholder & co.)
// real — they moved into @felinic/ui, so a total-replacement mock now swallows
// them through the host shims.
vi.mock('@felinic/ui', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  Spinner: { template: '<span />' },
  toast: { error: vi.fn() },
}))
vi.mock('@/components/markdown-preview/index.vue', () => ({
  default: {
    props: ['content'],
    template: '<article>{{ content }}</article>',
  },
}))
vi.mock('@/components/html-preview/index.vue', () => ({
  default: {
    props: ['content'],
    template: '<article>{{ content }}</article>',
  },
}))
vi.mock('./use-panel-visible', () => ({
  usePanelVisible: () => ref(true),
}))

async function flush() {
  await nextTick()
  await Promise.resolve()
  await nextTick()
}

describe('panel-preview refresh', () => {
  let root: HTMLElement
  let app: ReturnType<typeof createApp>
  let pinia: ReturnType<typeof createPinia>

  beforeEach(() => {
    vi.useFakeTimers()
    pinia = createPinia()
    setActivePinia(pinia)
    const chatStore = useChatStore()
    chatStore.currentBotId = 'bot-1'
    sdk.getBots.mockResolvedValue({ data: { items: [{ id: 'bot-1', status: 'active', name: 'Bot' }] } })
    sdk.getBotsByBotIdSessions.mockResolvedValue({ data: { items: [] } })
    sdk.getBotsByBotIdContainerFsRead.mockReset()
    root = document.createElement('div')
    document.body.append(root)
  })

  afterEach(() => {
    app?.unmount()
    root.remove()
    vi.useRealTimers()
  })

  it('polls a visible markdown preview so external file edits refresh without a chat fs bump', async () => {
    sdk.getBotsByBotIdContainerFsRead
      .mockResolvedValueOnce({ data: { content: 'first' } })
      .mockResolvedValueOnce({ data: { content: 'second' } })

    app = createApp(PanelPreview, {
      params: {
        params: { filePath: '/data/readme.md' },
        api: {},
        containerApi: {},
      },
    })
    app.use(pinia)
    app.mount(root)
    await flush()

    expect(sdk.getBotsByBotIdContainerFsRead).toHaveBeenCalledTimes(1)

    vi.advanceTimersByTime(2_000)
    await flush()

    expect(sdk.getBotsByBotIdContainerFsRead).toHaveBeenCalledTimes(2)
    expect(sdk.getBotsByBotIdContainerFsRead).toHaveBeenLastCalledWith(expect.objectContaining({
      path: { bot_id: 'bot-1' },
      query: { path: '/data/readme.md' },
    }))
  })
})
