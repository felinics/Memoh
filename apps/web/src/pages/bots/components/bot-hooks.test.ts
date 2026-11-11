// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createPinia, type Pinia } from 'pinia'
import { PiniaColada, useQueryCache } from '@pinia/colada'
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
let pinia: Pinia
afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
  vi.clearAllMocks()
})

async function mountPage(): Promise<HTMLElement> {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(BotHooks, { botId: 'bot-1' })
  pinia = createPinia()
  app.use(pinia).use(PiniaColada)
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

  // 用例 A
  it('shows a load failure instead of a starter template when the config cannot be read', async () => {
    api.events.mockResolvedValue({ data: { events: [] } })
    api.read.mockRejectedValue({ status: 500 })
    const root = await mountPage()
    expect(api.write).not.toHaveBeenCalled()
    const textareas = [...root.querySelectorAll('textarea')]
    expect(textareas.some(el => el.value.includes('log tool results'))).toBe(false)
    expect(root.textContent).toContain('bots.hooks.loadFailed')
  })

  // 用例 B
  it('keeps an unsaved edit when the config is refetched in the background', async () => {
    api.events.mockResolvedValue({ data: { events: [] } })
    api.read.mockResolvedValue({ data: { content: '{"hooks":[]}' } })
    const root = await mountPage()
    const editor = root.querySelector('textarea')!
    expect(editor.value).toBe('{"hooks":[]}')
    editor.value = '{"hooks":[],"enabled":false}'
    editor.dispatchEvent(new Event('input'))
    await nextTick()
    expect(root.textContent).toContain('common.unsaved')

    const reads = api.read.mock.calls.length
    await useQueryCache(pinia).invalidateQueries({ key: ['bot-hooks-config', 'bot-1'] })
    await vi.waitFor(() => expect(api.read.mock.calls.length).toBeGreaterThan(reads))
    await new Promise(resolve => setTimeout(resolve, 0))
    await nextTick()

    expect(root.querySelector('textarea')!.value).toBe('{"hooks":[],"enabled":false}')
  })

  // 用例 C
  it('creates an empty config when the file is missing', async () => {
    api.events.mockResolvedValue({ data: { events: [] } })
    api.read.mockRejectedValue({ status: 404 })
    api.write.mockResolvedValue({ data: {} })
    const root = await mountPage()
    await vi.waitFor(() => expect(api.write).toHaveBeenCalledOnce())
    await new Promise(resolve => setTimeout(resolve, 0))
    await nextTick()
    expect(root.querySelector('textarea')!.value).toContain('"hooks": []')
    expect(root.textContent).not.toContain('bots.hooks.loadFailed')
  })

  // 用例 D
  it('reload drops the draft and save keeps the editor clean', async () => {
    api.events.mockResolvedValue({ data: { events: [] } })
    api.read.mockResolvedValue({ data: { content: '{"hooks":[]}' } })
    api.write.mockResolvedValue({ data: {} })
    const root = await mountPage()
    const editor = root.querySelector('textarea')!
    const buttons = () => [...root.querySelectorAll('button')]

    editor.value = '{"hooks":[],"enabled":false}'
    editor.dispatchEvent(new Event('input'))
    await nextTick()
    buttons().find(b => b.textContent?.includes('bots.hooks.save'))!.click()
    await vi.waitFor(() => expect(api.write).toHaveBeenCalledOnce())
    await new Promise(resolve => setTimeout(resolve, 0))
    await nextTick()
    expect(root.textContent).not.toContain('common.unsaved')

    editor.value = '{"hooks":[],"enabled":true}'
    editor.dispatchEvent(new Event('input'))
    await nextTick()
    expect(root.textContent).toContain('common.unsaved')
    buttons().find(b => b.textContent?.includes('bots.hooks.reload'))!.click()
    await new Promise(resolve => setTimeout(resolve, 10))
    await nextTick()
    expect(root.querySelector('textarea')!.value).toBe('{"hooks":[]}')
    expect(root.textContent).not.toContain('common.unsaved')
  })
})
