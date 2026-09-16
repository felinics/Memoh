// @vitest-environment jsdom
import { createApp, defineComponent, h, nextTick, provide, ref } from 'vue'
import { createI18n } from 'vue-i18n'
import { createPinia } from 'pinia'
import { PiniaColada } from '@pinia/colada'
import { afterEach, expect, it, vi } from 'vitest'
import type { ModelsAddRequest } from '@memohai/sdk'
import CreateModel from './index.vue'
import en from '@/i18n/locales/en.json'

const postModels = vi.fn(async (_options: { body: ModelsAddRequest }) => ({ data: { id: 'model-1' } }))
vi.mock('@memohai/sdk', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@memohai/sdk')>()),
  postModels: (options: { body: ModelsAddRequest }) => postModels(options),
}))

let app: ReturnType<typeof createApp> | undefined
let root: HTMLDivElement | undefined

afterEach(() => {
  app?.unmount()
  root?.remove()
  vi.unstubAllGlobals()
  postModels.mockClear()
})

function typeInto(input: HTMLInputElement, text: string) {
  for (let i = 1; i <= text.length; i++) {
    input.value = text.slice(0, i)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  }
  input.dispatchEvent(new Event('change', { bubbles: true }))
  input.dispatchEvent(new Event('blur'))
}

it('submits the typed context window as a number', async () => {
  vi.stubGlobal('ResizeObserver', class {
    observe() {}
    unobserve() {}
    disconnect() {}
  })
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(defineComponent({
    setup() {
      provide('openModel', ref(true))
      provide('openModelTitle', ref('title'))
      provide('openModelState', ref(null))
      return () => h(CreateModel, { id: 'provider-1' })
    },
  }))
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.use(createPinia())
  app.use(PiniaColada)
  app.mount(root)

  const contextWindow = await vi.waitFor(() => {
    const el = document.querySelector<HTMLInputElement>('#create-model-context-window')
    expect(el).not.toBeNull()
    return el!
  })
  typeInto(document.querySelector<HTMLInputElement>('#create-model-model-id')!, 'glm-4.5')
  typeInto(contextWindow, '128000')
  await nextTick()

  document.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
  await vi.waitFor(() => expect(postModels).toHaveBeenCalledTimes(1))
  expect(postModels).toHaveBeenCalledWith(expect.objectContaining({
    body: expect.objectContaining({ config: expect.objectContaining({ context_window: 128000 }) }),
  }))
})
