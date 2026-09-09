import { onBeforeUnmount, onMounted, type Ref } from 'vue'
import { detectPlatform } from '@/lib/keyboard-bindings'

const INPUT_OWNER = 'input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="textbox"], [role="combobox"], [role="listbox"], [role="menu"], [role="tree"], [role="grid"], [role="slider"], [role="spinbutton"], .monaco-editor, .cm-editor, .xterm'
const OPEN_OVERLAY = '[role="dialog"], [role="alertdialog"], [role="menu"], [role="listbox"]'

/** Only the active chat pane may recover text that otherwise has no input owner. */
export function useUnfocusedComposerInput(options: {
  textarea: Ref<HTMLTextAreaElement | null>
  enabled: () => boolean
  onPaste: (event: ClipboardEvent) => void
}) {
  function destination(event: Event) {
    const textarea = options.textarea.value
    if (event.defaultPrevented || !options.enabled() || !textarea?.isConnected
      || textarea.disabled || textarea.readOnly || !textarea.getClientRects().length) return null
    if (document.activeElement?.closest(INPUT_OWNER)) return null
    const path = event.composedPath()
    if (path.some(node => node instanceof Element && node.closest(INPUT_OWNER))) return null
    // Portalled overlays can own keyboard interaction even while focus is on a trigger.
    if (Array.from(document.querySelectorAll<HTMLElement>(OPEN_OVERLAY))
      .some(node => node.getClientRects().length && getComputedStyle(node).visibility !== 'hidden')) return null
    return textarea
  }

  function insert(textarea: HTMLTextAreaElement, text: string) {
    textarea.focus({ preventScroll: true })
    textarea.setRangeText(text, textarea.selectionStart, textarea.selectionEnd, 'end')
    textarea.dispatchEvent(new Event('input', { bubbles: true }))
  }

  function onKeydown(event: KeyboardEvent) {
    if (event.metaKey || event.isComposing) return
    // AltGr and macOS Option produce text; ordinary Ctrl/Alt shortcuts do not.
    const altGraph = event.getModifierState('AltGraph')
    const optionText = detectPlatform() === 'mac' && event.altKey && !event.ctrlKey
    if ((event.ctrlKey || event.altKey) && !altGraph && !optionText) return
    if (event.key.length !== 1 && event.key !== 'Process' && event.key !== 'Dead') return
    // Space on a focused control belongs to that control, not the composer.
    if (event.key === ' ' && event.composedPath().some(node => node instanceof Element
      && node.closest('button, a, [role="button"], [role="checkbox"], [role="switch"]'))) return
    const textarea = destination(event)
    if (!textarea) return
    // Keep the native default action: the browser inserts into the newly focused
    // textarea, preserving IME/dead-key processing and the native undo stack.
    textarea.focus({ preventScroll: true })
  }

  function onPaste(event: ClipboardEvent) {
    const textarea = destination(event)
    const text = event.clipboardData?.getData('text/plain')
    if (!textarea || !text || !event.cancelable) return
    textarea.focus({ preventScroll: true })
    // Reuse attachment/large-paste policy before handling plain text ourselves.
    options.onPaste(event)
    if (event.defaultPrevented) return
    event.preventDefault()
    insert(textarea, text)
  }

  function onBeforeInput(event: InputEvent) {
    if (event.isComposing || event.inputType !== 'insertText' || !event.data || !event.cancelable) return
    const textarea = destination(event)
    if (!textarea) return
    event.preventDefault()
    insert(textarea, event.data)
  }

  onMounted(() => {
    document.addEventListener('keydown', onKeydown)
    document.addEventListener('paste', onPaste)
    document.addEventListener('beforeinput', onBeforeInput)
  })
  onBeforeUnmount(() => {
    document.removeEventListener('keydown', onKeydown)
    document.removeEventListener('paste', onPaste)
    document.removeEventListener('beforeinput', onBeforeInput)
  })
}
