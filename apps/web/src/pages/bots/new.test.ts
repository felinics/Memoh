// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import type { Slots } from 'vue'

const mocks = vi.hoisted(() => ({
  acpProfiles: [] as Array<{ id: string, display_name: string, setup_modes: string[] }>,
  getBotsNameAvailability: vi.fn(),
  routerBack: vi.fn(),
  routerPush: vi.fn(),
  startBotCreate: vi.fn(),
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

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({
    back: mocks.routerBack,
    push: mocks.routerPush,
  }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: translate,
  }),
}))

vi.mock('vue-sonner', () => ({
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}))

vi.mock('@vueuse/core', () => ({
  useDebounceFn: (fn: (...args: unknown[]) => unknown) => fn,
}))

vi.mock('@pinia/colada', async () => {
  const { ref } = await import('vue')
  return {
    useQuery: (options: { key?: string[] }) => ({
      data: ref(options.key?.[0] === 'acp-profiles' ? { items: mocks.acpProfiles } : []),
    }),
    useQueryCache: () => ({ invalidateQueries: vi.fn() }),
  }
})

vi.mock('@memohai/sdk', () => ({
  getBotsNameAvailability: (...args: unknown[]) => mocks.getBotsNameAvailability(...args),
  getAcpProfiles: vi.fn(async () => ({ data: { items: mocks.acpProfiles } })),
  getMemoryProviders: vi.fn(async () => ({ data: [] })),
  getModels: vi.fn(async () => ({ data: [] })),
  getProviders: vi.fn(async () => ({ data: [] })),
}))

vi.mock('@/store/bot-create-progress', () => ({
  useBotCreateProgressStore: () => ({
    bot: null,
    errorCode: null,
    setupError: null,
    start: mocks.startBotCreate,
    status: 'idle',
    reset: vi.fn(),
  }),
}))

vi.mock('@/composables/useAvatarInitials', () => ({
  useAvatarInitials: () => 'P',
}))

vi.mock('@felinic/ui', async () => {
  const { h } = await import('vue')
  const Passthrough = (_props: Record<string, unknown>, { slots }: { slots: Slots }) => h('div', slots.default?.())
  const Button = (props: Record<string, unknown>, { attrs, slots }: { attrs: Record<string, unknown>, slots: Slots }) =>
    h('button', { ...attrs, disabled: props.disabled, type: props.type ?? 'button' }, slots.default?.())
  const Input = Object.assign((
    props: { modelValue?: string },
    { attrs, emit }: { attrs: Record<string, unknown>, emit: (event: 'update:modelValue', value: string) => void },
  ) =>
    h('input', {
      ...attrs,
      value: props.modelValue ?? '',
      onInput: (event: Event) => emit('update:modelValue', (event.target as HTMLInputElement).value),
    }), {
    emits: ['update:modelValue'],
  })
  return {
    Avatar: Passthrough,
    AvatarFallback: Passthrough,
    AvatarImage: Passthrough,
    Button,
    Input,
    Label: Passthrough,
    Select: Passthrough,
    SelectContent: Passthrough,
    SelectItem: Passthrough,
    SelectTrigger: Passthrough,
    SelectValue: Passthrough,
    Separator: Passthrough,
    Spinner: Passthrough,
    Tabs: Passthrough,
    TabsList: Passthrough,
    TabsTrigger: Passthrough,
    Tooltip: Passthrough,
    TooltipContent: Passthrough,
    TooltipTrigger: Passthrough,
  }
})

vi.mock('lucide-vue-next', async () => {
  const { h } = await import('vue')
  const Icon = () => h('span')
  return {
    Check: Icon,
    CircleHelp: Icon,
    LoaderCircle: Icon,
    SquarePen: Icon,
    X: Icon,
  }
})

vi.mock('@/components/timezone-select/index.vue', () => ({ default: (_props: Record<string, unknown>) => h('select') }))
vi.mock('./components/avatar-edit-dialog.vue', () => ({ default: () => h('div') }))
vi.mock('./components/bot-import-panel.vue', () => ({ default: () => h('div') }))
vi.mock('./components/memory-provider-select.vue', () => ({ default: () => h('select') }))
vi.mock('./components/model-select.vue', () => ({ default: () => h('select') }))
vi.mock('./components/agent-type-pill.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return {
    default: defineComponent({
      props: { profiles: { type: Array, default: () => [] } },
      emits: ['update:modelValue'],
      setup(props, { emit }) {
        return () => h('button', {
          'data-select-oauth-agent': '',
          type: 'button',
          onClick: () => emit('update:modelValue', (props.profiles as Array<{ id: string }>)[0]?.id ?? 'memoh'),
        })
      },
    }),
  }
})
vi.mock('./components/acp-setup-panel.vue', async () => {
  const { defineComponent, h } = await import('vue')
  return {
    default: defineComponent({
      setup(_props, { expose }) {
        expose({
          selection: () => ({ agentId: 'claude-code', setupMode: 'oauth', managed: {} }),
          missingRequiredField: () => null,
        })
        return () => h('div')
      },
    }),
  }
})

describe('bot create page', () => {
  beforeEach(() => {
    mocks.acpProfiles = []
    mocks.getBotsNameAvailability.mockReset()
    mocks.routerBack.mockReset()
    mocks.routerPush.mockReset()
    mocks.startBotCreate.mockReset()
    mocks.getBotsNameAvailability.mockResolvedValue({ data: { available: true } })
    document.body.innerHTML = ''
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('clears submit loading after handing container creation to the progress route', async () => {
    const Page = (await import('./new.vue')).default
    const root = document.createElement('div')
    document.body.append(root)
    const app = createApp(Page)
    app.config.globalProperties.$t = translate
    app.mount(root)
    await flushPromises()

    const [displayInput] = Array.from(root.querySelectorAll('input'))
    displayInput!.value = 'Prog'
    displayInput!.dispatchEvent(new Event('input', { bubbles: true }))
    await flushPromises()

    const form = root.querySelector('form')!
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flushPromises()

    expect(mocks.startBotCreate).toHaveBeenCalledTimes(1)
    expect(mocks.routerPush).toHaveBeenCalledWith({ name: 'bot-create-progress' })
    expect(form.getAttribute('aria-busy')).toBe('false')

    app.unmount()
    root.remove()
  })

  it('defers default Agent assignment when regular creation selects OAuth', async () => {
    mocks.acpProfiles = [{ id: 'claude-code', display_name: 'Claude Code', setup_modes: ['oauth'] }]
    const Page = (await import('./new.vue')).default
    const root = document.createElement('div')
    document.body.append(root)
    const app = createApp(Page)
    app.config.globalProperties.$t = translate
    app.mount(root)
    await flushPromises()

    root.querySelector<HTMLButtonElement>('[data-select-oauth-agent]')!.click()
    await flushPromises()

    const [displayInput] = Array.from(root.querySelectorAll('input'))
    displayInput!.value = 'OAuth Bot'
    displayInput!.dispatchEvent(new Event('input', { bubbles: true }))
    await flushPromises()

    root.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flushPromises()

    expect(mocks.startBotCreate).toHaveBeenCalledWith(
      expect.any(Object),
      expect.objectContaining({
        agent: expect.objectContaining({
          provider: 'claude-code',
          deferDefault: true,
        }),
      }),
    )

    app.unmount()
    root.remove()
  })

})
