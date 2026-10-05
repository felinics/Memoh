import { describe, expect, it, vi } from 'vitest'
import { appKeyboardCommands, type KeyboardCommandRegistry } from './keyboard-commands'
import { keyboardBindings } from './keyboard-bindings'
import { formatKeyCombo, keyComboFromEvent, parseKeyCombo } from './keyboard-combo'
import {
  connectBrowserKeyboardShortcutsLive,
  handleBrowserKeyboardShortcut,
  type BrowserKeyboardShortcutBinding,
} from './browser-keyboard-shortcuts'

function createKeyboardEventLike(options: {
  key: string
  metaKey?: boolean
  ctrlKey?: boolean
  altKey?: boolean
  shiftKey?: boolean
}) {
  return {
    key: options.key,
    metaKey: options.metaKey ?? false,
    ctrlKey: options.ctrlKey ?? false,
    altKey: options.altKey ?? false,
    shiftKey: options.shiftKey ?? false,
    preventDefault: vi.fn(),
  }
}

function createRegistry(handled = true): Pick<KeyboardCommandRegistry, 'dispatch'> {
  return {
    dispatch: vi.fn(() => handled),
  }
}

const saveBinding: BrowserKeyboardShortcutBinding = {
  command: appKeyboardCommands.saveActiveFile,
  key: 's',
  mod: true,
}

describe('browser keyboard shortcuts matcher', () => {
  it('matches a mod binding via Cmd on macOS', () => {
    const registry = createRegistry(true)
    const event = createKeyboardEventLike({ key: 's', metaKey: true })

    expect(handleBrowserKeyboardShortcut(event, registry, [saveBinding], 'mac')).toBe(true)
    expect(registry.dispatch).toHaveBeenCalledWith(appKeyboardCommands.saveActiveFile)
    expect(event.preventDefault).toHaveBeenCalledOnce()
  })

  it('matches a mod binding via Ctrl on Windows/Linux', () => {
    const registry = createRegistry(true)
    const winEvent = createKeyboardEventLike({ key: 's', ctrlKey: true })
    const linuxEvent = createKeyboardEventLike({ key: 's', ctrlKey: true })

    expect(handleBrowserKeyboardShortcut(winEvent, registry, [saveBinding], 'win')).toBe(true)
    expect(handleBrowserKeyboardShortcut(linuxEvent, registry, [saveBinding], 'linux')).toBe(true)
  })

  it('on macOS, Ctrl does NOT satisfy a mod binding (mod is Cmd, not "meta or ctrl")', () => {
    const registry = createRegistry()
    const event = createKeyboardEventLike({ key: 's', ctrlKey: true })

    expect(handleBrowserKeyboardShortcut(event, registry, [saveBinding], 'mac')).toBe(false)
    expect(registry.dispatch).not.toHaveBeenCalled()
  })

  it('on Windows/Linux, Meta does NOT satisfy a mod binding', () => {
    const registry = createRegistry()
    const event = createKeyboardEventLike({ key: 's', metaKey: true })

    expect(handleBrowserKeyboardShortcut(event, registry, [saveBinding], 'linux')).toBe(false)
    expect(registry.dispatch).not.toHaveBeenCalled()
  })

  it('requires the opposite platform key absent (Cmd+Ctrl+S is not Cmd+S)', () => {
    const registry = createRegistry()
    const event = createKeyboardEventLike({ key: 's', metaKey: true, ctrlKey: true })

    expect(handleBrowserKeyboardShortcut(event, registry, [saveBinding], 'mac')).toBe(false)
    expect(registry.dispatch).not.toHaveBeenCalled()
  })

  it('leaves browser defaults alone when the matched command is unhandled', () => {
    const registry = createRegistry(false)
    const event = createKeyboardEventLike({ key: 's', metaKey: true })

    expect(handleBrowserKeyboardShortcut(event, registry, [saveBinding], 'mac')).toBe(false)
    expect(registry.dispatch).toHaveBeenCalledWith(appKeyboardCommands.saveActiveFile)
    expect(event.preventDefault).not.toHaveBeenCalled()
  })

  it('does nothing when given no bindings (passthrough exclusion happens at selection)', () => {
    const registry = createRegistry()
    const event = createKeyboardEventLike({ key: 'w', metaKey: true })

    expect(handleBrowserKeyboardShortcut(event, registry, [], 'mac')).toBe(false)
    expect(registry.dispatch).not.toHaveBeenCalled()
    expect(event.preventDefault).not.toHaveBeenCalled()
  })

  it('has no internal reserved-key list: a mod+w binding passed in is dispatched', () => {
    const registry = createRegistry(true)
    const event = createKeyboardEventLike({ key: 'w', metaKey: true })

    expect(handleBrowserKeyboardShortcut(event, registry, [{
      command: appKeyboardCommands.closeCurrentWorkspaceTab,
      key: 'w',
      mod: true,
    }], 'mac')).toBe(true)
    expect(registry.dispatch).toHaveBeenCalledWith(appKeyboardCommands.closeCurrentWorkspaceTab)
    expect(event.preventDefault).toHaveBeenCalledOnce()
  })

  it('does not match when a required modifier is absent', () => {
    const registry = createRegistry()
    const event = createKeyboardEventLike({ key: 's' })

    expect(handleBrowserKeyboardShortcut(event, registry, [saveBinding], 'mac')).toBe(false)
    expect(registry.dispatch).not.toHaveBeenCalled()
  })

  it.each([
    { defaultPrevented: true },
    { isComposing: true },
    { keyCode: 229 },
    { getModifierState: (key: string) => key === 'AltGraph' },
  ])('leaves consumed events and text composition alone (%j)', (state) => {
    const registry = createRegistry()
    const event = { ...createKeyboardEventLike({ key: 's', ctrlKey: true }), ...state }

    expect(handleBrowserKeyboardShortcut(event, registry, [saveBinding], 'linux')).toBe(false)
    expect(registry.dispatch).not.toHaveBeenCalled()
    expect(event.preventDefault).not.toHaveBeenCalled()
  })

  it('consumes a held shortcut without executing the command again', () => {
    const registry = createRegistry()
    const event = { ...createKeyboardEventLike({ key: 's', ctrlKey: true }), repeat: true }

    expect(handleBrowserKeyboardShortcut(event, registry, [saveBinding], 'linux')).toBe(true)
    expect(registry.dispatch).not.toHaveBeenCalled()
    expect(event.preventDefault).toHaveBeenCalledOnce()
  })

  it('allows repeat when a navigation binding explicitly requests it', () => {
    const registry = createRegistry()
    const event = { ...createKeyboardEventLike({ key: 's', ctrlKey: true }), repeat: true }

    expect(handleBrowserKeyboardShortcut(event, registry, [{ ...saveBinding, repeat: true }], 'linux')).toBe(true)
    expect(registry.dispatch).toHaveBeenCalledOnce()
  })

  it('matches the per-platform key override for the active platform', () => {
    const registry = createRegistry(true)
    const binding: BrowserKeyboardShortcutBinding = {
      command: appKeyboardCommands.closeCurrentWorkspaceTab,
      key: 'w',
      win: 'F4',
      mod: true,
    }
    const f4 = createKeyboardEventLike({ key: 'F4', ctrlKey: true })
    const w = createKeyboardEventLike({ key: 'w', ctrlKey: true })

    expect(handleBrowserKeyboardShortcut(f4, registry, [binding], 'win')).toBe(true)
    // On Windows the base 'w' no longer matches because the override took effect.
    expect(handleBrowserKeyboardShortcut(w, createRegistry(), [binding], 'win')).toBe(false)
  })

  it('continues past an unhandled binding to a later one that claims the command', () => {
    // Two bindings share Escape: a scoped lightbox command (no live handler)
    // and a global with a live handler. The dispatcher should keep iterating
    // instead of bailing on the first unhandled match.
    const dispatch = vi.fn()
      .mockImplementationOnce(() => false)
      .mockImplementationOnce(() => true)
    const registry = { dispatch }
    const event = createKeyboardEventLike({ key: 'Escape' })
    const bindings: BrowserKeyboardShortcutBinding[] = [
      { command: appKeyboardCommands.closeMediaLightbox, key: 'Escape' },
      { command: appKeyboardCommands.toggleSidebar, key: 'Escape' },
    ]

    expect(handleBrowserKeyboardShortcut(event, registry, bindings, 'mac')).toBe(true)
    expect(dispatch).toHaveBeenCalledTimes(2)
    expect(event.preventDefault).toHaveBeenCalledOnce()
  })
})

describe('macOS Command+Option shortcuts', () => {
  const optionKeys: Record<string, { key: string; code: string }> = {
    'n': { key: 'Dead', code: 'KeyN' },
    'Enter': { key: 'Enter', code: 'Enter' },
    '1': { key: '¡', code: 'Digit1' },
    '2': { key: '™', code: 'Digit2' },
    '3': { key: '£', code: 'Digit3' },
    '4': { key: '¢', code: 'Digit4' },
    ']': { key: '‘', code: 'BracketRight' },
    '[': { key: '“', code: 'BracketLeft' },
    '.': { key: '≥', code: 'Period' },
    ',': { key: '≤', code: 'Comma' },
    'x': { key: '≈', code: 'KeyX' },
    'o': { key: 'ø', code: 'KeyO' },
  }
  const defaults = keyboardBindings.filter(binding => binding.mod && binding.alt)

  it.each(defaults.map(binding => [binding.command, binding] as const))('dispatches %s from the Option-modified key', (_command, binding) => {
    const registry = createRegistry(true)
    const event = { ...createKeyboardEventLike({ ...optionKeys[binding.key]!, metaKey: true, altKey: true }), code: optionKeys[binding.key]!.code }

    expect(handleBrowserKeyboardShortcut(event, registry, defaults, 'mac')).toBe(true)
    expect(registry.dispatch).toHaveBeenCalledWith(binding.command)
  })

  it('accepts Firefox reporting Option as AltGraph while Command is held', () => {
    const registry = createRegistry(true)
    const event = {
      ...createKeyboardEventLike({ key: '¡', metaKey: true, altKey: true }),
      code: 'Digit1',
      getModifierState: (key: string) => key === 'AltGraph',
    }

    expect(handleBrowserKeyboardShortcut(event, registry, defaults, 'mac')).toBe(true)
    expect(registry.dispatch).toHaveBeenCalledWith(appKeyboardCommands.showSessions)
  })

  it('dispatches a Command+Option combo recorded on the same Mac', () => {
    const press = { ...createKeyboardEventLike({ key: '√', metaKey: true, altKey: true }), code: 'KeyV' }
    const combo = parseKeyCombo(formatKeyCombo(keyComboFromEvent(press, true)!))!
    const registry = createRegistry(true)

    expect(handleBrowserKeyboardShortcut(press, registry, [{ command: appKeyboardCommands.newBrowser, ...combo }], 'mac')).toBe(true)
    expect(registry.dispatch).toHaveBeenCalledWith(appKeyboardCommands.newBrowser)
  })

  it('keeps an override captured as the Option character working', () => {
    const registry = createRegistry(true)
    const event = { ...createKeyboardEventLike({ key: '∫', metaKey: true, altKey: true }), code: 'KeyB' }

    expect(handleBrowserKeyboardShortcut(event, registry, [{ command: appKeyboardCommands.toggleSidebar, key: '∫', mod: true, alt: true }], 'mac')).toBe(true)
  })

  it('leaves AltGr text on Windows and Linux to the focused input', () => {
    for (const platform of ['win', 'linux'] as const) {
      const registry = createRegistry(true)
      const event = {
        ...createKeyboardEventLike({ key: 'ó' }),
        code: 'KeyO',
        getModifierState: (key: string) => key === 'AltGraph',
      }

      expect(handleBrowserKeyboardShortcut(event, registry, [{ command: appKeyboardCommands.mediaLightboxNext, key: 'ó' }], platform)).toBe(false)
      expect(registry.dispatch).not.toHaveBeenCalled()
      expect(event.preventDefault).not.toHaveBeenCalled()
    }
  })
})

describe('connectBrowserKeyboardShortcutsLive', () => {
  function fakeTarget() {
    let registered: ((event: KeyboardEvent) => void) | null = null
    return {
      addEventListener: vi.fn((_type: 'keydown', listener: (event: KeyboardEvent) => void) => {
        registered = listener
      }),
      removeEventListener: vi.fn(),
      fire(event: ReturnType<typeof createKeyboardEventLike>) {
        registered?.(event as unknown as KeyboardEvent)
      },
    }
  }

  it('reads bindings from the getter on every keydown so store updates take effect live', () => {
    const registry = createRegistry(true)
    const target = fakeTarget()
    let bindings: BrowserKeyboardShortcutBinding[] = [saveBinding]
    connectBrowserKeyboardShortcutsLive(registry, () => bindings, target)

    // Node test env detects as linux, so mod means ctrlKey here.
    target.fire(createKeyboardEventLike({ key: 's', ctrlKey: true }))
    expect(registry.dispatch).toHaveBeenCalledWith(appKeyboardCommands.saveActiveFile)

    bindings = [{ command: appKeyboardCommands.toggleSidebar, key: 'b', mod: true }]
    target.fire(createKeyboardEventLike({ key: 'b', ctrlKey: true }))
    expect(registry.dispatch).toHaveBeenLastCalledWith(appKeyboardCommands.toggleSidebar)
  })

  it('returns a disposer that detaches the listener', () => {
    const registry = createRegistry()
    const target = fakeTarget()
    const dispose = connectBrowserKeyboardShortcutsLive(registry, () => [saveBinding], target)
    dispose()
    expect(target.removeEventListener).toHaveBeenCalledOnce()
  })
})
