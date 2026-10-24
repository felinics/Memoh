// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import SearchProviderTest from './search-provider-test.vue'

const { runSearch } = vi.hoisted(() => ({ runSearch: vi.fn() }))
vi.mock('@memohai/sdk', () => ({ postSearchProvidersByIdTest: runSearch }))
let app: App | undefined
let root: HTMLDivElement
async function flush() {
  for (let i = 0; i < 10; i++) { await Promise.resolve(); await nextTick() }
}
async function mount(providerId?: string) {
  root = document.createElement('div'); document.body.appendChild(root)
  app = createApp(SearchProviderTest, { providerId })
  app.use(createPinia()); app.use(PiniaColada)
  app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: { en: {} } }))
  app.mount(root); await flush()
}
function input(query: string) {
  const field = root.querySelector<HTMLInputElement>('input')!
  field.value = query; field.dispatchEvent(new Event('input', { bubbles: true }))
}
afterEach(() => { app?.unmount(); root?.remove(); runSearch.mockReset() })

it('does not test an unsaved provider', async () => {
  await mount()
  expect(root.querySelector('button')?.disabled).toBe(true)
  expect(root.textContent).toContain('webSearch.test.saveFirst')
  expect(runSearch).not.toHaveBeenCalled()
})

it('tests the saved provider with only the trimmed query and renders upstream text safely', async () => {
  runSearch.mockResolvedValue({ data: { query: 'Memoh', results: [{ title: '<img src=x onerror=alert(1)>', url: 'javascript:alert(1)', description: 'Result' }] } })
  await mount('saved-id'); input('  Memoh  '); await flush()
  root.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await flush()
  await vi.waitFor(() => expect(runSearch).toHaveBeenCalledWith({ path: { id: 'saved-id' }, body: { query: 'Memoh' }, throwOnError: true }))
  expect(root.textContent).toContain('<img src=x onerror=alert(1)>')
  expect(root.querySelector('img')).toBeNull()
  expect(root.querySelector('a[href^="javascript:"]')).toBeNull()
})

it('rejects blank or overlong queries without sending a request', async () => {
  await mount('saved-id')
  for (const [query, message] of [[' ', 'webSearch.test.queryRequired'], ['界'.repeat(129), 'webSearch.test.queryTooLong']] as const) {
    input(query); await flush()
    root.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await flush()
    await vi.waitFor(() => expect(root.textContent).toContain(message))
  }
  expect(runSearch).not.toHaveBeenCalled()
})

it('keeps test failures generic instead of displaying raw provider errors', async () => {
  runSearch.mockRejectedValue(new Error('raw provider secret'))
  await mount('saved-id'); input('Memoh'); await flush()
  root.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await flush()
  await vi.waitFor(() => expect(root.textContent).toContain('webSearch.test.failed'))
  expect(root.textContent).not.toContain('raw provider secret')
})
