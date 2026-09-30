import { describe, expect, it } from 'vitest'
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
})
