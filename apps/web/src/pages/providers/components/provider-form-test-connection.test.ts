// @vitest-environment jsdom
// 独立文件而非并入 provider-form.test.ts:那边的 SettingsSection mock 只渲染
// default slot(测试按钮在 #footer 里,渲染不出来),而渲染全部 slot 会改变
// 既有用例的按钮 DOM 顺序。这里自建一套渲染全 slot 的 mock,互不干扰。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import type { Slots } from 'vue'

const mocks = vi.hoisted(() => ({
  postTest: vi.fn(),
}))

function translate(key: string) {
  return key
}

async function flushPromises() {
  await Promise.resolve()
  await nextTick()
  await Promise.resolve()
  await nextTick()
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: translate }),
}))

vi.mock('@memohai/sdk', () => ({
  deleteProvidersByIdOauthToken: vi.fn(),
  getProvidersByIdOauthAuthorize: vi.fn(),
  getProvidersByIdOauthStatus: vi.fn(),
  postProvidersByIdOauthPoll: vi.fn(),
  postProvidersByIdTest: mocks.postTest,
}))

vi.mock('@/composables/useProviderModelCatalog', () => ({
  useProviderModelCatalog: () => ({ syncProviderModelCatalog: vi.fn() }),
}))

vi.mock('lucide-vue-next', () => ({
  AlertCircle: () => h('span'),
  KeyRound: () => h('span'),
  RefreshCw: () => h('span'),
}))

vi.mock('@/components/check-draw-icon/index.vue', () => ({ default: { template: '<span />' } }))
vi.mock('@/components/loading-button/index.vue', () => ({ default: { template: '<div><slot /></div>' } }))

vi.mock('@felinic/ui', async () => {
  const { h } = await import('vue')
  const Passthrough = (_props: Record<string, unknown>, { slots }: { slots: Slots }) =>
    h('div', Object.values(slots).map(slot => slot?.()))
  const FormField = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.({
    componentField: {},
    errorMessage: '',
  }))
  const Button = (_props: Record<string, unknown>, { attrs, slots }: { attrs: Record<string, unknown>, slots: Slots }) => h('button', attrs, slots.default?.())
  return {
    AutoHeight: Passthrough,
    Button,
    ConfirmPopover: Passthrough,
    DeviceCodePanel: Passthrough,
    SettingsRow: Passthrough,
    SettingsSection: Passthrough,
    FormControl: Passthrough,
    FormField,
    FormItem: Passthrough,
    FormMessage: Passthrough,
    HoverCard: Passthrough,
    HoverCardContent: Passthrough,
    HoverCardTrigger: Passthrough,
    Input: Passthrough,
    LabelSwap: Passthrough,
    Select: Passthrough,
    SelectContent: Passthrough,
    SelectItem: Passthrough,
    SelectTrigger: Passthrough,
    SelectValue: Passthrough,
    Spinner: Passthrough,
    toast: { success: vi.fn(), error: vi.fn() },
  }
})

describe('provider test connection states', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  async function mountAndRunTest(status: string, message = 'service error (404): not found') {
    mocks.postTest.mockResolvedValue({
      data: { status, reachable: true, latency_ms: 12, message },
    })
    const providerForm = (await import('./provider-form.vue')).default
    const root = document.createElement('div')
    document.body.append(root)

    const app = createApp(providerForm, {
      provider: {
        id: 'provider-id',
        name: 'Custom',
        client_type: 'openai-completions',
        enable: true,
        config: {},
      },
      editLoading: false,
      ensureProvider: vi.fn(),
      saveProvider: vi.fn(),
    })
    app.config.globalProperties.$t = translate
    app.mount(root)
    await flushPromises()

    const testButton = [...root.querySelectorAll('button')].find(b =>
      b.textContent?.includes('provider.testConnection'),
    ) as HTMLButtonElement
    expect(testButton, 'test connection button should render').toBeTruthy()
    testButton.click()
    await flushPromises()
    return { app, root }
  }

  // #1087: unverified 是"无法确认"而非失败,必须给指引文案,不能落进错误态。
  it('shows the unverified hint when the provider cannot be confirmed', async () => {
    const { app, root } = await mountAndRunTest('unverified')
    expect(root.textContent).toContain('provider.testUnverifiedHint')
    app.unmount()
    root.remove()
  })

  it('does not show the unverified hint on a plain error', async () => {
    const { app, root } = await mountAndRunTest('error')
    expect(root.textContent).not.toContain('provider.testUnverifiedHint')
    app.unmount()
    root.remove()
  })

  // Base URL 指向网页时,上游把整个 HTML 页面塞在错误体里;页面散文
  // (含剥了链接的死文字)不是有用的 API 错误,必须整条丢掉,只留状态头。
  it('drops HTML page prose from the upstream error body', async () => {
    const { app, root } = await mountAndRunTest(
      'unverified',
      'api error 404: 404 Not Found [body: <!doctype html><html><body><h1>Example Domain</h1><a href="https://iana.org">Learn more</a></body></html>]',
    )
    expect(root.textContent).toContain('api error 404')
    expect(root.textContent).not.toContain('Learn more')
    expect(root.textContent).not.toContain('Example Domain')
    app.unmount()
    root.remove()
  })
})
