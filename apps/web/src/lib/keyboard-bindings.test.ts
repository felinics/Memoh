import { describe, expect, it } from 'vitest'
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
  it('declares the close-tab and save bindings as the single source of truth', () => {
    const close = keyboardBindings.find(b => b.command === appKeyboardCommands.closeCurrentWorkspaceTab)
    const save = keyboardBindings.find(b => b.command === appKeyboardCommands.saveActiveFile)

    expect(close).toMatchObject({ key: 'w', mod: true, desktop: 'menu', browser: 'passthrough', scope: 'workspace' })
    expect(save).toMatchObject({ key: 's', mod: true, browser: 'intercept', scope: 'workspace' })
  })

  it('migrates the previously hardcoded sidebar toggle into the table', () => {
    const toggle = keyboardBindings.find(b => b.command === appKeyboardCommands.toggleSidebar)
    expect(toggle).toMatchObject({ key: 'b', mod: true, browser: 'intercept', scope: 'workspace' })
  })

  it('declares Mod+K as the open-settings global shortcut', () => {
    const open = keyboardBindings.find(b => b.command === appKeyboardCommands.openSettings)
    expect(open).toMatchObject({ key: 'k', mod: true, browser: 'intercept', scope: 'global' })
  })

  it('migrates the lightbox keys with a scoped lifetime (not global)', () => {
    const lightboxCommands = [
      appKeyboardCommands.closeMediaLightbox,
      appKeyboardCommands.mediaLightboxPrev,
      appKeyboardCommands.mediaLightboxNext,
    ]
    for (const command of lightboxCommands) {
      const binding = keyboardBindings.find(b => b.command === command)
      expect(binding, command).toBeDefined()
      expect(binding?.scope).toBe('mediaLightbox')
      expect(binding?.mod).toBeUndefined()
    }
  })

  it('every binding declares an i18nKey unique within the table', () => {
    const keys = keyboardBindings.map(b => b.i18nKey)
    expect(keys.every(Boolean)).toBe(true)
    expect(new Set(keys).size).toBe(keys.length)
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
