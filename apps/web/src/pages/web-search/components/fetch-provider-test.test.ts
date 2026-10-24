// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import FetchProviderTest from './fetch-provider-test.vue'

const { testFetch } = vi.hoisted(() => ({ testFetch: vi.fn() }))
vi.mock('@memohai/sdk', () => ({ postFetchProvidersByIdTest: testFetch }))

let app: App | undefined
let root: HTMLDivElement

async function flush() {
  for (let i = 0; i < 8; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

function mount(id?: string) {
  root = document.createElement('div')
  document.body.appendChild(root)
  app = createApp(FetchProviderTest, { providerId: id })
  app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: { en: {} } }))
  app.mount(root)
}

afterEach(() => {
  app?.unmount()
  root?.remove()
  testFetch.mockReset()
})

it('requires a saved provider before a test can run', async () => {
  mount()
  await flush()
  expect(root.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(true)
  expect(root.textContent).toContain('webSearch.testSaveFirst')
  expect(testFetch).not.toHaveBeenCalled()
})

it('fetches the entered URL and displays returned content as text', async () => {
  testFetch.mockResolvedValue({ data: { title: 'Fetched article', content: '<script>not executable</script>\nActual page content' } })
  mount('saved-provider')
  const input = root.querySelector<HTMLInputElement>('input[type="url"]')!
  input.value = 'https://example.com/article'
  input.dispatchEvent(new Event('input'))
  root.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
  await flush()
  expect(testFetch).toHaveBeenCalledWith({ path: { id: 'saved-provider' }, body: { url: 'https://example.com/article' }, throwOnError: true })
  expect(root.textContent).toContain('Fetched article')
  expect(root.textContent).toContain('Actual page content')
  expect(root.querySelector('script')).toBeNull()
})

it('keeps loading exclusive and replaces stale results with safe errors', async () => {
  let reject: (error: unknown) => void = () => {}
  testFetch.mockReturnValue(new Promise((_resolve, rejectPromise) => { reject = rejectPromise }))
  mount('saved-provider')
  root.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
  await flush()
  expect(root.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(true)
  reject(new Error('secret-key private upstream response'))
  await flush()
  expect(root.textContent).toContain('webSearch.testFailed')
  expect(root.textContent).not.toContain('secret-key')
  expect(root.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(false)
})
