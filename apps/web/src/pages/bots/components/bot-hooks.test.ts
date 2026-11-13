// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref, type App, type Ref } from 'vue'
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

function mountRaw(botIdRef: Ref<string> = ref('bot-1')): HTMLElement {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp({ render: () => h(BotHooks, { botId: botIdRef.value }) })
  pinia = createPinia()
  app.use(pinia).use(PiniaColada)
  app.config.globalProperties.$t = (key: string) => key
  app.mount(root)
  return root
}

async function mountPage(): Promise<HTMLElement> {
  const root = mountRaw()
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

  it('does not flash invalid JSON or allow pre-hydration edits while initial config is loading', async () => {
    api.events.mockResolvedValue({ data: { events: [] } })
    let resolveRead!: (value: { data: { content: string } }) => void
    api.read.mockReturnValue(new Promise((resolve) => {
      resolveRead = resolve
    }))

    const root = mountRaw()
    await nextTick()
    await vi.waitFor(() => expect(api.read).toHaveBeenCalledOnce())

    const editor = root.querySelector('textarea')!
    const buttons = () => [...root.querySelectorAll('button')]
    const templateBtn = buttons().find(b => b.textContent?.includes('bots.hooks.template'))!

    expect(root.textContent).not.toContain('bots.hooks.invalidJson')
    expect(root.textContent).not.toContain('bots.hooks.missing')
    expect(root.textContent).not.toContain('bots.hooks.missingDescription')
    expect(editor.getAttribute('aria-invalid')).not.toBe('true')
    expect(editor.disabled).toBe(true)
    expect(templateBtn.disabled).toBe(true)

    // Pre-hydration template click or input event must not mark the draft dirty.
    templateBtn.click()
    editor.value = '{"hooks":[{"name":"premature"}]}'
    editor.dispatchEvent(new Event('input'))
    await nextTick()
    expect(root.textContent).not.toContain('common.unsaved')

    resolveRead({ data: { content: '{"hooks":[{"name":"server-hook"}]}' } })
    await new Promise(resolve => setTimeout(resolve, 0))
    await nextTick()

    expect(editor.disabled).toBe(false)
    expect(templateBtn.disabled).toBe(false)
    expect(editor.value).toBe('{"hooks":[{"name":"server-hook"}]}')
    expect(root.textContent).not.toContain('common.unsaved')
  })

  it('keeps save scoped to the originating bot when switching bots mid-flight', async () => {
    api.events.mockResolvedValue({ data: { events: [] } })
    api.read.mockImplementation(async ({ path }: { path: { bot_id: string } }) => ({
      data: {
        content: path.bot_id === 'bot-1'
          ? '{"hooks":[]}'
          : '{"hooks":[{"name":"bot-2-only"}]}',
      },
    }))

    let resolveWrite!: (value: { data: Record<string, never> }) => void
    api.write.mockImplementation(() => new Promise((resolve) => {
      resolveWrite = resolve
    }))

    const botId = ref('bot-1')
    const root = mountRaw(botId)
    await vi.waitFor(() => expect(api.read).toHaveBeenCalled())
    await new Promise(resolve => setTimeout(resolve, 0))
    await nextTick()

    const editor = root.querySelector('textarea')!
    expect(editor.value).toBe('{"hooks":[]}')

    editor.value = '{"hooks":[],"enabled":false}'
    editor.dispatchEvent(new Event('input'))
    await nextTick()

    const saveBtn = [...root.querySelectorAll('button')].find(b => b.textContent?.includes('bots.hooks.save'))!
    saveBtn.click()
    await vi.waitFor(() => expect(api.write).toHaveBeenCalledOnce())
    expect(api.write).toHaveBeenCalledWith(expect.objectContaining({
      path: { bot_id: 'bot-1' },
    }))

    // Switch to bot-2 before bot-1's save resolves.
    botId.value = 'bot-2'
    await nextTick()
    await vi.waitFor(() => expect(root.querySelector('textarea')!.value).toBe('{"hooks":[{"name":"bot-2-only"}]}'))

    resolveWrite({ data: {} })
    await new Promise(resolve => setTimeout(resolve, 0))
    await nextTick()

    // bot-2's editor and cache must stay untouched; bot-1's cache gets the saved config.
    expect(root.querySelector('textarea')!.value).toBe('{"hooks":[{"name":"bot-2-only"}]}')
    expect(root.textContent).not.toContain('common.unsaved')
    const cache = useQueryCache(pinia)
    expect(cache.getQueryData(['bot-hooks-config', 'bot-1'])).toBe(JSON.stringify({ hooks: [], enabled: false }, null, 2))
    expect(cache.getQueryData(['bot-hooks-config', 'bot-2'])).toBe('{"hooks":[{"name":"bot-2-only"}]}')
  })
})
