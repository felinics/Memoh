// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, ref, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import { client } from '@memohai/sdk/client'
import en from '@/i18n/locales/en.json'
import ProviderSetting from './provider-setting.vue'

const ui = vi.hoisted(() => ({ error: vi.fn() }))
vi.mock('@felinic/ui', async importOriginal => ({
  ...await importOriginal<typeof import('@felinic/ui')>(),
  toast: { error: ui.error, success: vi.fn() },
}))

const originalConfig = client.getConfig()
let app: App | undefined

afterEach(() => {
  app?.unmount()
  app = undefined
  document.body.innerHTML = ''
  client.setConfig(originalConfig)
  localStorage.clear()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
})

it('shows the quota error from a failed binary synthesis request in the model editor', async () => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  localStorage.setItem('language', 'en')
  const provider = { id: 'fish', name: 'Fish Audio', client_type: 'fishaudio-speech', config: {}, enable: true }
  const fetch = vi.fn(async (input: RequestInfo | URL) => {
    const request = input as Request
    const path = new URL(request.url).pathname
    if (path === '/speech-models/fish-model/test') {
      return Response.json({
        type: 'about:blank', title: 'Bad Gateway', status: 502,
        code: 'agent.provider_quota_exhausted', fault: 'dependency',
        detail: 'PRIVATE upstream diagnostic',
      }, { status: 502, headers: { 'Content-Type': 'application/problem+json' } })
    }
    if (path === '/speech-providers/fish') return Response.json(provider)
    if (path === '/speech-providers/meta') return Response.json([])
    if (path === '/speech-providers/fish/models') {
      return Response.json([{ id: 'fish-model', model_id: 's1', name: 'Fish S1', config: {} }])
    }
    throw new Error(`Unexpected request: ${path}`)
  })
  client.setConfig({ baseUrl: 'http://memoh.test', fetch })
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(ProviderSetting)
  app.provide('curTtsProvider', ref(provider))
  app.use(createPinia()).use(PiniaColada)
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)

  const model = await vi.waitFor(() => {
    const button = [...root.querySelectorAll('button')].find(button => button.textContent?.includes('Fish S1'))
    expect(button).toBeTruthy()
    return button!
  })
  model.click()
  const input = await vi.waitFor(() => {
    const input = document.querySelector<HTMLTextAreaElement>('[role="dialog"] textarea')
    expect(input).toBeTruthy()
    return input!
  })
  input.value = 'Hello Fish'
  input.dispatchEvent(new Event('input', { bubbles: true }))
  const generate = await vi.waitFor(() => {
    const button = [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')]
      .find(button => button.textContent?.includes(en.speech.test.generate))
    expect(button?.disabled).toBe(false)
    return button!
  })
  generate.click()

  await vi.waitFor(() => expect(ui.error).toHaveBeenCalledWith(en.errors.agent.provider_quota_exhausted))
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain(en.errors.agent.provider_quota_exhausted)
  expect(document.body.textContent).not.toContain('PRIVATE upstream diagnostic')
})
