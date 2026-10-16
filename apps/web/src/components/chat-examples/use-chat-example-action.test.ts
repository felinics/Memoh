import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ChatExample } from '@/lib/chat-examples/types'

const mocks = vi.hoisted(() => ({
  push: vi.fn(),
  prefillComposer: vi.fn(),
  queries: null as null | {
    examples: { value: unknown[] }
    capabilities: { value: Record<string, boolean> | undefined }
    status: { value: 'pending' | 'success' | 'error' }
  },
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mocks.push }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ locale: { value: 'zh-CN' } }),
}))

vi.mock('@/store/chat-list', () => ({
  useChatStore: () => ({ bots: [{ id: 'bot-a', name: 'alpha' }] }),
}))

vi.mock('@/composables/useComposerPrefill', () => ({
  useComposerPrefill: () => ({ prefillComposer: mocks.prefillComposer }),
}))

vi.mock('@/composables/api/useChatExamples', () => ({
  useChatExamplesQuery: () => ({ data: mocks.queries!.examples }),
  useBotCapabilitiesQuery: () => ({ data: mocks.queries!.capabilities, status: mocks.queries!.status }),
}))

const { ref } = await import('vue')
const { useChatExampleAction, useChatExampleEntries } = await import('./use-chat-example-action')

const example: ChatExample = {
  id: 'news',
  category: 'research',
  icon: 'newspaper',
  priority: 0,
  title: { en: 'News' },
  prompt: { en: 'Read the news', zh: '读新闻' },
  requires: ['browser', 'channel'],
  surfaces: ['welcome'],
}

describe('useChatExampleAction', () => {
  beforeEach(() => vi.clearAllMocks())

  it('prefills the localized prompt for the bot when nothing is missing', async () => {
    await useChatExampleAction(() => 'bot-a').run(example, [])
    expect(mocks.prefillComposer).toHaveBeenCalledWith('读新闻', { botId: 'bot-a' })
    expect(mocks.push).not.toHaveBeenCalled()
  })

  it('opens the bot settings tab that enables the first missing capability, by bot name', async () => {
    await useChatExampleAction(() => 'bot-a').run(example, ['browser'])
    expect(mocks.push).toHaveBeenCalledWith({ name: 'bot-detail', params: { botName: 'alpha' }, query: { tab: 'advanced' } })
    expect(mocks.prefillComposer).not.toHaveBeenCalled()
  })

  it('prefills without a bot so prefillComposer can pick one or report there is none', async () => {
    await useChatExampleAction(() => '').run(example, ['browser'])
    expect(mocks.prefillComposer).toHaveBeenCalledWith('读新闻', {})
  })
})

describe('useChatExampleEntries', () => {
  function setup(status: 'pending' | 'success') {
    mocks.queries = {
      examples: ref([example, { ...example, id: 'free', requires: [], priority: -1 }]),
      capabilities: ref(status === 'success' ? { browser: false } : undefined),
      status: ref(status),
    }
  }

  it('shows nothing until the bot capabilities are known, so cards never re-sort under the pointer', () => {
    setup('pending')
    expect(useChatExampleEntries(() => 'bot-a', { surface: 'welcome' }).value).toBeNull()
  })

  it('lists examples with their missing capabilities once known', () => {
    setup('success')
    const entries = useChatExampleEntries(() => 'bot-a', { surface: 'welcome' }).value ?? []
    expect(entries.map(entry => [entry.example.id, entry.missing])).toEqual([['free', []], ['news', ['browser']]])
  })

  it('does not wait without a bot', () => {
    setup('pending')
    expect(useChatExampleEntries(() => '', { surface: 'welcome' }).value).toHaveLength(2)
  })

  it('can be empty once settled, e.g. a category with no examples', () => {
    setup('success')
    expect(useChatExampleEntries(() => 'bot-a', { surface: 'gallery' }).value).toEqual([])
  })
})
