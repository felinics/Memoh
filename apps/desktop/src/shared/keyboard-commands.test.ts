import { describe, expect, it } from 'vitest'
import { handleBrowserKeyboardShortcut } from '../../../web/src/lib/browser-keyboard-shortcuts'
import { toElectronAccelerator } from '../../../web/src/lib/keyboard-bindings'
import {
  DESKTOP_KEYBOARD_COMMAND_CHANNEL,
  appKeyboardCommands,
  isAppKeyboardCommand,
  matchesMenuAccelerator,
} from './keyboard-commands'

describe('desktop keyboard command transport', () => {
  it('defines a stable desktop IPC channel and reuses app command validation', () => {
    expect(DESKTOP_KEYBOARD_COMMAND_CHANNEL).toBe('desktop:keyboard-command')
    expect(isAppKeyboardCommand(appKeyboardCommands.closeCurrentWorkspaceTab)).toBe(true)
    expect(isAppKeyboardCommand('workspace-tab:close-current')).toBe(false)
    expect(isAppKeyboardCommand(null)).toBe(false)
  })

  it('matches the physical platform modifier without accepting the opposite or extra modifiers', () => {
    const input = { key: 'g', ctrlKey: true, metaKey: false, altKey: false, shiftKey: false }
    expect(matchesMenuAccelerator(input, 'CmdOrCtrl+G', 'linux')).toBe(true)
    expect(matchesMenuAccelerator(input, 'CmdOrCtrl+G', 'mac')).toBe(false)
    expect(matchesMenuAccelerator({ ...input, ctrlKey: false, metaKey: true }, 'CmdOrCtrl+G', 'mac')).toBe(true)
    expect(matchesMenuAccelerator({ ...input, metaKey: true }, 'CmdOrCtrl+G', 'linux')).toBe(false)
    expect(matchesMenuAccelerator({ ...input, shiftKey: true }, 'CmdOrCtrl+G', 'linux')).toBe(false)
    expect(matchesMenuAccelerator({ ...input, key: 'Escape' }, 'CmdOrCtrl+Esc', 'linux')).toBe(true)
    expect(matchesMenuAccelerator({ ...input, key: '+', shiftKey: true }, 'CmdOrCtrl+Shift+Plus', 'linux')).toBe(true)
  })

  it('matches a macOS Command+Option accelerator by the physical key', () => {
    const input = { key: '∑', code: 'KeyW', ctrlKey: false, metaKey: true, altKey: true, shiftKey: false }
    expect(matchesMenuAccelerator(input, 'CmdOrCtrl+Alt+W', 'mac')).toBe(true)
    expect(matchesMenuAccelerator({ ...input, key: 'ł', ctrlKey: true, metaKey: false }, 'CmdOrCtrl+Alt+W', 'linux')).toBe(false)
  })

  it('suspends the native Close accelerator exactly when the DOM listener owns the key', () => {
    const command = appKeyboardCommands.closeCurrentWorkspaceTab
    const bindings = [{ key: 'w', mod: true }, { key: 'w', mod: true, alt: true }, { key: '∑', mod: true, alt: true }]
    const inputs = [
      { key: 'w', code: 'KeyW', ctrlKey: false, metaKey: true, altKey: false, shiftKey: false },
      { key: '∑', code: 'KeyW', ctrlKey: false, metaKey: true, altKey: true, shiftKey: false },
      { key: '„', code: 'KeyW', ctrlKey: false, metaKey: true, altKey: true, shiftKey: true },
      { key: 'w', code: 'KeyW', ctrlKey: true, metaKey: false, altKey: false, shiftKey: false },
    ]
    for (const binding of bindings) {
      for (const input of inputs) {
        const dom = handleBrowserKeyboardShortcut({ ...input, preventDefault() {} }, { dispatch: () => true }, [{ command, ...binding }], 'mac')
        expect(matchesMenuAccelerator(input, toElectronAccelerator(binding), 'mac'), `${JSON.stringify(binding)} ${input.key}`).toBe(dom)
      }
    }
  })
})
