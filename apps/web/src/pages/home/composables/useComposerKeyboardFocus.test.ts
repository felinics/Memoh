// @vitest-environment jsdom
import { createApp, h, nextTick, ref } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { KEYBOARD_REGISTRY } from '@/composables/useKeyboardCommand'
import { appKeyboardCommands, createKeyboardCommandRegistry } from '@/lib/keyboard-commands'
import { useComposerKeyboardFocus } from './useComposerKeyboardFocus'

let cleanup = () => {}
afterEach(() => { cleanup(); document.body.replaceChildren() })

function mountComposer() {
  const registry = createKeyboardCommandRegistry()
  const mounted = ref(false)
  const ready = ref(false)
  const enabled = ref(true)
  const available = ref(true)
  const owner = ref('bot-a:chat:session-a')
  const request = ref(false)
  registry.register(appKeyboardCommands.focusChatInput, () => { request.value = true; return true })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup() {
    const textarea = ref<HTMLTextAreaElement | null>(null)
    useComposerKeyboardFocus({
      textarea,
      enabled: () => enabled.value,
      available: () => available.value,
      ready: () => ready.value,
      owner: () => owner.value,
      request: () => request.value,
      consumeRequest: () => { request.value = false },
    })
    return () => mounted.value ? h('textarea', { ref: textarea, disabled: !ready.value }) : null
  } }).provide(KEYBOARD_REGISTRY, registry)
  app.mount(host)
  cleanup = () => { app.unmount(); host.remove() }
  return { registry, mounted, ready, enabled, available, owner, host, request }
}

describe('composer keyboard focus', () => {
  it('retains one focus request until the composer mounts and finishes loading', async () => {
    const view = mountComposer()
    expect(view.registry.dispatch(appKeyboardCommands.focusChatInput)).toBe(true)
    view.mounted.value = true
    await nextTick()
    expect(document.activeElement).toBe(document.body)
    view.ready.value = true
    await nextTick()
    await vi.waitFor(() => expect(document.activeElement).toBe(view.host.querySelector('textarea')))
  })

  it('cancels a pending request when its pane loses ownership', async () => {
    const view = mountComposer()
    view.registry.dispatch(appKeyboardCommands.focusChatInput)
    view.enabled.value = false
    view.enabled.value = true
    view.mounted.value = true
    view.ready.value = true
    await nextTick()
    expect(document.activeElement).toBe(document.body)
    view.registry.dispatch(appKeyboardCommands.focusChatInput)
    await nextTick()
    await vi.waitFor(() => expect(document.activeElement).toBe(view.host.querySelector('textarea')))
  })

  it('does not focus again on later loading transitions', async () => {
    const view = mountComposer()
    view.mounted.value = true
    view.ready.value = true
    await nextTick()
    view.registry.dispatch(appKeyboardCommands.focusChatInput)
    await vi.waitFor(() => expect(document.activeElement).toBe(view.host.querySelector('textarea')))
    view.host.querySelector('textarea')!.blur()
    view.ready.value = false
    await nextTick()
    view.ready.value = true
    await nextTick()
    expect(document.activeElement).toBe(document.body)
  })

  it('does not move focus out of a dialog opened while the composer loads', async () => {
    const view = mountComposer()
    view.registry.dispatch(appKeyboardCommands.focusChatInput)
    const dialog = document.createElement('div')
    dialog.setAttribute('role', 'dialog')
    document.body.append(dialog)
    view.mounted.value = true
    view.ready.value = true
    await nextTick()
    expect(document.activeElement).toBe(document.body)
    dialog.remove()
    view.ready.value = false
    await nextTick()
    view.ready.value = true
    await nextTick()
    expect(document.activeElement).toBe(document.body)
  })

  it('cancels a request when the active panel is repointed to another session', async () => {
    const view = mountComposer()
    view.registry.dispatch(appKeyboardCommands.focusChatInput)
    view.owner.value = 'bot-a:chat:session-b'
    view.mounted.value = true
    view.ready.value = true
    await nextTick()
    expect(document.activeElement).toBe(document.body)
  })

  it('waits for the Dockview container to become visible', async () => {
    const view = mountComposer()
    view.host.style.visibility = 'hidden'
    view.mounted.value = true
    view.ready.value = true
    view.registry.dispatch(appKeyboardCommands.focusChatInput)
    await nextTick()
    expect(document.activeElement).toBe(document.body)
    view.host.style.visibility = 'visible'
    await vi.waitFor(() => expect(document.activeElement).toBe(view.host.querySelector('textarea')))
  })

  it('consumes an ineligible request without retaining it for a future owner', async () => {
    const view = mountComposer()
    view.available.value = false
    view.registry.dispatch(appKeyboardCommands.focusChatInput)
    expect(view.request.value).toBe(false)
    view.owner.value = 'bot-a:chat:session-b'
    view.available.value = true
    view.mounted.value = true
    view.ready.value = true
    await nextTick()
    expect(document.activeElement).toBe(document.body)
  })
})
