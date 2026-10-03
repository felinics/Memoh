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

// Only the probe failure codes have copy in this mock, so an unknown code
// exercises the fallback.
function hasTranslation(key: string) {
  return key.startsWith('errors.agent.provider_')
}

async function flushPromises() {
  await Promise.resolve()
  await nextTick()
  await Promise.resolve()
  await nextTick()
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: translate, te: hasTranslation }),
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

  async function mountAndRunTest(result: Record<string, unknown> | Error) {
    if (result instanceof Error || !('status' in result)) {
      mocks.postTest.mockRejectedValue(result)
    } else {
      mocks.postTest.mockResolvedValue({ data: { latency_ms: 12, ...result } })
    }
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

  // #1087: unverified 是"无法确认"而非失败,必须给指引文案,不能落进错误态;
  // code 文案降为次要行。
  it('shows the unverified hint with the code copy as a second line', async () => {
    const { app, root } = await mountAndRunTest({
      status: 'unverified',
      reachable: true,
      code: 'agent.provider_request_rejected',
    })
    expect(root.textContent).toContain('provider.testUnverifiedHint')
    expect(root.textContent).toContain('errors.agent.provider_request_rejected')
    app.unmount()
    root.remove()
  })

  it('shows the copy of the failure code without the unverified hint', async () => {
    const { app, root } = await mountAndRunTest({
      status: 'auth_error',
      reachable: true,
      code: 'agent.provider_auth_failed',
    })
    expect(root.textContent).toContain('errors.agent.provider_auth_failed')
    expect(root.textContent).not.toContain('provider.testUnverifiedHint')
    app.unmount()
    root.remove()
  })

  it('falls back to the unreachable copy when no code was sent', async () => {
    const { app, root } = await mountAndRunTest({ status: 'error', reachable: false })
    expect(root.textContent).toContain('provider.unreachable')
    app.unmount()
    root.remove()
  })

  it('falls back to the generic copy for a code without copy', async () => {
    const { app, root } = await mountAndRunTest({
      status: 'unverified',
      reachable: true,
      code: 'agent.some_future_code',
    })
    expect(root.textContent).toContain('provider.testFailed')
    expect(root.textContent).not.toContain('agent.some_future_code')
    app.unmount()
    root.remove()
  })

  // 请求本身失败时只显示 code 对应的文案,不显示 Problem 的 detail。
  it('shows the copy of a failed request, not its detail', async () => {
    const { app, root } = await mountAndRunTest(Object.assign(new Error('dial tcp 10.0.0.7:443: connect: connection refused'), {
      code: 'agent.provider_unreachable',
      fault: 'dependency',
    }))
    expect(root.textContent).toContain('could not reach the model provider')
    expect(root.textContent).not.toContain('dial tcp')
    app.unmount()
    root.remove()
  })
})
