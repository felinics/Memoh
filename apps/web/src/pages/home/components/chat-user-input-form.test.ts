// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */

import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const respondUserInput = vi.fn()

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
    return () => h('input', attrs)
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
})
