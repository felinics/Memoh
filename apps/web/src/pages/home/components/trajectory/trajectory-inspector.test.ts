// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */

import { computed, createApp, defineComponent, h, nextTick, reactive, ref } from 'vue'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import en from '@/i18n/locales/en.json'
import type { ChatAssistantTurn, ChatUserTurn } from '@/store/chat/types'
import { buildTrajectoryRows, type TrajectoryRow } from '../../composables/trajectory-model'

vi.mock('@felinic/ui', () => {
  const box = (tag = 'div') => defineComponent({ setup: (_, { slots, attrs }) => () => h(tag, attrs, slots.default?.()) })
  return {
    Button: defineComponent({ setup: (_, { slots, attrs }) => () => h('button', { type: 'button', ...attrs }, slots.default?.()) }),
    Empty: box(),
    EmptyDescription: box(),
    Skeleton: box(),
    ScrollArea: defineComponent({ setup: (_, { slots, attrs }) => () => h('div', attrs, h('div', { 'data-slot': 'scroll-area-viewport' }, slots.default?.())) }),
    Collapsible: box(),
    CollapsibleTrigger: box('button'),
    CollapsibleContent: box(),
  }
})
vi.mock('../../composables/useContextLifecycleFragments', () => ({
  useContextLifecycleFragments: () => ({ fragments: computed(() => []), status: ref('pending'), error: ref(null) }),
}))
vi.mock('../../composables/useContextLifecycleDecisions', () => ({
  useContextLifecycleDecisions: () => ({ decisions: computed(() => []), status: ref('pending') }),
}))

import TrajectoryInspector from './trajectory-inspector.vue'

beforeAll(() => {
  const offsets = new WeakMap<Element, number>()
  Object.defineProperty(HTMLElement.prototype, 'scrollTop', {
    configurable: true,
    get() {
      return offsets.get(this as Element) ?? 0
    },
    set(value: number) {
      offsets.set(this as Element, Math.max(value, 0))
    },
  })
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
    messages: [
      { id: 0, type: 'tool', name: 'exec', toolName: 'exec', tool_call_id: 'c', toolCallId: 'c', input: { cmd: 'ls' }, output: 'ok', result: 'ok', running: false, done: true, execution_timing: { started_at_ms: 1_000, ended_at_ms: 1_850 } },
      { id: 1, type: 'text', content: 'the answer' },
    ],
    stepTraces: [{ first_message_id: 0, last_message_id: 1, step_index: 0, started_at_ms: 500, first_token_at_ms: 600, ended_at_ms: 2_000 }],
  } satisfies ChatAssistantTurn,
], new Map())

const mounted: { app: ReturnType<typeof createApp>, root: HTMLDivElement }[] = []

function mount(props: { row: TrajectoryRow }) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  const app = createApp(defineComponent({ setup: () => () => h(TrajectoryInspector, props) }))
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
})

describe('trajectory inspector', () => {
  it('keeps the title and close out of the scrolling body and names the tool', () => {
    const root = mount({ row: rows[1]! })
    const viewport = root.querySelector('[data-slot="scroll-area-viewport"]') as HTMLElement
    const close = root.querySelector('button[aria-label]') as HTMLElement
    expect(viewport.contains(close)).toBe(false)
    const header = close.parentElement!
    expect(header.textContent).toContain('Turn 1')
    expect(header.textContent).toContain('TOOL')
    expect(header.textContent).toContain('exec')
    expect(header.textContent).toContain('Step 0')
    expect(viewport.textContent).toContain('"cmd": "ls"')
  })

  it('scrolls the body back to the top when another row is shown', async () => {
    const props = reactive({ row: rows[1]! })
    const root = mount(props)
    const viewport = root.querySelector('[data-slot="scroll-area-viewport"]') as HTMLElement
    viewport.scrollTop = 120
    props.row = rows[2]!
    await nextTick()
    await nextTick()
    expect(viewport.scrollTop).toBe(0)
    expect(viewport.textContent).toContain('the answer')
  })
})
