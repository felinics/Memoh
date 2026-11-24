// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, type Slots } from 'vue'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import BotMcp from './bot-mcp.vue'

const mocks = vi.hoisted(() => ({
  getBotsByBotIdMcp: vi.fn(),
  putBotsByBotIdMcpOpsImport: vi.fn(),
  route: { query: {} as Record<string, string> },
  replace: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => mocks.route,
  useRouter: () => ({ replace: mocks.replace }),
}))

vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdMcp: mocks.getBotsByBotIdMcp,
  putBotsByBotIdMcpOpsImport: mocks.putBotsByBotIdMcpOpsImport,
}))

vi.mock('./mcp-server-detail.vue', () => ({
  default: defineComponent({
    props: {
      server: { type: Object, default: null },
    },
    setup(props) {
      return () => h('div', {
        'data-testid': 'mcp-detail',
        'data-server-id': String((props.server as Record<string, unknown> | null)?.id ?? ''),
        'data-server-name': String((props.server as Record<string, unknown> | null)?.name ?? ''),
      })
    },
  }),
}))
vi.mock('@/components/code-surface/lazy', () => ({
  LazyCodeEditor: { template: '<div />' },
}))
vi.mock('@/utils/api-error', () => ({
  resolveApiErrorMessage: (_err: unknown, fallback: string) => fallback,
}))
vi.mock('@/utils/shell-words', () => ({
  joinShellWords: (cmd: string) => cmd,
}))

vi.mock('@felinic/ui', () => {
  const Passthrough = defineComponent({
    setup: (_, { slots }: { slots: Slots }) => () => h('div', [slots.actions?.(), slots.default?.()]),
  })
  const BackendCard = defineComponent({
    props: {
      name: { type: String, default: '' },
      subtitle: { type: String, default: '' },
      enabled: { type: Boolean, default: false },
    },
    emits: ['click'],
    setup(props, { emit }) {
      return () => h('button', {
        'data-mcp-card': props.name,
        'data-enabled': String(props.enabled),
        'onClick': () => emit('click'),
      }, props.name)
    },
  })
  const InputGroupInput = defineComponent({
    props: {
      modelValue: { type: String, default: '' },
      placeholder: { type: String, default: '' },
    },
    emits: ['update:modelValue'],
    setup(props, { emit }) {
      return () => h('input', {
        'data-testid': 'mcp-search',
        'value': props.modelValue,
        'placeholder': props.placeholder,
        'onInput': (event: Event) => emit('update:modelValue', (event.target as HTMLInputElement).value),
      })
    },
  })
  const Button = defineComponent({
    emits: ['click'],
    setup(_, { slots, emit }) {
      return () => h('button', { type: 'button', onClick: () => emit('click') }, slots.default?.())
    },
  })
  const passthroughUi = Object.fromEntries([
    'AutoHeight', 'Badge', 'Dialog', 'DialogClose', 'DialogDescription', 'DialogFooter',
    'DialogHeader', 'DialogPanel', 'DialogTitle', 'Empty', 'EmptyContent', 'EmptyDescription',
    'EmptyTitle', 'InputGroup', 'InputGroupAddon', 'PageShell', 'SettingsSection', 'Skeleton',
    'SwapTransition', 'Tooltip', 'TooltipContent', 'TooltipProvider', 'TooltipTrigger',
  ].map(name => [name, Passthrough]))

  return {
    ...passthroughUi,
    BackendCard,
    Button,
    InputGroupInput,
    toast: { success: vi.fn(), error: vi.fn() },
  }
})

let unmount = () => {}

async function mountMcp(items: Array<Record<string, unknown>>) {
  mocks.getBotsByBotIdMcp.mockResolvedValue({ data: { items } })
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(BotMcp, { botId: 'bot-1' })
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)
  unmount = () => app.unmount()
  await nextTick()
  await nextTick()
  await nextTick()
  return root
}

describe('bot-mcp list ordering', () => {
  beforeEach(() => {
    mocks.route.query = {}
    mocks.replace.mockReset()
    mocks.getBotsByBotIdMcp.mockReset()
  })

  afterEach(() => {
    unmount()
    document.body.innerHTML = ''
  })

  it('orders active MCP servers before inactive ones while preserving server order, keeping add tile last across search filtering', async () => {
    const root = await mountMcp([
      { id: 'mcp-4', name: 'Server Disabled Newer', type: 'stdio', is_active: false, config: { command: 'node' } },
      { id: 'mcp-3', name: 'Server Active Newer', type: 'stdio', is_active: true, config: { command: 'npx' } },
      { id: 'mcp-2', name: 'Server Disabled Older', type: 'sse', is_active: false, config: { url: 'https://a.example.com' } },
      { id: 'mcp-1', name: 'Server Active Older', type: 'sse', is_active: true, config: { url: 'https://b.example.com' } },
    ])

    const cards = () => [...root.querySelectorAll('[data-mcp-card]')]
    expect(cards().map(c => c.getAttribute('data-mcp-card'))).toEqual([
      'Server Active Newer',
      'Server Active Older',
      'Server Disabled Newer',
      'Server Disabled Older',
    ])
    expect(cards().map(c => c.getAttribute('data-enabled'))).toEqual([
      'true',
      'true',
      'false',
      'false',
    ])

    const gridButtons = () => [...root.querySelectorAll('.grid > button')]
    expect(gridButtons().map(b => b.getAttribute('data-mcp-card') ?? b.textContent?.trim())).toEqual([
      'Server Active Newer',
      'Server Active Older',
      'Server Disabled Newer',
      'Server Disabled Older',
      en.mcp.addNew,
    ])

    const searchInput = root.querySelector<HTMLInputElement>('[data-testid="mcp-search"]')
    expect(searchInput).not.toBeNull()
    searchInput!.value = 'Newer'
    searchInput!.dispatchEvent(new Event('input'))
    await nextTick()

    expect(cards().map(c => c.getAttribute('data-mcp-card'))).toEqual([
      'Server Active Newer',
      'Server Disabled Newer',
    ])
    expect(gridButtons().map(b => b.getAttribute('data-mcp-card') ?? b.textContent?.trim())).toEqual([
      'Server Active Newer',
      'Server Disabled Newer',
      en.mcp.addNew,
    ])
  })

  it('shows no-data empty state without the add tile when search matches no servers', async () => {
    const root = await mountMcp([
      { id: 'mcp-1', name: 'Filesystem', type: 'stdio', is_active: true, config: { command: 'npx' } },
    ])

    const searchInput = root.querySelector<HTMLInputElement>('[data-testid="mcp-search"]')
    expect(searchInput).not.toBeNull()
    searchInput!.value = 'nonexistent'
    searchInput!.dispatchEvent(new Event('input'))
    await nextTick()

    expect(root.querySelector('.grid')).toBeNull()
    expect(root.querySelectorAll('[data-mcp-card]').length).toBe(0)
    expect(root.querySelectorAll('.grid > button').length).toBe(0)
    expect(root.textContent).toContain(en.common.noData)
  })

  it('opens a disabled non-first server directly when ?mcpId= deep-link is present', async () => {
    mocks.route.query = { mcpId: 'mcp-2' }
    const root = await mountMcp([
      { id: 'mcp-3', name: 'Server Active', type: 'stdio', is_active: true, config: { command: 'npx' } },
      { id: 'mcp-2', name: 'Server Disabled Older', type: 'sse', is_active: false, config: { url: 'https://a.example.com' } },
    ])

    const detail = root.querySelector('[data-testid="mcp-detail"]')
    expect(detail?.getAttribute('data-server-id')).toBe('mcp-2')
    expect(detail?.getAttribute('data-server-name')).toBe('Server Disabled Older')
  })
})
