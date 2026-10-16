// @vitest-environment jsdom
/* eslint-disable vue/no-deprecated-model-definition, vue/one-component-per-file */

import { createApp, defineComponent, h, nextTick } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  postTest: vi.fn(),
}))

const SlotComponent = (name: string) => defineComponent({
  name,
  setup(_, { slots }) {
    return () => h('div', slots.default?.())
  },
})

const EmptyComponent = (name: string) => defineComponent({
  name,
  setup() {
    return () => h('span', { 'data-component': name })
  },
})

vi.mock('@felinic/ui', () => ({
  Badge: SlotComponent('Badge'),
  ConfirmPopover: SlotComponent('ConfirmPopover'),
  Button: SlotComponent('Button'),
  Spinner: EmptyComponent('Spinner'),
  Switch: EmptyComponent('Switch'),
  toast: { error: vi.fn() },
}))

vi.mock('lucide-vue-next', () => ({
  Binary: EmptyComponent('Binary'),
  Settings: EmptyComponent('Settings'),
  Trash2: EmptyComponent('Trash2'),
  Zap: EmptyComponent('Zap'),
}))

vi.mock('@/components/model-description-tooltip/index.vue', () => ({
  default: SlotComponent('ModelDescriptionTooltip'),
}))

vi.mock('@memohai/sdk', () => ({
  postModelsByIdTest: mocks.postTest,
  putModelsById: vi.fn(),
}))

vi.mock('@pinia/colada', () => ({
  useQueryCache: () => ({ invalidateQueries: vi.fn() }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
    // Only the probe failure codes have copy in this mock.
    te: (key: string) => key.startsWith('errors.agent.provider_'),
  }),
}))

describe('Provider model item', () => {
  let app: ReturnType<typeof createApp> | undefined
  let root: HTMLDivElement | undefined

  afterEach(() => {
    app?.unmount()
    root?.remove()
    app = undefined
    root = undefined
  })

  async function mount(description?: string, preview = false) {
    const ModelItem = (await import('./model-item.vue')).default
    root = document.createElement('div')
    document.body.append(root)
    app = createApp(ModelItem, {
      model: {
        id: 'model-1',
        provider_id: 'provider-1',
        model_id: 'gpt-5.4',
        name: 'GPT-5.4',
        type: 'chat',
        enable: true,
        config: description === undefined ? {} : { description },
      },
      deleteLoading: false,
      preview,
    })
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    await nextTick()
    return root
  }

  async function runTest(el: HTMLElement) {
    const button = el.querySelector('[data-component="Zap"]')?.parentElement
    expect(button, 'test button should render').toBeTruthy()
    button!.click()
    for (let i = 0; i < 3; i++) {
      await Promise.resolve()
      await nextTick()
    }
  }

  it('shows the copy of the failure code after a failed test', async () => {
    mocks.postTest.mockResolvedValue({
      data: { status: 'error', reachable: true, latency_ms: 30, code: 'agent.provider_auth_failed' },
    })
    const el = await mount()
    await runTest(el)

    expect(el.textContent).toContain('errors.agent.provider_auth_failed')
  })

  it('shows the generic copy for a failure without a code', async () => {
    mocks.postTest.mockResolvedValue({ data: { status: 'error', reachable: false } })
    const el = await mount()
    await runTest(el)

    expect(el.textContent).toContain('models.testFailed')
  })

  it('shows that the provider does not recognize the model', async () => {
    mocks.postTest.mockResolvedValue({ data: { status: 'model_not_supported', reachable: true, latency_ms: 30 } })
    const el = await mount()
    await runTest(el)

    expect(el.textContent).toContain('models.testModelNotSupported')
    expect(el.textContent).not.toContain('models.testFailed')
  })

  it('shows no failure copy after a passed test', async () => {
    mocks.postTest.mockResolvedValue({ data: { status: 'ok', reachable: true, latency_ms: 30 } })
    const el = await mount()
    await runTest(el)

    expect(el.textContent).toContain('30ms')
    expect(el.textContent).not.toContain('models.testFailed')
  })

  it('shows the copy of a failed request, not its detail', async () => {
    mocks.postTest.mockRejectedValue(Object.assign(new Error('dial tcp 10.0.0.7:443: connect: connection refused'), {
      code: 'agent.provider_unreachable',
      fault: 'dependency',
    }))
    const el = await mount()
    await runTest(el)

    expect(el.textContent).toContain('could not reach the model provider')
    expect(el.textContent).not.toContain('dial tcp')
  })

  it('shows the model description below the model metadata', async () => {
    const el = await mount('General-purpose model with vision and tool calling.')

    const description = el.querySelector('[data-model-description]')
    expect(description?.textContent?.trim()).toBe(
      'General-purpose model with vision and tool calling.',
    )
    expect(description?.classList).toContain('line-clamp-2')
  })

  it('does not reserve a description row when the model has no description', async () => {
    const el = await mount()

    expect(el.querySelector('[data-model-description]')).toBeNull()
  })

  it('hides tenant model actions while showing a template preview', async () => {
    const el = await mount(undefined, true)

    expect(el.textContent).toContain('GPT-5.4')
    expect(el.querySelector('[data-component="Switch"]')).toBeNull()
    expect(el.querySelector('[data-component="Zap"]')).toBeNull()
    expect(el.querySelector('[data-component="Settings"]')).toBeNull()
    expect(el.querySelector('[data-component="Trash2"]')).toBeNull()
  })
})
