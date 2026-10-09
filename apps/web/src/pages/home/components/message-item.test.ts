// @vitest-environment jsdom
/* eslint-disable vue/one-component-per-file */

import { createApp, defineComponent, h, nextTick, ref, type App } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { handleBrowserKeyboardShortcut } from '@/lib/browser-keyboard-shortcuts'
import { keyboardBindings, resolveKeyboardBinding } from '@/lib/keyboard-bindings'
import { appKeyboardCommands, createKeyboardCommandRegistry } from '@/lib/keyboard-commands'

const Stub = defineComponent({ inheritAttrs: false, setup: () => () => h('div') })

vi.mock('markstream-vue', () => ({ default: Stub, setCustomComponents: vi.fn(), enableKatex: vi.fn(), enableMermaid: vi.fn() }))
vi.mock('@/components/markdown', () => ({ registerSharedMarkdownComponents: vi.fn() }))
vi.mock('@/components/themed-mermaid-block/index.vue', () => ({ default: Stub }))
vi.mock('@/components/chat-list/channel-badge/index.vue', () => ({ default: Stub }))
for (const child of ['chat-code-block', 'tool-call-group', 'chat-answers-card', 'attachment-block', 'background-task-block', 'dependency-missing-block']) {
  vi.doMock(`./${child}.vue`, () => ({ default: Stub }))
}
vi.mock('./collapsible-user-text.vue', () => ({
  default: defineComponent({ props: { text: String }, setup: props => () => h('p', props.text) }),
}))
vi.mock('./message-actions.vue', () => ({
  default: defineComponent({
    props: { onEdit: Function },
    setup: props => () => h('button', { 'data-edit': '', onClick: () => props.onEdit?.() }),
  }),
}))
vi.mock('@felinic/ui', () => ({
  Avatar: Stub, AvatarImage: Stub, AvatarFallback: Stub, Button: Stub, CalloutBanner: Stub,
  Textarea: defineComponent({
    inheritAttrs: false,
    props: { modelValue: String },
    emits: ['update:modelValue'],
    setup: (props, { attrs, emit }) => () => h('textarea', {
      ...attrs,
      value: props.modelValue,
      onInput: (event: Event) => emit('update:modelValue', (event.target as HTMLTextAreaElement).value),
    }),
  }),
}))
vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key, te: () => false, locale: ref('en') }),
}))
vi.mock('@/store/settings', () => ({ useSettingsStore: () => ({}) }))
vi.mock('@/store/user', () => ({ useUserStore: () => ({ userInfo: {} }) }))
vi.mock('@vueuse/core', async importOriginal => ({
  ...await importOriginal<typeof import('@vueuse/core')>(),
  useElementVisibility: () => ref(false),
}))

let app: App | undefined
let root: HTMLDivElement | undefined
afterEach(() => {
  app?.unmount()
  root?.remove()
})

describe('message-item edit', () => {
  it.each(['mac', 'win', 'linux'] as const)('leaves the %s focus-input shortcut to the workbench instead of resubmitting the edit', async (platform) => {
    const registry = createKeyboardCommandRegistry()
    const focusChatInput = vi.fn(() => true)
    registry.register(appKeyboardCommands.focusChatInput, focusChatInput)
    const bindings = keyboardBindings.map(binding => resolveKeyboardBinding(binding, platform))
    const dispatch = (event: KeyboardEvent) => { handleBrowserKeyboardShortcut(event, registry, bindings, platform) }
    window.addEventListener('keydown', dispatch)
    try {
      const editMessage = vi.fn()
      const MessageItem = (await import('./message-item.vue')).default
      root = document.createElement('div')
      document.body.append(root)
      app = createApp(MessageItem, {
        message: { id: 'm1', turnId: 't1', role: 'user', text: 'hello', attachments: [], messages: [], timestamp: new Date() },
        canEditLatestUser: true,
        isScrolling: false,
        onEditMessage: editMessage,
      })
      app.config.globalProperties.$t = (key: string) => key
      app.mount(root)
      await nextTick()
      root.querySelector<HTMLButtonElement>('[data-edit]')!.click()
      await nextTick()
      const textarea = root.querySelector('textarea')!

      const chord = bindings.find(binding => binding.command === appKeyboardCommands.focusChatInput)!
      textarea.dispatchEvent(new KeyboardEvent('keydown', {
        key: chord.key,
        metaKey: platform === 'mac' && !!chord.mod,
        ctrlKey: platform !== 'mac' && !!chord.mod,
        altKey: !!chord.alt,
        shiftKey: !!chord.shift,
        bubbles: true,
        cancelable: true,
      }))
      expect(editMessage).not.toHaveBeenCalled()
      expect(focusChatInput).toHaveBeenCalledOnce()

      const submit = new KeyboardEvent('keydown', { key: 'Enter', metaKey: platform === 'mac', ctrlKey: platform !== 'mac', bubbles: true, cancelable: true })
      textarea.dispatchEvent(submit)
      expect(editMessage).toHaveBeenCalledOnce()
      expect(editMessage.mock.calls[0]!.slice(0, 2)).toEqual(['t1', 'hello'])
    } finally {
      window.removeEventListener('keydown', dispatch)
    }
  })
})
