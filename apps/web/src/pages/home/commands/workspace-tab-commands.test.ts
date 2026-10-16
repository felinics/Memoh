import { describe, expect, it, vi } from 'vitest'
import { appKeyboardCommands } from '@/lib/keyboard-commands'
import { handleWorkspaceKeyboardCommand, registerWorkspaceTabCommands } from './workspace-tab-commands'

describe('workspace tab commands', () => {
  it('closes the active workspace tab', () => {
    const store = {
      activeId: 'terminal:1',
      requestCloseTab: vi.fn(),
    }

    expect(handleWorkspaceKeyboardCommand(appKeyboardCommands.closeCurrentWorkspaceTab, store)).toBe(true)
    expect(store.requestCloseTab).toHaveBeenCalledWith('terminal:1')
  })

  it('leaves the workspace unchanged when there is no active tab', () => {
    const store = {
      activeId: null,
      requestCloseTab: vi.fn(),
    }

    expect(handleWorkspaceKeyboardCommand(appKeyboardCommands.closeCurrentWorkspaceTab, store)).toBe(false)
    expect(store.requestCloseTab).not.toHaveBeenCalled()
  })

  it('registers the close-tab command with a keyboard registry', () => {
    const handlers = new Map<string, () => boolean>()
    const unregister = vi.fn()
    const registry = {
      register: vi.fn((command: string, handler: () => boolean) => {
        handlers.set(command, handler)
        return unregister
      }),
    }
    const store = {
      activeId: 'browser:1',
      requestCloseTab: vi.fn(),
    }

    const cleanup = registerWorkspaceTabCommands(registry, store)
    const handler = handlers.get(appKeyboardCommands.closeCurrentWorkspaceTab)
    if (!handler) throw new Error('close-tab handler was not registered')

    expect(registry.register).toHaveBeenCalledWith(appKeyboardCommands.closeCurrentWorkspaceTab, expect.any(Function))
    expect(handler()).toBe(true)
    expect(store.requestCloseTab).toHaveBeenCalledWith('browser:1')
    cleanup()
    expect(unregister).toHaveBeenCalledOnce()
  })
})
