// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { connectBrowserKeyboardShortcutsLive } from '../../../../web/src/lib/browser-keyboard-shortcuts'
import { keyboardBindings } from '../../../../web/src/lib/keyboard-bindings'
import { appKeyboardCommands, createKeyboardCommandRegistry } from '../../../../web/src/lib/keyboard-commands'
import { canDispatchKeyboardCommand, selectActiveKeyboardBindings } from '../../../../web/src/lib/keyboard-context'

let disconnect = () => {}
afterEach(() => { disconnect(); document.body.replaceChildren(); vi.restoreAllMocks() })

function setup(bindings = keyboardBindings) {
  const registry = createKeyboardCommandRegistry(command => canDispatchKeyboardCommand(command, { name: 'home', path: '/' }))
  const close = vi.fn(() => true)
  registry.register(appKeyboardCommands.closeCurrentWorkspaceTab, close)
  disconnect = connectBrowserKeyboardShortcutsLive(registry, () => selectActiveKeyboardBindings(bindings))
  return { registry, close }
}

function closeKey(target: Element = document.body) {
  const event = new KeyboardEvent('keydown', { key: 'w', ctrlKey: true, bubbles: true, cancelable: true })
  target.dispatchEvent(event)
  return event
}

describe('desktop DOM shortcut ownership', () => {
  it.each(['preventDefault', 'stopPropagation'] as const)('preserves a child control that consumes Close via %s', (method) => {
    const { close } = setup()
    const input = document.createElement('textarea')
    document.body.append(input)
    input.addEventListener('keydown', event => event[method]())
    closeKey(input)
    expect(close).not.toHaveBeenCalled()
    expect(closeKey().defaultPrevented).toBe(true)
    expect(close).toHaveBeenCalledOnce()
  })

  it('keeps AltGraph on the original event and declines the host command', () => {
    const { close } = setup(keyboardBindings.map(binding => binding.command === appKeyboardCommands.closeCurrentWorkspaceTab
      ? { ...binding, key: 'w', alt: true } : binding))
    const event = new KeyboardEvent('keydown', { key: 'w', ctrlKey: true, altKey: true, bubbles: true, cancelable: true })
    vi.spyOn(event, 'getModifierState').mockImplementation(key => key === 'AltGraph')
    document.body.dispatchEvent(event)
    expect(close).not.toHaveBeenCalled()
    expect(event.defaultPrevented).toBe(false)
  })

  it('resolves a shared Close key in the active media scope before the workspace', () => {
    const bindings = keyboardBindings.map(binding => binding.command === appKeyboardCommands.closeMediaLightbox
      ? { ...binding, key: 'w', mod: true } : binding)
    const { registry, close } = setup(bindings)
    const media = vi.fn(() => true)
    registry.register(appKeyboardCommands.closeMediaLightbox, media)
    const dialog = document.createElement('div')
    dialog.setAttribute('role', 'dialog')
    dialog.dataset.keyboardScope = 'mediaLightbox'
    document.body.append(dialog)
    closeKey(dialog)
    expect(media).toHaveBeenCalledOnce()
    expect(close).not.toHaveBeenCalled()
    dialog.remove()
    closeKey()
    expect(close).toHaveBeenCalledOnce()
  })
})
