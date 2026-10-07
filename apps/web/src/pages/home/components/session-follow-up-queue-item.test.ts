// @vitest-environment jsdom

import { createApp, nextTick, defineComponent, h, type VNode } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

const uiStubs = vi.hoisted(() => ({
  ButtonStub: {
    name: 'UiButtonStub',
    inheritAttrs: false,
    setup(_: unknown, context: { attrs: Record<string, unknown>; slots: { default?: () => VNode[] } }) {
      return () => h('button', context.attrs, context.slots.default?.())
    },
  },
  InputStub: {
    name: 'UiInputStub',
    inheritAttrs: false,
    setup(_: unknown, context: { attrs: Record<string, unknown> }) {
      return () => h('input', { ...context.attrs, value: context.attrs.modelValue })
    },
  },
}))

// The item only needs the two UI primitives in this test; keeping them as native
// elements makes the assertions about available actions explicit.
vi.mock('@felinic/ui', () => ({ Button: uiStubs.ButtonStub, Input: uiStubs.InputStub }))

import SessionFollowUpQueueItem from './session-follow-up-queue-item.vue'
import { handleBrowserKeyboardShortcut } from '@/lib/browser-keyboard-shortcuts'
import { keyboardBindings, resolveKeyboardBinding } from '@/lib/keyboard-bindings'
import { appKeyboardCommands, createKeyboardCommandRegistry } from '@/lib/keyboard-commands'
import type { EditableFollowUpQueueItem } from './use-session-follow-up-queue'

const baseItem: Omit<EditableFollowUpQueueItem, 'queueKind'> = {
  item_id: 'item-1',
  status: 'accepted' as const,
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

  async function mount(item: EditableFollowUpQueueItem, onSave?: (text: string) => void) {
    root = document.createElement('div')
    document.body.append(root)
    const harness = defineComponent({
      setup() {
        return () => h(SessionFollowUpQueueItem, { item, busy: false, onSave })
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

  it.each(['mac', 'win', 'linux'] as const)('lets the %s focus-input shortcut leave the field instead of saving from the key', async (platform) => {
    const save = vi.fn()
    const el = await mount({ ...baseItem, queueKind: 'follow-up' }, save)
    const composer = document.createElement('textarea')
    document.body.append(composer)
    let savesWhenFocusRan = -1
    const registry = createKeyboardCommandRegistry()
    registry.register(appKeyboardCommands.focusChatInput, () => {
      savesWhenFocusRan = save.mock.calls.length
      composer.focus()
      return true
    })
    const bindings = keyboardBindings.map(binding => resolveKeyboardBinding(binding, platform))
    const dispatch = (event: KeyboardEvent) => { handleBrowserKeyboardShortcut(event, registry, bindings, platform) }
    window.addEventListener('keydown', dispatch)
    try {
      const input = el.querySelector('input')!
      input.focus()
      const chord = bindings.find(binding => binding.command === appKeyboardCommands.focusChatInput)!
      input.dispatchEvent(new KeyboardEvent('keydown', {
        key: chord.key,
        metaKey: platform === 'mac' && !!chord.mod,
        ctrlKey: platform !== 'mac' && !!chord.mod,
        altKey: !!chord.alt,
        shiftKey: !!chord.shift,
        bubbles: true,
        cancelable: true,
      }))
      expect(savesWhenFocusRan).toBe(0)
      expect(document.activeElement).toBe(composer)

      input.focus()
      save.mockClear()
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
      expect(save).toHaveBeenCalledWith('queued input')
    } finally {
      window.removeEventListener('keydown', dispatch)
      composer.remove()
    }
  })
})
