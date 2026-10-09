import { nextTick, onDeactivated, ref, watch, type Ref } from 'vue'
import { activeKeyboardDialog, isKeyboardElementVisible } from '@/lib/keyboard-context'

export function useComposerKeyboardFocus(options: {
  textarea: Ref<HTMLTextAreaElement | null>
  enabled: () => boolean
  available: () => boolean
  ready: () => boolean
  owner: () => string
  request: () => boolean
  consumeRequest: () => void
}) {
  const pending = ref(false)
  const cancel = () => { pending.value = false }
  watch(options.enabled, enabled => {
    if (!enabled) { cancel(); if (options.request()) options.consumeRequest() }
  }, { flush: 'sync' })
  watch(options.owner, cancel, { flush: 'sync' })
  watch(options.available, available => { if (!available) cancel() }, { flush: 'sync' })
  onDeactivated(cancel)
  function requestFocus() {
    if (!options.enabled()) return false
    pending.value = true
    return true
  }
  watch([options.request, options.enabled, options.available], ([requested, enabled, available]) => {
    if (!requested) return
    if (available && !enabled) return
    options.consumeRequest()
    if (available) requestFocus()
  }, { immediate: true, flush: 'sync' })
  watch([pending, options.textarea, options.ready], async (_value, _old, onCleanup) => {
    let live = true
    const observer = new MutationObserver(() => { void focus() })
    onCleanup(() => { live = false; observer.disconnect() })
    async function focus() {
      await nextTick()
      const textarea = options.textarea.value
      if (!live || !pending.value || !options.enabled() || !options.available() || !options.ready() || !textarea) return
      if (activeKeyboardDialog()) return cancel()
      if (!isKeyboardElementVisible(textarea) || textarea.disabled || textarea.readOnly) return
      textarea.focus()
      if (document.activeElement === textarea) pending.value = false
    }
    await nextTick()
    const textarea = options.textarea.value
    if (!live || !pending.value || !textarea) return
    for (let element: HTMLElement | null = textarea; element; element = element.parentElement) {
      observer.observe(element, { attributes: true, attributeFilter: ['style', 'class', 'hidden', 'aria-hidden', 'disabled', 'readonly'] })
    }
    await focus()
  }, { immediate: true, flush: 'post' })
}
