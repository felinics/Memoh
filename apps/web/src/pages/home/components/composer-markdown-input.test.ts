// @vitest-environment jsdom
import { createApp, h, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import ComposerMarkdownInput from './composer-markdown-input.vue'
import type { ComposerInputHandle } from '../composables/composer-input-target'

let dispose: (() => void) | undefined
afterEach(() => { dispose?.(); vi.unstubAllGlobals() })

it('publishes edits before send, restores external drafts, and lets chat own file paste', async () => {
  // jsdom has no layout observer; real scroll/resize behavior is covered in browser QA.
  vi.stubGlobal('ResizeObserver', class {
    observe() {}
    disconnect() {}
  })
  const host = document.createElement('div')
  document.body.append(host)
  const text = ref('')
  const input = ref<ComposerInputHandle | null>(null)
  const onPaste = vi.fn((event: ClipboardEvent) => event.preventDefault())
  const app = createApp({ render: () => h(ComposerMarkdownInput, {
    ref: input, modelValue: text.value, disabled: false, placeholder: 'Ask anything',
    'onUpdate:modelValue': value => { text.value = value }, onPaste,
  }) })
  dispose = () => { app.unmount(); host.remove() }
  app.mount(host)
  await vi.waitFor(() => expect(input.value?.disabled).toBe(false))
  input.value!.insertText('中文 draft')
  expect(text.value).toBe('中文 draft')
  expect(host.querySelector('.chat-cjk')?.textContent).toBe('中文 ')
  expect(host.querySelector('.chat-latin')?.textContent).toBe('draft')
  await nextTick()
  expect(host.querySelector('.ProseMirror')?.textContent).toBe('中文 draft')
  text.value = '# 新草稿'
  await nextTick()
  expect(host.querySelector('h1')?.textContent).toBe('新草稿')
  text.value = ''
  await nextTick()
  expect(host.querySelector('.ProseMirror')?.textContent).toBe('')
  const paste = new Event('paste', { bubbles: true, cancelable: true })
  host.querySelector('.ProseMirror')!.dispatchEvent(paste)
  expect(onPaste).toHaveBeenCalledOnce()
  expect(paste.defaultPrevented).toBe(true)
})
