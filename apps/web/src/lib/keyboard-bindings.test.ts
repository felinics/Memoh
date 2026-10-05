import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en.json'
import ja from '@/i18n/locales/ja.json'
import zh from '@/i18n/locales/zh.json'
import { appKeyboardCommands } from './keyboard-commands'
import {
  keyboardBindings,
  toElectronAccelerator,
  acceleratorForCommand,
  selectWebBindings,
  resolveBindingKey,
  detectPlatform,
  RESERVED_BROWSER_COMBOS,
  type KeyboardBinding,
} from './keyboard-bindings'

describe('keyboard bindings table', () => {
  it('gives every settings row its own translated label and description', () => {
    const keys = keyboardBindings.map(b => b.i18nKey)
    expect(new Set(keys).size).toBe(keys.length)
    for (const [locale, messages] of Object.entries({ en, zh, ja })) {
      for (const key of keys) {
        const command = (messages.settings.keyboard.commands as Record<string, { label?: string, description?: string }>)[key]
        expect(command?.label, `${locale} ${key}`).toBeTruthy()
        expect(command?.description, `${locale} ${key}`).toBeTruthy()
      }
    }
  })

  it('gives every app command exactly one discoverable binding', () => {
    for (const command of Object.values(appKeyboardCommands)) {
      expect(keyboardBindings.filter(binding => binding.command === command), command).toHaveLength(1)
    }
  })
})

describe('toElectronAccelerator', () => {
  it('maps mod to CmdOrCtrl so one string aligns across platforms', () => {
    expect(toElectronAccelerator({ key: 'w', mod: true })).toBe('CmdOrCtrl+W')
  })

  it('encodes a literal plus key for the native accelerator parser', () => {
    expect(toElectronAccelerator({ key: '+', mod: true, shift: true })).toBe('CmdOrCtrl+Shift+Plus')
  })

  it('orders modifiers CmdOrCtrl, Alt, Shift and uppercases single-char keys', () => {
    const binding: KeyboardBinding = { command: appKeyboardCommands.saveActiveFile, key: 'k', mod: true, alt: true, shift: true, browser: 'intercept', scope: 'global', i18nKey: 'saveActiveFile' }
    expect(toElectronAccelerator(binding)).toBe('CmdOrCtrl+Alt+Shift+K')
  })

  it('maps DOM key names (ArrowLeft, Escape, Space) to their Electron accelerator equivalents', () => {
    // Electron's accelerator parser uses Left/Right/Up/Down and Esc, not the
    // DOM ArrowLeft/Escape names — pushing the raw DOM names leaves the
    // native menu accelerator silently broken.
    const make = (key: string): KeyboardBinding => ({
      command: appKeyboardCommands.closeCurrentWorkspaceTab, key, mod: true,
      desktop: 'menu', browser: 'passthrough', scope: 'global', i18nKey: 'closeCurrentWorkspaceTab',
    })
    expect(toElectronAccelerator(make('ArrowLeft'))).toBe('CmdOrCtrl+Left')
    expect(toElectronAccelerator(make('ArrowDown'))).toBe('CmdOrCtrl+Down')
    expect(toElectronAccelerator(make('Escape'))).toBe('CmdOrCtrl+Esc')
    expect(toElectronAccelerator(make(' '))).toBe('CmdOrCtrl+Space')
  })
})

describe('acceleratorForCommand', () => {
  it('returns the derived accelerator for a known command', () => {
    expect(acceleratorForCommand(appKeyboardCommands.closeCurrentWorkspaceTab)).toBe('CmdOrCtrl+W')
  })

  it('returns undefined for a command without a binding', () => {
    expect(acceleratorForCommand('no-such-command' as never)).toBeUndefined()
  })
})

describe('selectWebBindings', () => {
  it('keeps intercept bindings and drops passthrough ones (browser keeps native behavior)', () => {
    const commands = selectWebBindings(keyboardBindings).map(b => b.command)
    expect(commands).toContain(appKeyboardCommands.saveActiveFile)
    expect(commands).not.toContain(appKeyboardCommands.closeCurrentWorkspaceTab)
  })
})

describe('resolveBindingKey', () => {
  const base = { command: appKeyboardCommands.saveActiveFile, key: 's' }

  it('returns the base key when no per-platform override is declared', () => {
    expect(resolveBindingKey(base, 'mac')).toBe('s')
    expect(resolveBindingKey(base, 'win')).toBe('s')
    expect(resolveBindingKey(base, 'linux')).toBe('s')
  })

  it('returns the platform-specific override when declared', () => {
    const binding = { key: 'w', mac: 'w', win: 'F4', linux: 'w' }
    expect(resolveBindingKey(binding, 'mac')).toBe('w')
    expect(resolveBindingKey(binding, 'win')).toBe('F4')
    expect(resolveBindingKey(binding, 'linux')).toBe('w')
  })

  it('falls back to the base key when only some platforms are overridden', () => {
    const binding = { key: 'k', win: 'j' }
    expect(resolveBindingKey(binding, 'mac')).toBe('k')
    expect(resolveBindingKey(binding, 'win')).toBe('j')
  })
})

describe('detectPlatform', () => {
  it('detects mac, windows and linux from a navigator-like object', () => {
    expect(detectPlatform({ platform: 'MacIntel' })).toBe('mac')
    expect(detectPlatform({ platform: 'Win32' })).toBe('win')
    expect(detectPlatform({ platform: 'Linux x86_64' })).toBe('linux')
  })

  it('falls back to userAgent when platform is unavailable', () => {
    expect(detectPlatform({ userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X)' })).toBe('mac')
    expect(detectPlatform({ userAgent: 'Mozilla/5.0 (Windows NT 10.0)' })).toBe('win')
  })

  it('defaults to linux for unknown environments', () => {
    expect(detectPlatform({})).toBe('linux')
    expect(detectPlatform(undefined)).toBe('linux')
  })
})

describe('menu bindings do not use per-platform key overrides', () => {
  // toElectronAccelerator emits CmdOrCtrl+<base key>; Electron maps the mod per
  // platform natively. A menu binding with divergent per-platform keys would not
  // be reflected in that single accelerator, so it is disallowed (flagged here
  // rather than silently producing a wrong menu accelerator).
  it('every desktop:menu binding leaves mac/win/linux keys unset', () => {
    const offenders = keyboardBindings.filter(
      b => b.desktop === 'menu' && (b.mac !== undefined || b.win !== undefined || b.linux !== undefined),
    )
    expect(offenders).toEqual([])
  })
})

describe('reserved browser combos invariant', () => {
  it('never marks an OS/browser-reserved combo as browser:intercept', () => {
    const offenders = keyboardBindings.filter(
      b => b.mod === true && !b.alt && !b.shift && b.browser === 'intercept' && RESERVED_BROWSER_COMBOS.has(b.key.toLowerCase()),
    )
    expect(offenders).toEqual([])
  })
})
