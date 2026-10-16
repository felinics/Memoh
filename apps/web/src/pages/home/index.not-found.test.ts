// @vitest-environment jsdom
import { createApp, defineComponent, h, nextTick, reactive, ref, type App, type Slots } from 'vue'
import { createPinia, defineStore } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Home from './index.vue'

const sdk = vi.hoisted(() => ({ getBot: vi.fn() }))
const router = vi.hoisted(() => ({ replace: vi.fn(async () => {}) }))
const route = vi.hoisted(() => ({ value: null as null | { name: string, params: Record<string, string>, query: Record<string, string> } }))
const chat = vi.hoisted(() => ({
  selectBot: vi.fn(),
  refreshBots: vi.fn(async () => {}),
  initializeWithRecovery: vi.fn(async () => {}),
}))

vi.mock('@memohai/sdk', () => ({ getBotsById: sdk.getBot, getBotsByBotIdAgents: vi.fn() }))
vi.mock('vue-router', () => ({ useRoute: () => route.value, useRouter: () => router }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@felinic/ui', async () => {
  const { h } = await import('vue')
  return {
    PanePlaceholder: (props: { title?: string }, { slots }: { slots: Slots }) =>
      h('div', [props.title, slots.default?.()]),
  }
})
vi.mock('@/store/chat-list', () => ({
  useChatStore: defineStore('chat', () => {
    const currentBotId = ref<string | null>(null)
    const bots = ref<Array<{ id: string, name: string }>>([])
    const activeSession = ref(null)
    const selectBot = async (id: string) => {
      chat.selectBot(id)
      currentBotId.value = id
    }
    return {
      currentBotId, bots, activeSession, selectBot,
      refreshBots: chat.refreshBots,
      initializeWithRecovery: chat.initializeWithRecovery,
    }
  }),
}))
vi.mock('@/store/workspace-tabs', () => ({ useWorkspaceTabsStore: () => ({ openSessionChat: vi.fn() }) }))
vi.mock('./components/chat-workspace.vue', async () => {
  const { h } = await import('vue')
  return { default: () => h('div', 'chat-workspace') }
})

const existingBot = { id: 'bot-1', name: 'errs-bot' }
const previousBot = { id: 'bot-0', name: 'previous-bot' }

let app: App | undefined
let host: HTMLDivElement | undefined

async function flush() {
  for (let i = 0; i < 6; i += 1) {
    await new Promise(resolve => setTimeout(resolve, 0))
    await nextTick()
  }
}

// Mounts the chat home on /bot/<botName> with `previousBot` already selected,
// the state a user is in when they paste a link into an open tab.
async function mountHome(botName: string) {
  route.value = reactive({ name: 'bot', params: { botName }, query: {} })
  const pinia = createPinia()
  host = document.createElement('div')
  document.body.append(host)
  app = createApp(defineComponent({ setup: () => () => h(Home) }))
  app.use(pinia)
  const { useChatStore } = await import('@/store/chat-list')
  const store = useChatStore(pinia)
  store.currentBotId = previousBot.id
  store.bots = [previousBot]
  app.mount(host)
  await flush()
  return { el: host, store }
}

beforeEach(() => {
  sdk.getBot.mockReset()
  router.replace.mockClear()
  chat.selectBot.mockClear()
  chat.refreshBots.mockClear()
})
afterEach(() => {
  app?.unmount()
  host?.remove()
  app = undefined
  host = undefined
})

describe('chat home opened on /bot/<name>', () => {
  it('shows not-found for an unknown bot and does not fall back to the previous one', async () => {
    // Legacy 404: the body carries only a message, the status is on the response.
    sdk.getBot.mockResolvedValue({ data: undefined, error: { message: 'bot not found' }, response: { status: 404 } })

    const { el, store } = await mountHome('no-such-bot')

    expect(el.textContent).toContain('bots.notFound')
    expect(el.textContent).not.toContain('chat-workspace')
    expect(store.currentBotId).toBeNull()
    expect(router.replace).not.toHaveBeenCalled()
    expect(chat.selectBot).not.toHaveBeenCalled()
    expect(chat.refreshBots).toHaveBeenCalled()
  })

  it('keeps the current selection when the lookup fails for another reason', async () => {
    sdk.getBot.mockResolvedValue({ data: undefined, error: { message: 'upstream down' }, response: { status: 503 } })

    const { el, store } = await mountHome('errs-bot')

    expect(el.textContent).not.toContain('bots.notFound')
    expect(store.currentBotId).toBe(previousBot.id)
  })

  it('selects an existing bot', async () => {
    sdk.getBot.mockResolvedValue({ data: existingBot, error: undefined, response: { status: 200 } })

    const { el, store } = await mountHome('errs-bot')

    expect(chat.selectBot).toHaveBeenCalledWith(existingBot.id)
    expect(store.currentBotId).toBe(existingBot.id)
    expect(el.textContent).toContain('chat-workspace')
    expect(el.textContent).not.toContain('bots.notFound')
  })
})
