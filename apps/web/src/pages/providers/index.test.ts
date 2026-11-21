// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref, type Component, type Ref, type Slots } from 'vue'
import type { ProviderScopeTab } from '@/lib/provider-scopes'

interface MockPanelControl {
  view: Ref<'list' | 'detail'>
  openAdd: ReturnType<typeof vi.fn>
}

const mocks = vi.hoisted(() => ({
  route: null as unknown as { query: Record<string, string> },
  replace: vi.fn(() => Promise.resolve()),
  panels: {} as Record<string, MockPanelControl>,
  roCallback: null as ((entries: Array<{ borderBoxSize: Array<{ blockSize: number }> }>) => void) | null,
  roDisconnect: vi.fn(),
}))

vi.mock('vue-router', async () => {
  const { reactive } = await import('vue')
  mocks.route = reactive({ query: {} })
  return {
    useRoute: () => mocks.route,
    useRouter: () => ({ replace: mocks.replace }),
  }
})

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('lucide-vue-next', () => ({
  Plus: () => h('span'),
  Search: () => h('span'),
}))

vi.mock('@felinic/ui', () => {
  const Passthrough = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.())
  const Button = (_props: Record<string, unknown>, { slots, attrs }: { slots: Slots, attrs: Record<string, unknown> }) =>
    h('button', { ...attrs, 'data-testid': 'header-add-btn' }, slots.default?.())
  const PageHeader = (props: { title?: string }, { slots }: { slots: Slots }) =>
    h('header', [h('h1', props.title), slots.actions?.()])
  return {
    Button,
    InputGroup: Passthrough,
    InputGroupAddon: Passthrough,
    InputGroupInput: Passthrough,
    PageHeader,
  }
})

vi.mock('@/components/provider-scope-tabs/index.vue', () => ({
  default: {
    props: ['modelValue'],
    emits: ['update:modelValue'],
    setup(props: { modelValue: string }, { emit }: { emit: (e: 'update:modelValue', v: string) => void }) {
      return () => h('div', { 'data-testid': 'scope-tabs', 'data-active': props.modelValue }, [
        h('button', { 'data-testid': 'switch-voice', onClick: () => emit('update:modelValue', 'voice') }, 'Voice'),
      ])
    },
  },
}))

function makeMockPanel(tab: ProviderScopeTab) {
  return defineComponent({
    name: `Mock-${tab}`,
    props: { searchQuery: { type: String, required: true } },
    setup(_props, { expose }) {
      const view = ref<'list' | 'detail'>(
        tab === 'models' && mocks.route.query.provider ? 'detail' : 'list',
      )
      const openAdd = vi.fn()
      mocks.panels[tab] = { view, openAdd }
      expose({ view, openAdd })
      return () => h('div', { 'data-testid': `panel-${tab}`, 'data-view': view.value })
    },
  })
}

vi.mock('./models-panel.vue', () => ({ default: makeMockPanel('models') }))
vi.mock('@/pages/voice/index.vue', () => ({ default: makeMockPanel('voice') }))
vi.mock('@/pages/video/index.vue', () => ({ default: makeMockPanel('video') }))
vi.mock('@/pages/web-search/index.vue', () => ({ default: makeMockPanel('web-search') }))

let ProvidersPage: Component

describe('providers container page', () => {
  const originalResizeObserver = globalThis.ResizeObserver

  beforeEach(async () => {
    vi.resetModules()
    ProvidersPage = (await import('./index.vue')).default
    mocks.route.query = {}
    mocks.replace.mockClear()
    mocks.panels = {}
    mocks.roCallback = null
    mocks.roDisconnect.mockClear()

    globalThis.ResizeObserver = class {
      constructor(cb: (entries: Array<{ borderBoxSize: Array<{ blockSize: number }> }>) => void) {
        mocks.roCallback = cb
      }

      observe() {}
      unobserve() {}
      disconnect() {
        mocks.roDisconnect()
      }
    } as unknown as typeof ResizeObserver
  })

  afterEach(() => {
    globalThis.ResizeObserver = originalResizeObserver
    document.body.innerHTML = ''
  })

  async function mountProviders() {
    const root = document.createElement('div')
    document.body.append(root)
    const app = createApp(ProvidersPage)
    app.mount(root)
    await nextTick()
    return { app, root }
  }

  function getPanel(tab: ProviderScopeTab): MockPanelControl {
    const panel = mocks.panels[tab]
    if (!panel) throw new Error(`Expected panel ${tab} to mount`)
    return panel
  }

  async function flushFrames() {
    await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
    await nextTick()
  }

  it('syncs header visibility and leave direction on panel view flip before route.query settles', async () => {
    const { app, root } = await mountProviders()
    const header = root.querySelector('[data-providers-header]') as HTMLElement
    expect(header.style.display).not.toBe('none')

    // Simulate synchronous openDetail in active panel while route.query.provider is still unset.
    getPanel('models').view.value = 'detail'
    await nextTick()
    expect(header.className).toContain('swap-forward-leave-active')

    await flushFrames()
    expect(header.style.display).toBe('none')

    // Returning synchronously re-shows header before async route.replace finishes.
    getPanel('models').view.value = 'list'
    await nextTick()
    expect(header.style.display).not.toBe('none')
    expect(header.className).toContain('swap-back-enter-active')

    app.unmount()
  })

  it('isolates KeepAlive panel views across tabs and forwards Add to active tab', async () => {
    const { app, root } = await mountProviders()
    const header = root.querySelector('[data-providers-header]') as HTMLElement

    // Leave models cached in detail view, then navigate to voice tab via URL.
    getPanel('models').view.value = 'detail'
    await nextTick()
    await flushFrames()
    expect(header.style.display).toBe('none')

    mocks.route.query = { tab: 'voice' }
    await nextTick()
    expect(root.querySelector('[data-testid="panel-voice"]')).not.toBeNull()
    expect(header.style.display).not.toBe('none')

    // Header Add triggers the active voice panel rather than cached models panel.
    ;(root.querySelector('[data-testid="header-add-btn"]') as HTMLButtonElement).click()
    expect(getPanel('voice').openAdd).toHaveBeenCalledTimes(1)
    expect(getPanel('models').openAdd).not.toHaveBeenCalled()

    app.unmount()
  })

  it('writes subpixel ResizeObserver header height and re-measures on deep-link back', async () => {
    mocks.route.query = { tab: 'models', provider: 'p-1' }
    const { app, root } = await mountProviders()
    const shell = root.querySelector('[data-providers-shell]') as HTMLElement
    const header = root.querySelector('[data-providers-header]') as HTMLElement
    const measureEl = header.firstElementChild as HTMLElement

    expect(header.style.display).toBe('none')
    expect(shell.style.getPropertyValue('--providers-header-offset')).toBe('')

    vi.spyOn(measureEl, 'getBoundingClientRect').mockReturnValue({
      height: 157.25,
      width: 390,
      top: 0,
      left: 0,
      right: 390,
      bottom: 157.25,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    })

    // Return from cold deep-link -> @enter calls syncHeaderHeight synchronously once visible.
    getPanel('models').view.value = 'list'
    mocks.route.query = { tab: 'models' }
    await nextTick()
    expect(shell.style.getPropertyValue('--providers-header-offset')).toBe('157.25px')

    // ResizeObserver updates keep subpixel borderBoxSize precision.
    mocks.roCallback?.([{ borderBoxSize: [{ blockSize: 136.5 }] }])
    expect(shell.style.getPropertyValue('--providers-header-offset')).toBe('136.5px')

    app.unmount()
    expect(mocks.roDisconnect).toHaveBeenCalledTimes(1)
  })
})
