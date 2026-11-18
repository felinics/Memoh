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
      modelId: 'm1', config: {}, schema: null,
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

  it('awaits async onSave and blocks duplicate saves while pending', async () => {
    let resolveSave!: () => void
    const onSave = vi.fn().mockImplementation(() => new Promise<void>(resolve => { resolveSave = resolve }))
    const root = document.createElement('div')
    document.body.append(root)
    app = createApp(ModelConfigEditor, {
      modelId: 'm1',
      config: { voice: 'alloy' },
      schema: null,
      onTest: async () => new Blob(),
      onSave,
    })
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)

    const saveBtn = [...root.querySelectorAll('button')].find(b => b.textContent?.includes('provider.saveChanges'))!
    const isBusy = () => saveBtn.classList.contains('pointer-events-none') || saveBtn.hasAttribute('data-loading')
    saveBtn.click()
    await vi.waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    expect(isBusy()).toBe(true)

    // Second click while save promise is still pending is ignored.
    saveBtn.click()
    expect(onSave).toHaveBeenCalledOnce()

    resolveSave()
    await vi.waitFor(() => expect(isBusy()).toBe(false))
  })
})
