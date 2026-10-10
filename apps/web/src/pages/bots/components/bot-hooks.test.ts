// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import BotHooks from './bot-hooks.vue'

const api = vi.hoisted(() => ({ read: vi.fn(), events: vi.fn(), write: vi.fn(), test: vi.fn() }))
vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdContainerFsRead: api.read,
  getBotsByBotIdHooksEvents: api.events,
  postBotsByBotIdContainerFsWrite: api.write,
  postBotsByBotIdHooksTest: api.test,
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })

let app: App | null = null
afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
})

async function mountPage(): Promise<HTMLElement> {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(BotHooks, { botId: 'bot-1' })
  app.use(createPinia()).use(PiniaColada)
  app.config.globalProperties.$t = (key: string) => key
  app.mount(root)
  await vi.waitFor(() => expect(api.read).toHaveBeenCalled())
  await new Promise(resolve => setTimeout(resolve, 0))
  await nextTick()
  return root
}

describe('bot hooks editor', () => {
  it('describes a JSON syntax error with local copy, not the parser message', async () => {
    api.events.mockResolvedValue({ data: { events: [] } })
    api.read.mockResolvedValue({ data: { content: '{ "hooks": [ ' } })
    const root = await mountPage()
    expect(root.textContent).toContain('bots.hooks.invalidJson')
    expect(root.textContent).not.toMatch(/Unexpected|JSON at position|Expected/)
    const hint = [...root.querySelectorAll('p')].find(p => p.textContent?.includes('bots.hooks.invalidJson'))
    expect(hint?.classList.contains('text-destructive')).toBe(true)
    expect(root.querySelector('textarea[aria-invalid="true"]')).not.toBeNull()
  })
})
