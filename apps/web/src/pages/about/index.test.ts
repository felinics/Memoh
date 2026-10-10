// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, ref, type App } from 'vue'
import { createPinia } from 'pinia'
import About from './index.vue'

const ui = vi.hoisted(() => ({ error: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/i18n', () => ({ default: { global: { t: (key: string) => key } } }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ name: 'about', query: {} }),
  useRouter: () => ({ replace: vi.fn() }),
}))
vi.mock('markstream-vue', () => ({ default: () => h('div') }))
vi.mock('@/pages/home/components/chat-code-block.vue', () => ({ default: () => h('div') }))
vi.mock('@/components/markdown', () => ({ registerSharedMarkdownComponents: vi.fn() }))
vi.mock('@/store/capabilities', () => ({
  useCapabilitiesStore: () => ({ serverVersion: ref('1.0.0'), commitHash: ref(''), load: vi.fn() }),
}))
vi.mock('@/store/settings', () => ({
  useSettingsStore: () => ({ shikiThemeLight: 'a', shikiThemeDark: 'b' }),
}))
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

describe('about page update check', () => {
  it('toasts local copy, not the thrown text, when the release lookup fails', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 503 }))
    const root = document.createElement('div')
    document.body.append(root)
    app = createApp(About)
    app.use(createPinia())
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    const check = [...root.querySelectorAll('button')].find(b => b.textContent?.includes('about.checkForUpdates'))!
    check.click()
    await vi.waitFor(() => expect(ui.error).toHaveBeenCalledWith('about.checkFailed'))
  })
})
