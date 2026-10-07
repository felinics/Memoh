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
  resolveKeyboardBinding,
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

describe('resolveKeyboardBinding', () => {
  const base = { command: appKeyboardCommands.saveActiveFile, key: 's', mod: true }

  it('keeps the base chord when no platform diverges', () => {
    for (const platform of ['mac', 'win', 'linux'] as const) {
      expect(resolveKeyboardBinding(base, platform)).toMatchObject({ key: 's', mod: true })
    }
  })

  it('replaces the whole chord on a platform that declares its own', () => {
    const binding = { ...base, key: 'n', mod: undefined, alt: true, shift: true, mac: { key: 'n', mod: true, alt: true } }
    expect(resolveKeyboardBinding(binding, 'mac')).toMatchObject({ key: 'n', mod: true, alt: true, shift: undefined, mac: undefined })
    expect(resolveKeyboardBinding(binding, 'win')).toMatchObject({ key: 'n', mod: undefined, alt: true, shift: true })
  })
})

describe('platform defaults', () => {
  it.each(['win', 'linux'] as const)('never uses Ctrl+Alt on %s, which Windows reports for AltGr', (platform) => {
    const altGr = keyboardBindings.map(binding => resolveKeyboardBinding(binding, platform)).filter(binding => binding.mod && binding.alt)
    expect(altGr).toEqual([])
  })

  it.each(['win', 'linux'] as const)('leaves the file editor its own Alt+Shift keys on %s', (platform) => {
    // Monaco 0.52 binds Shift+Alt+arrows (expand selection, copy line), A, F, I, period and F8.
    const editorKeys = ['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'a', 'f', 'i', '.', 'F8']
    const taken = keyboardBindings.map(binding => resolveKeyboardBinding(binding, platform))
      .filter(binding => binding.alt && binding.shift && !binding.mod && editorKeys.includes(binding.key))
    expect(taken).toEqual([])
  })

  it('keeps the workspace defaults on Command+Option on macOS', () => {
    const workbench = keyboardBindings.filter(binding => binding.alt && binding.shift)
    expect(workbench).toHaveLength(12)
    for (const binding of workbench) {
      expect(resolveKeyboardBinding(binding, 'mac'), binding.command).toMatchObject({ mod: true, alt: true, shift: undefined })
    }
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

describe('menu bindings do not use per-platform chords', () => {
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
