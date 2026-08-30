// @vitest-environment jsdom

import { createApp, nextTick, defineComponent, h } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

const uiStubs = vi.hoisted(() => ({
  ButtonStub: {
    name: 'UiButtonStub',
    inheritAttrs: false,
    setup(_: unknown, context: { attrs: Record<string, unknown>; slots: { default?: () => unknown } }) {
      return () => h('button', context.attrs, context.slots.default?.())
    },
  },
  InputStub: {
    name: 'UiInputStub',
    inheritAttrs: false,
    setup(_: unknown, context: { attrs: Record<string, unknown> }) {
      return () => h('input', context.attrs)
    },
  },
}))

// The item only needs the two UI primitives in this test; keeping them as native
// elements makes the assertions about available actions explicit.
vi.mock('@felinic/ui', () => ({ Button: uiStubs.ButtonStub, Input: uiStubs.InputStub }))

import SessionFollowUpQueueItem from './session-follow-up-queue-item.vue'
import type { EditableFollowUpQueueItem } from './use-session-follow-up-queue'

const baseItem: Omit<EditableFollowUpQueueItem, 'queueKind'> = {
  item_id: 'item-1',
  status: 'accepted',
  position: 0,
  text: 'queued input',
}

describe('SessionFollowUpQueueItem', () => {
  let app: ReturnType<typeof createApp> | undefined
  let root: HTMLDivElement | undefined

  afterEach(() => {
    app?.unmount()
    root?.remove()
    app = undefined
    root = undefined
  })

  async function mount(item: EditableFollowUpQueueItem) {
    root = document.createElement('div')
    document.body.append(root)
    const harness = defineComponent({
      setup() {
        return () => h(SessionFollowUpQueueItem, { item, busy: false })
      },
    })
    app = createApp(harness)
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    await nextTick()
    return root
  }

  it('shows steer and remove actions for a follow-up item', async () => {
    const el = await mount({ ...baseItem, queueKind: 'follow-up' })

    expect(el.querySelector('[data-queue-steer-status]')).toBeNull()
    expect(el.querySelectorAll('button')).toHaveLength(2)
  })

  it('shows only the queued steer status after promotion', async () => {
    const el = await mount({ ...baseItem, queueKind: 'steer' })

    expect(el.querySelectorAll('button')).toHaveLength(0)
    const status = el.querySelector<HTMLElement>('[data-queue-steer-status]')
    expect(status).not.toBeNull()
    expect(status?.getAttribute('aria-label')).toBe('chat.queue.steerQueued')
    expect(status?.getAttribute('title')).toBe('chat.queue.steerQueued')
  })
})
