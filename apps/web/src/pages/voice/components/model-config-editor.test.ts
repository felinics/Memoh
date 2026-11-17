// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, type App } from 'vue'
import ModelConfigEditor from './model-config-editor.vue'

const ui = vi.hoisted(() => ({ error: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@felinic/ui', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@felinic/ui')>()
  return { ...actual, toast: { error: ui.error, success: vi.fn() } }
})

vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })

let app: App | null = null
afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
  vi.clearAllMocks()
})

describe('speech model test', () => {
  it('shows local copy, not the thrown text, when the test call fails', async () => {
    const root = document.createElement('div')
    document.body.append(root)
    app = createApp(ModelConfigEditor, {
      modelId: 'm1', modelName: 'm', config: {}, schema: null,
      onTest: () => Promise.reject(new Error('upstream 10.0.0.7 returned 502')),
    })
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    const input = root.querySelector('textarea, input[type="text"]') as HTMLTextAreaElement
    input.value = 'hello'
    input.dispatchEvent(new Event('input'))
    await vi.waitFor(() => expect(root.querySelector<HTMLButtonElement>('button[type="button"]:not([disabled])')).not.toBeNull())
    const run = [...root.querySelectorAll('button')].find(b => b.textContent?.includes('speech.test.generate'))!
    run.click()
    await vi.waitFor(() => expect(ui.error).toHaveBeenCalledWith('speech.test.failed'))
    expect(root.textContent).toContain('speech.test.failed')
    expect(root.textContent).not.toContain('10.0.0.7')
  })
})
