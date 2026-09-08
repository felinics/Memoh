// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */

import { computed, createApp, defineComponent, h, nextTick, reactive, ref } from 'vue'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import en from '@/i18n/locales/en.json'
import type { HandlersContextFragmentText } from '@memohai/sdk'
import type { ChatAssistantTurn, ChatUserTurn } from '@/store/chat/types'
import { buildTrajectoryRows, lifecycleByTurnId, type TrajectoryRow } from '../../composables/trajectory-model'
import { mergeTrajectoryCaptures } from '../../composables/context-trajectory-view'

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
const fragmentData = vi.hoisted((): { items: HandlersContextFragmentText[] } => ({ items: [] }))
vi.mock('../../composables/useContextLifecycleFragments', () => ({
  useContextLifecycleFragments: () => ({ fragments: computed(() => fragmentData.items), status: ref(fragmentData.items.length ? 'success' : 'pending'), error: ref(null) }),
}))
vi.mock('../../composables/useContextLifecycleDecisions', () => ({
  useContextLifecycleDecisions: () => ({ decisions: computed(() => []), status: ref('pending') }),
}))
vi.mock('./trajectory-capture-inspector.vue', () => ({
  default: defineComponent({
    props: { event: { type: Object, required: true } },
    emits: ['selectEvent'],
    setup: (props, { emit }) => () => h('div', { 'data-testid': 'capture-detail' }, [
      String(props.event?.id), h('button', { onClick: () => emit('selectEvent', '1') }, 'previous'),
    ]),
  }),
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

function mount(props: { row: TrajectoryRow, onSelectEvent?: (id: string) => void }) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  const app = createApp(defineComponent({ setup: () => () => h(TrajectoryInspector, props) }))
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)
  mounted.push({ app, root })
  return root
}

afterEach(() => {
  fragmentData.items = []
  for (const { app, root } of mounted.splice(0)) {
    app.unmount()
    root.remove()
  }
})

describe('trajectory inspector', () => {
  it('shows both occurrence names even when full text is shared', () => {
    fragmentData.items = [
      { content_hash: 'same', text_hash: 'shared', label: 'first.rules', text: 'shared rules', available: true },
      { content_hash: 'same', text_hash: 'shared', label: 'second.rules', text: 'shared rules', available: true },
    ]
    const row = buildTrajectoryRows([{
      id: 'u', role: 'user', text: 'task', attachments: [], timestamp: '', streaming: false, isSelf: true, turnId: 'turn',
    }], lifecycleByTurnId([{
      run_id: 'run', turn_id: 'turn', snapshot: {
        breakdown: [{ kind: 'workspace_instruction', fragments: 2 }],
        fragments: [
          { kind: 'workspace_instruction', label: 'first.rules', content_hash: 'same', text_hash: 'shared' },
          { kind: 'workspace_instruction', label: 'second.rules', content_hash: 'same', text_hash: 'shared' },
        ],
      },
    }])).find(row => row.detail.kind === 'context')!
    const root = mount({ row })
    expect(root.textContent).toContain('first.rules')
    expect(root.textContent).toContain('second.rules')
  })

  it('opens a captured request and relays navigation to the preceding stage', () => {
    const row = mergeTrajectoryCaptures([], [{ id: '2', run_id: 'run', capture_id: 'capture', sequence: 2, stage: 'wire_request' }], [])[0]!
    const selectEvent = vi.fn()
    const root = mount({ row, onSelectEvent: selectEvent })
    const detail = root.querySelector('[data-testid="capture-detail"]')
    expect(detail?.textContent).toContain('2')
    detail?.querySelector<HTMLButtonElement>('button')?.click()
    expect(selectEvent).toHaveBeenCalledWith('1')
  })
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
