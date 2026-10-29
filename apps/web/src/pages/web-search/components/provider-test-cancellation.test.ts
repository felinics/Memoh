// @vitest-environment jsdom
import { createServer, type Server, type ServerResponse } from 'node:http'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, ref, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import { client } from '@memohai/sdk/client'
import SearchProviderTest from './search-provider-test.vue'
import FetchProviderTest from './fetch-provider-test.vue'

interface PendingRequest {
  path: string
  body: Record<string, string>
  response: ServerResponse
  canceled: boolean
}

let app: App | undefined
let root: HTMLDivElement
let server: Server
let requests: PendingRequest[]
const originalConfig = client.getConfig()

beforeEach(async () => {
  requests = []
  server = createServer((request, response) => {
    let body = ''
    request.setEncoding('utf8')
    request.on('data', chunk => { body += chunk })
    request.on('end', () => {
      const pending = { path: request.url!, body: JSON.parse(body), response, canceled: false }
      response.on('close', () => { pending.canceled = !response.writableEnded })
      requests.push(pending)
    })
  })
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('Expected loopback HTTP address')
  client.setConfig({ baseUrl: `http://127.0.0.1:${address.port}` })
})

afterEach(async () => {
  app?.unmount()
  app = undefined
  root?.remove()
  server.closeAllConnections()
  await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()))
  client.setConfig(originalConfig)
})

function mount(component: typeof SearchProviderTest | typeof FetchProviderTest) {
  const providerId = ref<string | undefined>('provider-a')
  root = document.createElement('div')
  document.body.appendChild(root)
  app = createApp({ render: () => h(component, { providerId: providerId.value }) })
  app.use(createPinia())
  app.use(PiniaColada)
  app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: { en: {} } }))
  app.mount(root)
  return providerId
}

function submit(value: string) {
  const input = root.querySelector('input')!
  input.value = value
  input.dispatchEvent(new Event('input', { bubbles: true }))
  root.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
}

function respond(index: number, body: unknown, status = 200) {
  requests[index]!.response.writeHead(status, { 'Content-Type': 'application/json' })
  requests[index]!.response.end(JSON.stringify(body))
}

for (const testCase of [
  { name: 'search', component: SearchProviderTest, value: 'Memoh', field: 'query', failure: 'webSearch.test.failed', result: { results: [{ title: 'Current result' }] } },
  { name: 'fetch', component: FetchProviderTest, value: 'https://example.com/article', field: 'url', failure: 'webSearch.testFailed', result: { title: 'Current result', content: 'Current content' } },
]) {
  describe(`${testCase.name} provider test SDK transport`, () => {
    it('aborts on provider change, resets state, and allows retries without leaking canceled errors', async () => {
      const providerId = mount(testCase.component)
      submit(testCase.value)
      await vi.waitFor(() => expect(requests).toHaveLength(1))
      expect(requests[0]!.path).toBe(`/${testCase.name}-providers/provider-a/test`)
      expect(requests[0]!.body).toEqual({ [testCase.field]: testCase.value })

      providerId.value = 'provider-b'
      await nextTick()
      // Returning to A must not let the canceled A attempt overwrite its new state.
      providerId.value = 'provider-a'
      await vi.waitFor(() => expect(requests[0]!.canceled).toBe(true))
      await vi.waitFor(() => expect(root.querySelector('input')!.disabled).toBe(false))
      expect(root.textContent).not.toContain(testCase.failure)
      expect(root.querySelector('pre')).toBeNull()
      expect(root.querySelector('input')!.value).toBe(testCase.name === 'search' ? '' : testCase.value)

      providerId.value = 'provider-b'
      await nextTick()
      submit(testCase.value)
      await vi.waitFor(() => expect(requests).toHaveLength(2))
      expect(requests[1]!.path).toBe(`/${testCase.name}-providers/provider-b/test`)
      respond(1, { code: 'provider_unavailable', message: 'private upstream error' }, 502)
      await vi.waitFor(() => expect(root.textContent).toContain(testCase.failure))
      expect(root.textContent).not.toContain('private upstream error')

      submit(testCase.value)
      await vi.waitFor(() => expect(requests).toHaveLength(3))
      expect(root.textContent).not.toContain(testCase.failure)
      respond(2, testCase.result)
      await vi.waitFor(() => expect(root.textContent).toContain('Current result'))
      providerId.value = undefined
      await nextTick()
      expect(root.querySelector('pre')).toBeNull()
      expect(root.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(true)
      expect(root.textContent).not.toContain(testCase.failure)
    }, 10_000)

    it('keeps input edits from canceling work but aborts the real fetch on unmount', async () => {
      mount(testCase.component)
      submit(testCase.value)
      await vi.waitFor(() => expect(requests).toHaveLength(1))
      const input = root.querySelector('input')!
      input.value = `${testCase.value}2`
      input.dispatchEvent(new Event('input', { bubbles: true }))
      await nextTick()
      respond(0, testCase.result)
      await vi.waitFor(() => expect(root.textContent).toContain('Current result'))
      expect(requests[0]!.canceled).toBe(false)

      submit(testCase.value)
      await vi.waitFor(() => expect(requests).toHaveLength(2))
      app!.unmount()
      app = undefined
      await vi.waitFor(() => expect(requests[1]!.canceled).toBe(true))
    }, 10_000)
  })
}
