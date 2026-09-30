import { describe, expect, it, vi } from 'vitest'
import {
  appKeyboardCommands,
  createKeyboardCommandRegistry,
  createScopedKeyboardBinding,
  isAppKeyboardCommand,
  type AppKeyboardCommand,
} from './keyboard-commands'

describe('keyboard command registry', () => {
  it('defines stable app commands and validates command ids', () => {
    expect(isAppKeyboardCommand(appKeyboardCommands.closeCurrentWorkspaceTab)).toBe(true)
    expect(isAppKeyboardCommand(appKeyboardCommands.saveActiveFile)).toBe(true)
    expect(isAppKeyboardCommand('workspace-tab:close-current')).toBe(false)
    expect(isAppKeyboardCommand(null)).toBe(false)
  })

  it('dispatches registered command handlers', () => {
    const registry = createKeyboardCommandRegistry()
    const handler = vi.fn(() => true)

    registry.register(appKeyboardCommands.closeCurrentWorkspaceTab, handler)

    expect(registry.dispatch(appKeyboardCommands.closeCurrentWorkspaceTab)).toBe(true)
    expect(handler).toHaveBeenCalledOnce()
  })

  it('stops after the first handler consumes a command', () => {
    const registry = createKeyboardCommandRegistry()
    const effects: string[] = []
    registry.register(appKeyboardCommands.saveActiveFile, () => false)
    registry.register(appKeyboardCommands.saveActiveFile, () => {
      effects.push('focused')
      return true
    })
    registry.register(appKeyboardCommands.saveActiveFile, () => {
      effects.push('background')
      return true
    })

    expect(registry.dispatch(appKeyboardCommands.saveActiveFile)).toBe(true)
    expect(effects).toEqual(['focused'])
  })

  it('unregisters command handlers', () => {
    const registry = createKeyboardCommandRegistry()
    const handler = vi.fn(() => true)

    const unregister = registry.register(appKeyboardCommands.closeCurrentWorkspaceTab, handler)
    unregister()

    expect(registry.dispatch(appKeyboardCommands.closeCurrentWorkspaceTab)).toBe(false)
    expect(handler).not.toHaveBeenCalled()
  })

  it('connects command sources to the registry', () => {
    const listeners: Array<(command: string) => void> = []
    const unsubscribe = vi.fn()
    const api = {
      onKeyboardCommand: vi.fn((cb: (command: string) => void) => {
        listeners.push(cb)
        return unsubscribe
      }),
    }
    const registry = createKeyboardCommandRegistry()
    const handler = vi.fn(() => true)

    registry.register(appKeyboardCommands.closeCurrentWorkspaceTab, handler)
    const disconnect = registry.connect(api)
    const listener = listeners[0]
    if (!listener) throw new Error('keyboard command listener was not registered')
    listener(appKeyboardCommands.closeCurrentWorkspaceTab)
    disconnect()

    expect(api.onKeyboardCommand).toHaveBeenCalledOnce()
    expect(handler).toHaveBeenCalledOnce()
    expect(unsubscribe).toHaveBeenCalledOnce()
  })

  it('invokes onUnhandled when a connected command has no handler (or none claim it)', () => {
    const listeners: Array<(command: AppKeyboardCommand) => void> = []
    const api = {
      onKeyboardCommand: (cb: (command: AppKeyboardCommand) => void) => {
        listeners.push(cb)
        return () => {}
      },
    }
    const registry = createKeyboardCommandRegistry()
    const onUnhandled = vi.fn()

    registry.connect(api, onUnhandled)
    const listener = listeners[0]
    if (!listener) throw new Error('keyboard command listener was not registered')
    listener(appKeyboardCommands.closeCurrentWorkspaceTab)

    expect(onUnhandled).toHaveBeenCalledWith(appKeyboardCommands.closeCurrentWorkspaceTab)
  })

  it('scoped binding: bind registers, unbind removes (so a deactivated handler stops firing)', () => {
    const registry = createKeyboardCommandRegistry()
    const handler = vi.fn(() => true)
    const binding = createScopedKeyboardBinding(registry, appKeyboardCommands.saveActiveFile, handler)

    binding.bind()
    expect(registry.dispatch(appKeyboardCommands.saveActiveFile)).toBe(true)
    expect(handler).toHaveBeenCalledOnce()

    handler.mockClear()
    binding.unbind()
    expect(registry.dispatch(appKeyboardCommands.saveActiveFile)).toBe(false)
    expect(handler).not.toHaveBeenCalled()
  })

  it('scoped binding: bind is idempotent (mounted + activated fire both)', () => {
    const registry = createKeyboardCommandRegistry()
    const handler = vi.fn(() => true)
    const binding = createScopedKeyboardBinding(registry, appKeyboardCommands.saveActiveFile, handler)

    binding.bind()
    binding.bind()
    registry.dispatch(appKeyboardCommands.saveActiveFile)
    expect(handler).toHaveBeenCalledOnce()

    // A single unbind fully detaches despite the double bind.
    binding.unbind()
    expect(registry.dispatch(appKeyboardCommands.saveActiveFile)).toBe(false)
  })

  it('scoped binding: can re-bind after unbind (KeepAlive re-activate)', () => {
    const registry = createKeyboardCommandRegistry()
    const handler = vi.fn(() => true)
    const binding = createScopedKeyboardBinding(registry, appKeyboardCommands.saveActiveFile, handler)

    binding.bind()
    binding.unbind()
    binding.bind()
    expect(registry.dispatch(appKeyboardCommands.saveActiveFile)).toBe(true)
  })

  it('does not invoke onUnhandled when a handler claims the command', () => {
    const listeners: Array<(command: AppKeyboardCommand) => void> = []
    const api = {
      onKeyboardCommand: (cb: (command: AppKeyboardCommand) => void) => {
        listeners.push(cb)
        return () => {}
      },
    }
    const registry = createKeyboardCommandRegistry()
    const onUnhandled = vi.fn()
    registry.register(appKeyboardCommands.closeCurrentWorkspaceTab, () => true)

    registry.connect(api, onUnhandled)
    listeners[0]?.(appKeyboardCommands.closeCurrentWorkspaceTab)

    expect(onUnhandled).not.toHaveBeenCalled()
  })
})
