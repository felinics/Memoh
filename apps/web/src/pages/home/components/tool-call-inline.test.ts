// @vitest-environment jsdom
import { createApp, h, nextTick, reactive } from 'vue'
import { createI18n } from 'vue-i18n'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ToolCallBlock } from '@/store/chat-list'
import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import ja from '@/i18n/locales/ja.json'

vi.mock('@/i18n', () => ({
  default: { global: { t: (key: string) => key } },
  i18nRef: (key: string) => ({ value: key }),
}))
import ToolCallInline from './tool-call-inline.vue'

const mounted: ReturnType<typeof createApp>[] = []
afterEach(() => { for (const app of mounted.splice(0)) app.unmount() })
let sequence = 0
function mountTool(name: string, input: Record<string, unknown>, result: unknown, inGroup = false, locale = 'en') {
  const id = ++sequence
  const block = reactive<ToolCallBlock>({
    id, type: 'tool', name, toolName: name, tool_call_id: `call-${id}`,
    toolCallId: `call-${id}`, input, result, running: false, done: true,
  })
  const root = document.createElement('div')
  const app = createApp({ render: () => h(ToolCallInline, { block, inGroup, messageId: `message-${id}` }) })
  app.use(createI18n({ legacy: false, locale, messages: { en, zh, ja } }))
  app.mount(root)
  mounted.push(app)
  return { root, block }
}
async function open(root: HTMLElement) {
  const header = root.querySelector<HTMLElement>('.tool-header-row')
  expect(header).not.toBeNull()
  header!.click()
  await nextTick()
  expect(header!.getAttribute('aria-expanded')).toBe('true')
  return header!
}

const failures = [
  { isError: true, content: [{ type: 'text', text: 'Delivery unavailable' }] },
  { structuredContent: { isError: true, content: [{ type: 'text', text: 'Delivery unavailable' }] } },
  { structuredContent: { isError: true, message: 'Delivery unavailable' } },
]
describe.each([false, true])('tool failure detail (inGroup=%s)', (inGroup) => {
  it.each(failures)('shows the search failure instead of an empty result: %j', async (result) => {
    const { root } = mountTool('web_search', { query: 'review' }, result, inGroup)
    expect(root.textContent).not.toContain('Delivery unavailable')
    const header = await open(root)
    expect(header.classList.contains('text-destructive')).toBe(false)
    expect(header.textContent).not.toContain('Delivery unavailable')
    expect(root.textContent).toContain('Delivery unavailable')
    expect(root.textContent).not.toContain('No results')
    expect(root.querySelector('.text-destructive')?.textContent).toContain('Delivery unavailable')
  })
  it('shows send failure together with the attempted input', async () => {
    const { root } = mountTool('send', { text: 'attempted message' }, failures[0], inGroup)
    await open(root)
    expect(root.textContent).toContain('attempted message')
    expect(root.textContent).toContain('Delivery unavailable')
  })
  it('lets an empty-content write expand when it failed', async () => {
    const { root } = mountTool('write', { path: '/data/empty.txt', content: '' }, failures[0], inGroup)
    await open(root)
    expect(root.textContent).toContain('Delivery unavailable')
  })
})

it.each([['en', 'Tool execution failed.'], ['zh', '工具执行失败。'], ['ja', 'ツールの実行に失敗しました。']])('localizes a failure without diagnostics (%s)', async (locale, text) => {
  const { root } = mountTool('web_search', { query: 'review' }, { isError: true }, false, locale)
  await open(root)
  expect(root.textContent).toContain(text)
})
it('switches an open specialized detail when the streamed result completes', async () => {
  const { root, block } = mountTool('web_search', { query: 'review' }, null)
  block.running = true; block.done = false
  await nextTick()
  await open(root)
  expect(root.textContent).toContain('No results')
  block.result = failures[0]; block.running = false; block.done = true
  await nextTick()
  expect(root.textContent).toContain('Delivery unavailable')
  expect(root.textContent).not.toContain('No results')
})
it('preserves successful search rendering even when its content mentions errors', async () => {
  const { root } = mountTool('web_search', { query: 'errors' }, { isError: false, results: [{ title: 'Error handling guide', url: 'https://example.com/guide' }] })
  await open(root)
  expect(root.querySelector('a')?.textContent).toContain('Error handling guide')
  expect(root.querySelector('.text-destructive')).toBeNull()
})
it.each([1, -1, 127])('keeps native exec exit %s and stderr in the exec detail', async (code) => {
  const { root } = mountTool('exec', { command: 'false' }, { exit_code: code, stdout: 'output', stderr: 'diagnostic' })
  const header = await open(root)
  expect(header.textContent).not.toContain('exit')
  expect(root.textContent).toContain(`exit ${code}`)
  expect(root.textContent).toContain('output')
  expect(root.querySelector('.text-destructive')?.textContent).toContain('diagnostic')
})
it('retains structured diagnostics alongside MCP error text', async () => {
  const { root } = mountTool('exec', { command: 'false' }, { isError: true, content: [{ type: 'text', text: 'Command failed' }], structuredContent: { exit_code: 1, stderr: 'diagnostic' } })
  await open(root)
  expect(root.textContent).toContain('Command failed')
  expect(root.textContent).toContain('"exit_code":1')
  expect(root.textContent).toContain('diagnostic')
})
