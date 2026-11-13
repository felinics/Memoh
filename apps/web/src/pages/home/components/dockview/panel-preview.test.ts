// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import { createApp, nextTick, ref } from 'vue'
import PanelPreview from './panel-preview.vue'
import { useChatStore } from '@/store/chat-list'

const sdk = vi.hoisted(() => ({
  getBots: vi.fn(),
  getBotsByBotIdSessions: vi.fn(),
  getBotsByBotIdContainerFs: vi.fn(),
  getBotsByBotIdContainerFsRead: vi.fn(),
}))

vi.mock('@memohai/sdk', async (importOriginal) => {
  const original = await importOriginal<typeof import('@memohai/sdk')>()
  return { ...original, ...sdk }
})
vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({
    t: (key: string) => key,
    locale: ref('en'),
    availableLocales: ['en'],
    setLocaleMessage: vi.fn(),
  }),
}))
vi.mock('@/i18n', () => ({ default: { global: { t: (key: string) => key } } }))
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
  await vi.dynamicImportSettled()
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
    vi.setSystemTime(new Date('2026-05-01T12:00:10.000Z'))
    pinia = createPinia()
    setActivePinia(pinia)
    const chatStore = useChatStore()
    chatStore.currentBotId = 'bot-1'
    sdk.getBots.mockResolvedValue({ data: { items: [{ id: 'bot-1', status: 'active', name: 'Bot' }] } })
    sdk.getBotsByBotIdSessions.mockResolvedValue({ data: { items: [] } })
    sdk.getBotsByBotIdContainerFs.mockReset()
    sdk.getBotsByBotIdContainerFsRead.mockReset()
    root = document.createElement('div')
    document.body.append(root)
  })

  afterEach(() => {
    app?.unmount()
    root.remove()
    vi.useRealTimers()
  })

  it('gates visible polling on FSStat and reads full content only when metadata changes', async () => {
    sdk.getBotsByBotIdContainerFsRead
      .mockResolvedValueOnce({ data: { content: 'first', revision: 'rev-1', size: 5 } })
      .mockResolvedValueOnce({ data: { content: 'second', revision: 'rev-2', size: 6 } })
    sdk.getBotsByBotIdContainerFs
      .mockResolvedValueOnce({
        data: { isDir: false, size: 5, mode: '-rw-r--r--', modTime: '2026-05-01T12:00:05Z' },
      })
      .mockResolvedValueOnce({
        data: { isDir: false, size: 6, mode: '-rw-r--r--', modTime: '2026-05-01T12:00:13Z' },
      })

    app = createApp(PanelPreview, {
      params: {
        params: { filePath: '/data/readme.md' },
        api: {},
        containerApi: {},
      },
    })
    app.use(pinia).use(PiniaColada)
    app.mount(root)
    await flush()

    expect(sdk.getBotsByBotIdContainerFsRead).toHaveBeenCalledTimes(1)
    expect(root.querySelector('article')?.textContent).toBe('first')

    vi.advanceTimersByTime(2_000)
    await flush()

    expect(sdk.getBotsByBotIdContainerFs).toHaveBeenCalledTimes(1)
    expect(sdk.getBotsByBotIdContainerFsRead).toHaveBeenCalledTimes(1)

    vi.advanceTimersByTime(2_000)
    await flush()

    expect(sdk.getBotsByBotIdContainerFs).toHaveBeenCalledTimes(2)
    expect(sdk.getBotsByBotIdContainerFsRead).toHaveBeenCalledTimes(2)
    expect(root.querySelector('article')?.textContent).toBe('second')
    expect(sdk.getBotsByBotIdContainerFsRead).toHaveBeenLastCalledWith(expect.objectContaining({
      path: { bot_id: 'bot-1' },
      query: { path: '/data/readme.md' },
    }))
  })

  it('surfaces deleted file state, pauses 2s polling while missing, and recovers on manual retry', async () => {
    const notFoundError = Object.assign(new Error('Not Found'), { status: 404, code: 'workspace.file_not_found' })
    sdk.getBotsByBotIdContainerFsRead
      .mockResolvedValueOnce({ data: { content: 'initial', revision: 'rev-1', size: 7 } })
      .mockRejectedValueOnce(notFoundError)
      .mockResolvedValueOnce({ data: { content: 'restored', revision: 'rev-2', size: 8 } })
    sdk.getBotsByBotIdContainerFs.mockRejectedValueOnce(notFoundError)

    app = createApp(PanelPreview, {
      params: {
        params: { filePath: '/data/readme.md' },
        api: {},
        containerApi: {},
      },
    })
    app.use(pinia).use(PiniaColada)
    app.mount(root)
    await flush()

    expect(root.querySelector('article')?.textContent).toBe('initial')

    vi.advanceTimersByTime(2_000)
    await flush()

    expect(root.textContent).toContain('bots.files.externalChange.deleted')
    expect(sdk.getBotsByBotIdContainerFs).toHaveBeenCalledTimes(1)

    // While missing, background 2s polling is paused until Retry or fsChangedAt.
    vi.advanceTimersByTime(6_000)
    await flush()
    expect(sdk.getBotsByBotIdContainerFs).toHaveBeenCalledTimes(1)

    const retryButton = root.querySelector('button')
    expect(retryButton?.textContent).toContain('common.retry')
    retryButton?.click()
    await flush()

    expect(root.querySelector('article')?.textContent).toBe('restored')
  })
})
