// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */

import { computed, createApp, defineComponent, h, nextTick, ref } from 'vue'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import en from '@/i18n/locales/en.json'
import type { ChatAssistantTurn, ChatUserTurn } from '@/store/chat/types'
import { buildRowMap, buildTrajectoryRows, foldTrajectoryStats } from '../../composables/trajectory-model'
import { rowMapGeometry } from '../../composables/trajectory-view'

const selectedKey = ref<string | null>(null)
const select = vi.fn((key: string | null) => {
  selectedKey.value = selectedKey.value === key ? null : key
})
const focus = vi.fn((key: string) => {
  selectedKey.value = key
})
const rows = buildTrajectoryRows([
  { id: 'u1', role: 'user', text: 'hello', attachments: [], timestamp: '2026-09-03T00:00:00.000Z', streaming: false, isSelf: true, turnId: 'turn-1', turnPosition: 1 } satisfies ChatUserTurn,
  {
    id: 'a1',
    role: 'assistant',
    turnId: 'turn-1',
    turnPosition: 1,
    timestamp: '2026-09-03T00:00:01.000Z',
    streaming: false,
    messages: [{ id: 0, type: 'text', content: 'first answer' }, { id: 1, type: 'text', content: 'second answer' }],
    stepTraces: [{ first_message_id: 0, last_message_id: 1, step_index: 0, started_at_ms: 1_000, first_token_at_ms: 1_100, ended_at_ms: 1_600 }],
  } satisfies ChatAssistantTurn,
], new Map())

// The shared atoms resolve a second Vue copy under vitest, so the pane
// renders on plain stand-ins that keep the slots, attrs and refs it uses.
vi.mock('@felinic/ui', () => {
  const box = (tag = 'div') => defineComponent({ setup: (_, { slots, attrs }) => () => h(tag, attrs, slots.default?.()) })
  return {
    Button: defineComponent({ setup: (_, { slots, attrs }) => () => h('button', { type: 'button', ...attrs }, slots.default?.()) }),
    Empty: box(),
    EmptyDescription: box(),
    SegmentedControl: box(),
    Skeleton: box(),
    Spinner: box('span'),
    ScrollArea: defineComponent({ setup: (_, { slots, attrs }) => () => h('div', attrs, h('div', { 'data-slot': 'scroll-area-viewport' }, slots.default?.())) }),
    Collapsible: box(),
    CollapsibleTrigger: box('button'),
    CollapsibleContent: box(),
  }
})
vi.mock('../../composables/useTrajectory', () => ({
  useTrajectory: () => ({
    hasTarget: computed(() => true),
    rows: computed(() => rows),
    stats: computed(() => foldTrajectoryStats([], new Map())),
    fragmentPreviews: computed(() => null),
    loadingMessages: computed(() => false),
    selectedKey,
    selectedRow: computed(() => rows.find(row => row.key === selectedKey.value) ?? null),
    bars: computed(() => rowMapGeometry(buildRowMap(rows), 'duration')),
    mode: ref('duration'),
    hasOlder: computed(() => false),
    loadingOlder: computed(() => false),
    loadOlder: vi.fn(),
    select,
    focus,
  }),
}))
vi.mock('../../composables/useContextLifecycleFragments', () => ({
  useContextLifecycleFragments: () => ({ fragments: computed(() => []), status: ref('pending'), error: ref(null) }),
}))
vi.mock('../../composables/useContextLifecycleDecisions', () => ({
  useContextLifecycleDecisions: () => ({ decisions: computed(() => []), status: ref('pending') }),
}))

import TrajectoryPane from './trajectory-pane.vue'

// The pane reads its inspector layout off a custom property the container
// query sets; jsdom resolves no container queries, so the test states it.
let layout = ''
const resizeCallbacks: (() => void)[] = []
function resize(next: string) {
  layout = next
  for (const callback of resizeCallbacks) callback()
}
beforeAll(() => {
  globalThis.ResizeObserver = class {
    constructor(callback: () => void) {
      resizeCallbacks.push(callback)
    }

    observe() {}
    unobserve() {}
    disconnect() {}
  } as never
  const original = window.getComputedStyle.bind(window)
  vi.spyOn(window, 'getComputedStyle').mockImplementation((element: Element, pseudo?: string | null) => {
    if ((element as HTMLElement).dataset?.testid === 'trajectory-split') {
      return { getPropertyValue: (name: string) => (name === '--trajectory-inspector' ? layout : '') } as CSSStyleDeclaration
    }
    return original(element, pseudo)
  })
})

const mounted: { app: ReturnType<typeof createApp>, root: HTMLDivElement }[] = []

function mount() {
  const root = document.createElement('div')
  document.body.appendChild(root)
  const app = createApp(defineComponent({ setup: () => () => h(TrajectoryPane) }))
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)
  mounted.push({ app, root })
  return root
}

afterEach(() => {
  for (const { app, root } of mounted.splice(0)) {
    app.unmount()
    root.remove()
  }
  selectedKey.value = null
  select.mockClear()
  focus.mockClear()
})

function caret(): HTMLElement | null {
  const active = document.activeElement as HTMLElement | null
  return active?.getAttribute('role') === 'option' ? active : null
}

function key(target: Element, name: string) {
  target.dispatchEvent(new KeyboardEvent('keydown', { key: name, bubbles: true, cancelable: true }))
}

async function settle() {
  await nextTick()
  await nextTick()
}

describe('trajectory pane', () => {
  it('lets selection follow the caret while the inspector sits beside the list', async () => {
    layout = 'beside'
    const root = mount()
    await settle()
    const first = root.querySelector('[role="option"]') as HTMLElement
    first.focus()
    key(first, 'ArrowDown')
    await settle()
    expect(focus).toHaveBeenCalledWith(rows[1]!.key)
    expect(root.querySelector('[data-testid="trajectory-inspector-host"]')).not.toBeNull()
    expect(caret()?.textContent).toContain('first answer')

    key(document.activeElement!, 'Escape')
    await settle()
    expect(selectedKey.value).toBeNull()
    expect(root.querySelector('[data-testid="trajectory-inspector-host"]')).toBeNull()
    expect(caret()?.textContent).toContain('first answer')
  })

  it('keeps the list in reach when the inspector would cover it', async () => {
    layout = 'cover'
    const root = mount()
    await settle()
    const first = root.querySelector('[role="option"]') as HTMLElement
    first.focus()
    key(first, 'ArrowDown')
    await settle()
    key(document.activeElement!, 'ArrowDown')
    await settle()
    expect(focus).not.toHaveBeenCalled()
    expect(selectedKey.value).toBeNull()
    expect(caret()?.textContent).toContain('second answer')

    key(document.activeElement!, 'Enter')
    await settle()
    expect(select).toHaveBeenCalledWith(rows[2]!.key)
    const host = root.querySelector('[data-testid="trajectory-inspector-host"]') as HTMLElement
    expect(host).not.toBeNull()
    expect(host.contains(document.activeElement)).toBe(true)
    expect(root.querySelector('[data-testid="trajectory-column"]')?.hasAttribute('inert')).toBe(true)

    key(document.activeElement!, 'Escape')
    await settle()
    expect(selectedKey.value).toBeNull()
    expect(root.querySelector('[data-testid="trajectory-column"]')?.hasAttribute('inert')).toBe(false)
    expect(caret()?.textContent).toContain('second answer')
  })

  it('moves a covered caret into the inspector when the pane narrows with a row open', async () => {
    layout = 'beside'
    const root = mount()
    await settle()
    const first = root.querySelector('[role="option"]') as HTMLElement
    first.focus()
    key(first, 'ArrowDown')
    await settle()
    expect(root.querySelector('[data-testid="trajectory-column"]')?.hasAttribute('inert')).toBe(false)

    resize('cover')
    await settle()
    const host = root.querySelector('[data-testid="trajectory-inspector-host"]') as HTMLElement
    expect(host.contains(document.activeElement)).toBe(true)
    expect(root.querySelector('[data-testid="trajectory-column"]')?.hasAttribute('inert')).toBe(true)

    resize('beside')
    await settle()
    expect(root.querySelector('[data-testid="trajectory-column"]')?.hasAttribute('inert')).toBe(false)
  })
})
