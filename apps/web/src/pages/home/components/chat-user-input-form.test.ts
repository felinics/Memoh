// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */

import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { handleBrowserKeyboardShortcut } from '@/lib/browser-keyboard-shortcuts'
import { keyboardBindings, resolveKeyboardBinding } from '@/lib/keyboard-bindings'
import { appKeyboardCommands, createKeyboardCommandRegistry } from '@/lib/keyboard-commands'

const respondUserInput = vi.fn()
const openBrowserAt = vi.fn(() => true)

const ButtonStub = defineComponent({
  name: 'UiButtonStub',
  inheritAttrs: false,
  setup(_, { attrs, slots }) {
    return () => h('button', attrs, slots.default?.())
  },
})

const InputStub = defineComponent({
  name: 'UiInputStub',
  inheritAttrs: false,
  setup(_, { attrs }) {
    return () => h('input', {
      ...attrs,
      value: attrs.modelValue,
      onInput: (event: Event) => (attrs['onUpdate:modelValue'] as (value: string) => void)((event.target as HTMLInputElement).value),
    })
  },
})

vi.mock('@felinic/ui', () => ({
  Button: ButtonStub,
  Input: InputStub,
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('@/store/workspace-tabs', () => ({
  useWorkspaceTabsStore: () => ({ openBrowserAt }),
}))

vi.mock('@/store/chat-list', () => ({
  useChatStore: () => ({ respondUserInput }),
}))

vi.mock('../composables/useChatViewContext', () => ({
  useChatViewTarget: () => ref({ botId: 'bot-1', sessionId: 'session-1', viewId: 'view-1' }),
}))

describe('chat-user-input-form', () => {
  let app: ReturnType<typeof createApp> | undefined
  let root: HTMLDivElement | undefined

  beforeEach(() => {
    respondUserInput.mockReset()
    openBrowserAt.mockClear()
  })

  afterEach(() => {
    app?.unmount()
    root?.remove()
    app = undefined
    root = undefined
  })

  async function mount(component: Parameters<typeof createApp>[0], props: Record<string, unknown>) {
    root = document.createElement('div')
    document.body.append(root)
    app = createApp(component, props)
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    await nextTick()
    return root
  }

  it('keeps the composer hidden when an option is selected', async () => {
    const revealComposer = vi.fn()
    const ChatUserInputForm = (await import('./chat-user-input-form.vue')).default
    const el = await mount(ChatUserInputForm, {
      userInput: {
        user_input_id: 'input-1',
        status: 'pending',
        questions: [{
          id: 'q1',
          text: 'Choose one',
          kind: 'single_select',
          options: [{ id: 'q1.o1', label: 'One' }, { id: 'q1.o2', label: 'Two' }],
        }],
      },
      onRevealComposer: revealComposer,
    })

    const firstOption = el.querySelector<HTMLButtonElement>('[role="radio"]')
    expect(firstOption).not.toBeNull()
    firstOption!.click()
    await nextTick()

    expect(firstOption!.getAttribute('aria-checked')).toBe('true')
    expect(revealComposer).not.toHaveBeenCalled()
    expect(respondUserInput).not.toHaveBeenCalled()
  })

  it('hands the composer back when the request is canceled', async () => {
    const revealComposer = vi.fn()
    const userInput = {
      user_input_id: 'input-1',
      status: 'pending',
      questions: [{
        id: 'q1',
        text: 'Choose one',
        kind: 'single_select',
        options: [{ id: 'q1.o1', label: 'One' }],
      }],
    }
    const ChatUserInputForm = (await import('./chat-user-input-form.vue')).default
    const el = await mount(ChatUserInputForm, {
      userInput,
      onRevealComposer: revealComposer,
    })

    const cancel = [...el.querySelectorAll<HTMLButtonElement>('button')]
      .find(button => button.textContent === 'chat.tools.cancelUserInput')
    expect(cancel).toBeDefined()
    cancel!.click()
    await nextTick()

    expect(revealComposer).toHaveBeenCalledWith({ focus: true })
    expect(respondUserInput).toHaveBeenCalledWith(userInput, {
      canceled: true,
      reason: 'user_canceled',
    }, {
      botId: 'bot-1',
      sessionId: 'session-1',
      viewId: 'view-1',
    })
  })
  it.each(['mac', 'win', 'linux'] as const)('leaves the %s focus-input shortcut to the workbench instead of submitting the answer', async (platform) => {
    const registry = createKeyboardCommandRegistry()
    const focusChatInput = vi.fn(() => true)
    registry.register(appKeyboardCommands.focusChatInput, focusChatInput)
    const bindings = keyboardBindings.map(binding => resolveKeyboardBinding(binding, platform))
    const dispatch = (event: KeyboardEvent) => { handleBrowserKeyboardShortcut(event, registry, bindings, platform) }
    window.addEventListener('keydown', dispatch)
    try {
      const ChatUserInputForm = (await import('./chat-user-input-form.vue')).default
      const el = await mount(ChatUserInputForm, {
        userInput: { user_input_id: 'input-key', status: 'pending', questions: [{ id: 'q1', kind: 'text', text: 'Name?' }] },
      })
      const input = el.querySelector('input')!
      input.value = 'Ada'
      input.dispatchEvent(new Event('input'))
      await nextTick()
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
      expect(respondUserInput).not.toHaveBeenCalled()
      expect(focusChatInput).toHaveBeenCalledOnce()

      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
      expect(respondUserInput).toHaveBeenCalledOnce()
      expect(respondUserInput.mock.calls[0]![1]).toEqual({ answers: [{ question_id: 'q1', text: 'Ada' }] })
    } finally {
      window.removeEventListener('keydown', dispatch)
    }
  })

  it('keeps question text verbatim and routes local links into the workspace', async () => {
    const ChatUserInputForm = (await import('./chat-user-input-form.vue')).default
    const text = 'Use i<n or i<=n? __init__.py http://localhost:3000/test https://example.com'
    const el = await mount(ChatUserInputForm, {
      userInput: { user_input_id: 'input-link', questions: [{ id: 'q1', kind: 'text', text }] },
    })
    expect(el.querySelector('p')!.textContent).toBe(text)
    const [local, external] = el.querySelectorAll<HTMLAnchorElement>('a')
    const normal = new MouseEvent('click', { bubbles: true, cancelable: true })
    local!.dispatchEvent(normal)
    expect(normal.defaultPrevented).toBe(true)
    expect(openBrowserAt).toHaveBeenCalledWith('localhost:3000/test')
    const modified = new MouseEvent('click', { bubbles: true, cancelable: true, ctrlKey: true })
    local!.dispatchEvent(modified)
    external!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    expect(modified.defaultPrevented).toBe(false)
    expect(openBrowserAt).toHaveBeenCalledTimes(1)
  })

})
