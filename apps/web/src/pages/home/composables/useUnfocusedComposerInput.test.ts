// @vitest-environment jsdom
import { createApp, defineComponent, h, ref } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useUnfocusedComposerInput } from './useUnfocusedComposerInput'

let cleanup: (() => void) | undefined
function setup(enabled: boolean | (() => boolean) = true, onPaste = vi.fn()) {
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(defineComponent({
    setup() {
      const textarea = ref<HTMLTextAreaElement | null>(null)
      useUnfocusedComposerInput({ textarea, enabled: () => typeof enabled === 'function' ? enabled() : enabled, onPaste })
      return () => h('textarea', { ref: textarea })
    },
  }))
  app.mount(host)
  const textarea = host.querySelector('textarea')!
  vi.spyOn(textarea, 'getClientRects').mockReturnValue([{}] as unknown as DOMRectList)
  cleanup = () => { app.unmount(); host.remove() }
  return textarea
}
function paste(target: Element, text = 'Typeless') {
  const event = new Event('paste', { bubbles: true, cancelable: true })
  Object.defineProperty(event, 'clipboardData', { value: { getData: () => text } })
  target.dispatchEvent(event)
  return event
}
afterEach(() => { cleanup?.(); document.body.replaceChildren(); vi.restoreAllMocks() })

describe('unfocused composer input', () => {
  it('focuses for printable typing without canceling native insertion', () => {
    const textarea = setup()
    const event = new KeyboardEvent('keydown', { key: 'a', bubbles: true, cancelable: true })
    document.body.dispatchEvent(event)
    expect(document.activeElement).toBe(textarea)
    expect(event.defaultPrevented).toBe(false)
  })
  it('inserts pasted text at the saved selection and notifies v-model', () => {
    const textarea = setup()
    textarea.value = 'hello world'
    textarea.setSelectionRange(6, 11)
    const input = vi.fn()
    textarea.addEventListener('input', input)
    expect(paste(document.body).defaultPrevented).toBe(true)
    expect(textarea.value).toBe('hello Typeless')
    expect(input).toHaveBeenCalledOnce()
  })
  it('preserves existing large-paste handling', () => {
    const textarea = setup(true, vi.fn((event: Event) => event.preventDefault()))
    paste(document.body)
    expect(textarea.value).toBe('')
  })
  it.each(['input', 'textarea', '[contenteditable]', '[role="textbox"]', '.xterm'])('does not steal from %s', (selector) => {
    const textarea = setup()
    const owner = document.createElement(selector === 'input' || selector === 'textarea' ? selector : 'div')
    if (selector === '[contenteditable]') owner.setAttribute('contenteditable', 'true')
    if (selector === '[role="textbox"]') owner.setAttribute('role', 'textbox')
    if (selector === '.xterm') owner.className = 'xterm'
    document.body.append(owner)
    expect(paste(owner).defaultPrevented).toBe(false)
    expect(textarea.value).toBe('')
  })
  it('preserves the focused editor even when injected events target the body', () => {
    const textarea = setup()
    const input = document.createElement('input')
    document.body.append(input)
    input.focus()
    expect(paste(document.body).defaultPrevented).toBe(false)
    expect(textarea.value).toBe('')
  })
  it('ignores shortcuts and button activation', () => {
    const textarea = setup()
    const button = document.createElement('button')
    document.body.append(button)
    button.focus()
    button.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true }))
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', ctrlKey: true, bubbles: true }))
    expect(document.activeElement).toBe(button)
    expect(textarea.value).toBe('')
  })
  it('does not capture behind an open overlay', () => {
    const textarea = setup()
    const dialog = document.createElement('div')
    dialog.setAttribute('role', 'dialog')
    document.body.append(dialog)
    vi.spyOn(dialog, 'getClientRects').mockReturnValue([{}] as unknown as DOMRectList)
    expect(paste(document.body).defaultPrevented).toBe(false)
    expect(textarea.value).toBe('')
  })
  it('ignores inactive, disabled and hidden composers', () => {
    const textarea = setup(false)
    expect(paste(document.body).defaultPrevented).toBe(false)
    expect(textarea.value).toBe('')
    cleanup?.()
    const active = setup()
    active.disabled = true
    expect(paste(document.body).defaultPrevented).toBe(false)
    active.disabled = false
    vi.mocked(active.getClientRects).mockReturnValue([] as unknown as DOMRectList)
    expect(paste(document.body).defaultPrevented).toBe(false)
  })
  it('handles direct text insertion events once', () => {
    const textarea = setup()
    const event = new InputEvent('beforeinput', { inputType: 'insertText', data: '你好', bubbles: true, cancelable: true })
    document.body.dispatchEvent(event)
    expect(textarea.value).toBe('你好')
    expect(event.defaultPrevented).toBe(true)
  })
  it('stops capture when the application hides a still-mounted chat pane', () => {
    let chatRoute = true
    const textarea = setup(() => chatRoute)
    chatRoute = false
    expect(paste(document.body).defaultPrevented).toBe(false)
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', bubbles: true }))
    expect(document.activeElement).not.toBe(textarea)
    expect(textarea.value).toBe('')
    chatRoute = true
    paste(document.body)
    expect(textarea.value).toBe('Typeless')
  })
  it('allows AltGr text without treating Ctrl+Alt shortcuts as text', () => {
    const textarea = setup()
    const shortcut = new KeyboardEvent('keydown', { key: 'a', ctrlKey: true, altKey: true, bubbles: true })
    document.body.dispatchEvent(shortcut)
    expect(document.activeElement).not.toBe(textarea)
    const text = new KeyboardEvent('keydown', { key: '@', ctrlKey: true, altKey: true, bubbles: true })
    vi.spyOn(text, 'getModifierState').mockImplementation(key => key === 'AltGraph')
    document.body.dispatchEvent(text)
    expect(document.activeElement).toBe(textarea)
  })
  it.each(['€', 'Dead'])('allows macOS Option-produced %s', (key) => {
    vi.spyOn(window.navigator, 'platform', 'get').mockReturnValue('MacIntel')
    const textarea = setup()
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key, altKey: true, bubbles: true }))
    expect(document.activeElement).toBe(textarea)
  })
  it('removes listeners on unmount', () => {
    const textarea = setup()
    cleanup?.()
    cleanup = undefined
    expect(paste(document.body).defaultPrevented).toBe(false)
    expect(textarea.value).toBe('')
  })
})
