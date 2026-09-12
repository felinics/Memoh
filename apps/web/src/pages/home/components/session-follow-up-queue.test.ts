// @vitest-environment jsdom

import { computed, createApp, h, nextTick, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SortableEvent } from 'sortablejs'

const mocks = vi.hoisted(() => ({ create: vi.fn(), reorder: vi.fn(), refresh: vi.fn() }))
vi.mock('sortablejs', () => ({ default: { create: mocks.create, active: null, ghost: null, clone: null } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@felinic/ui', () => ({ toast: { error: vi.fn() } }))
vi.mock('@/utils/api-error', () => ({ resolveApiErrorMessage: () => 'error' }))
vi.mock('./session-follow-up-queue-item.vue', () => ({
  default: (props: { item: { item_id: string; text: string } }) =>
    h('div', { 'data-queue-item': props.item.item_id }, props.item.text),
}))
vi.mock('./use-session-follow-up-queue', () => ({
  useSessionFollowUpQueue: () => ({
    items, hasItems: computed(() => items.value.length > 0), steerSupported: true, busy: ref(new Set()),
    refresh: mocks.refresh, reorder: mocks.reorder, update: vi.fn(), remove: vi.fn(), steer: vi.fn(),
  }),
}))

import Sortable from 'sortablejs'
import SessionFollowUpQueue from './session-follow-up-queue.vue'

const items = ref([{ item_id: 'one', text: 'one' }, { item_id: 'two', text: 'two' }])
let app: ReturnType<typeof createApp> | undefined
let root: HTMLElement
let instance: { el: HTMLElement; destroy: ReturnType<typeof vi.fn> }
let options: { onChoose: (event: SortableEvent) => void; onEnd: (event: SortableEvent) => Promise<void> }

async function mount() {
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(SessionFollowUpQueue, { botId: 'bot', sessionId: 'session' })
  app.mount(root)
  await nextTick()
}

function startDrag() {
  const item = instance.el.firstElementChild as HTMLElement
  options.onChoose({ from: instance.el, item } as SortableEvent)
  Sortable.active = instance as unknown as Sortable
  Sortable.ghost = item.cloneNode(true) as HTMLElement
  Sortable.clone = item.cloneNode(true) as HTMLElement
  document.body.append(Sortable.ghost, Sortable.clone)
  return item
}

describe('follow-up queue drag lifecycle', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    items.value = [{ item_id: 'one', text: 'one' }, { item_id: 'two', text: 'two' }]
    mocks.create.mockImplementation((el, config) => {
      options = config
      instance = { el, destroy: vi.fn(() => {
        // Match Sortable's destroy: it clears references without removing the
        // fallback DOM nodes when no pointer event reaches _onDrop.
        Sortable.active = Sortable.ghost = Sortable.clone = null
      }) }
      return instance
    })
  })

  afterEach(() => {
    app?.unmount()
    app = undefined
    root?.remove()
  })

  it('removes floating artifacts when a dragged message is consumed', async () => {
    await mount()
    startDrag()
    const ghost = Sortable.ghost!
    const clone = Sortable.clone!
    const previous = instance
    items.value = [items.value[1]!]
    await nextTick()
    await nextTick()
    expect(previous.destroy).toHaveBeenCalledOnce()
    expect(ghost.isConnected).toBe(false)
    expect(clone.isConnected).toBe(false)
    expect(root.querySelectorAll('[data-queue-item]')).toHaveLength(1)
    expect(root.textContent).toBe('two')
    expect(mocks.reorder).not.toHaveBeenCalled()
  })

  it('restores DOM before letting Vue apply the reordered data', async () => {
    await mount()
    const item = startDrag()
    instance.el.append(item)
    mocks.reorder.mockImplementationOnce(() => {
      expect(Array.from(instance.el.children).map(el => el.textContent)).toEqual(['one', 'two'])
      items.value = [items.value[1]!, items.value[0]!]
    })
    // Normal drop removes these before emitting onEnd.
    Sortable.ghost?.remove()
    Sortable.clone?.remove()
    await options.onEnd({ from: instance.el, item, oldIndex: 0, newIndex: 1 } as SortableEvent)
    await nextTick()
    expect(mocks.reorder).toHaveBeenCalledWith(0, 1)
    expect(Array.from(instance.el.children).map(el => el.textContent)).toEqual(['two', 'one'])
  })

})
