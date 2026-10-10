// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useKeyboardShortcutsStore } from './keyboard-shortcuts'
import { appKeyboardCommands } from '@/lib/keyboard-commands'
import { comboFromBinding } from '@/lib/keyboard-combo'
import { handleBrowserKeyboardShortcut } from '@/lib/browser-keyboard-shortcuts'
import { detectPlatform, keyboardBindings, resolveKeyboardBinding } from '@/lib/keyboard-bindings'

beforeEach(() => {
  localStorage.clear()
  setActivePinia(createPinia())
})
afterEach(() => { vi.unstubAllGlobals() })

function onPlatform(platform: string) {
  vi.stubGlobal('navigator', { platform, userAgent: '' })
}

describe('useKeyboardShortcutsStore', () => {
  it('returns the default table when no overrides exist', () => {
    const store = useKeyboardShortcutsStore()
    const save = store.effectiveBindings.find(b => b.command === appKeyboardCommands.saveActiveFile)
    expect(save).toMatchObject({ key: 's', mod: true })
    expect(store.isOverridden(appKeyboardCommands.saveActiveFile)).toBe(false)
  })

  it.each([
    ['MacIntel', { mod: true, alt: true, shift: false, key: 'n' }],
    ['Win32', { mod: false, alt: true, shift: true, key: 'n' }],
    ['Linux x86_64', { mod: false, alt: true, shift: true, key: 'n' }],
  ])('resolves the default chord for %s', (platform, combo) => {
    onPlatform(platform)
    const store = useKeyboardShortcutsStore()
    expect(store.getEffectiveCombo(appKeyboardCommands.newChatSession)).toEqual(combo)
  })

  it('overrides a binding key and reports it as overridden', () => {
    const store = useKeyboardShortcutsStore()
    const result = store.setBinding(appKeyboardCommands.saveActiveFile, 'Mod+Shift+s')

    expect(result.kind).toBe('none')
    expect(store.isOverridden(appKeyboardCommands.saveActiveFile)).toBe(true)
    const save = store.effectiveBindings.find(b => b.command === appKeyboardCommands.saveActiveFile)
    expect(save).toMatchObject({ key: 's', mod: true, shift: true })
  })

  it('persists the canonical combo string in localStorage', () => {
    const store = useKeyboardShortcutsStore()
    store.setBinding(appKeyboardCommands.toggleSidebar, 'mod+shift+B')
    expect(store.overrides[appKeyboardCommands.toggleSidebar]).toBe('Mod+Shift+b')
  })

  it('rejects invalid combo strings without writing an override', () => {
    const store = useKeyboardShortcutsStore()
    expect(store.setBinding(appKeyboardCommands.saveActiveFile, 'Mod+Shift').kind).toBe('invalid')
    expect(store.setBinding(appKeyboardCommands.saveActiveFile, '').kind).toBe('invalid')
    expect(store.isOverridden(appKeyboardCommands.saveActiveFile)).toBe(false)
  })

  it('blocks OS-reserved Mod+W/Q/T/N as reserved', () => {
    const store = useKeyboardShortcutsStore()
    for (const key of ['w', 'q', 't', 'n']) {
      const result = store.setBinding(appKeyboardCommands.saveActiveFile, `Mod+${key}`)
      expect(result.kind, key).toBe('reserved')
    }
    expect(store.isOverridden(appKeyboardCommands.saveActiveFile)).toBe(false)
  })

  it('blocks text editing combos so copy, paste and undo keep working in inputs', () => {
    const store = useKeyboardShortcutsStore()
    for (const combo of ['Mod+c', 'Mod+v', 'Mod+x', 'Mod+z', 'Mod+Shift+z', 'Mod+a']) {
      expect(store.setBinding(appKeyboardCommands.newTerminal, combo).kind, combo).toBe('editing')
    }
    expect(store.isOverridden(appKeyboardCommands.newTerminal)).toBe(false)
  })

  it.each([
    ['MacIntel', ['Mod+h', 'Mod+Alt+h', 'Mod+Alt+i'], ['Mod+Shift+i', 'Mod+y']],
    ['Win32', ['Mod+Shift+i', 'F11'], ['Mod+h', 'Mod+Alt+h', 'Mod+Alt+i']],
    ['Linux x86_64', ['Mod+Shift+i', 'F11'], ['Mod+h', 'Mod+Alt+h', 'Mod+Alt+i', 'Mod+y']],
  ])('on %s blocks only the combos its desktop app menu owns', (platform, owned, free) => {
    onPlatform(platform)
    const store = useKeyboardShortcutsStore()
    for (const combo of ['Mod+r', 'Mod+Shift+r', 'Mod+m', 'Mod+0', 'Mod+=', 'Mod+Plus', 'Mod+Shift+Plus', 'Mod+-', ...owned]) {
      expect(store.detectConflictFromString(appKeyboardCommands.newTerminal, combo).kind, combo).toBe('reserved')
    }
    expect(store.detectConflictFromString(appKeyboardCommands.mediaLightboxNext, 'F11').kind).toBe(owned.includes('F11') ? 'reserved' : 'none')
    for (const combo of free) {
      expect(store.detectConflictFromString(appKeyboardCommands.newTerminal, combo).kind, combo).toBe('none')
    }
  })

  it('blocks Ctrl+Y on Windows, where it is redo', () => {
    onPlatform('Win32')
    expect(useKeyboardShortcutsStore().setBinding(appKeyboardCommands.newTerminal, 'Mod+y').kind).toBe('editing')
  })

  it.each(['MacIntel', 'Win32', 'Linux x86_64'])('ships %s defaults that pass the same checks as a user rebind', (platform) => {
    onPlatform(platform)
    const store = useKeyboardShortcutsStore()
    for (const binding of store.effectiveBindings) {
      const expected = binding.browser === 'passthrough' ? 'reserved' : 'none'
      expect(store.detectConflict(binding.command, comboFromBinding(binding)).kind, binding.command).toBe(expected)
    }
  })

  it('reserved check ignores combos that include extra modifiers (Mod+Shift+W is fine)', () => {
    const store = useKeyboardShortcutsStore()
    expect(store.detectConflictFromString(appKeyboardCommands.saveActiveFile, 'Mod+Shift+w').kind).toBe('none')
  })

  it('blocks same-scope collisions and reports the colliding command', () => {
    const store = useKeyboardShortcutsStore()
    // saveActiveFile = Mod+s; try to bind toggleSidebar to Mod+s
    const result = store.setBinding(appKeyboardCommands.toggleSidebar, 'Mod+s')
    expect(result.kind).toBe('same-scope')
    expect(result.collidesWith).toBe(appKeyboardCommands.saveActiveFile)
    expect(store.isOverridden(appKeyboardCommands.toggleSidebar)).toBe(false)
  })

  it('blocks collisions between workspace and global commands that can run together', () => {
    const store = useKeyboardShortcutsStore()
    expect(store.setBinding(appKeyboardCommands.newTerminal, 'Mod+k').kind).toBe('same-scope')
    expect(store.isOverridden(appKeyboardCommands.newTerminal)).toBe(false)
    expect(store.setBinding(appKeyboardCommands.newChatSession, 'n').kind).toBe('no-modifier')
  })

  it('rejects bare-key global bindings so typing the key in a form does not fire the command', () => {
    const store = useKeyboardShortcutsStore()
    expect(store.setBinding(appKeyboardCommands.toggleSidebar, 'b').kind).toBe('no-modifier')
    expect(store.setBinding(appKeyboardCommands.openSettings, 'Shift+k').kind).toBe('no-modifier')
    expect(store.isOverridden(appKeyboardCommands.toggleSidebar)).toBe(false)
  })

  it('accepts modified global bindings (Mod or Alt)', () => {
    const store = useKeyboardShortcutsStore()
    expect(store.setBinding(appKeyboardCommands.toggleSidebar, 'Mod+Shift+j').kind).toBe('none')
    expect(store.setBinding(appKeyboardCommands.openSettings, 'Alt+m').kind).toBe('none')
  })

  it('allows bare-key bindings for scoped commands (lightbox arrows)', () => {
    const store = useKeyboardShortcutsStore()
    // mediaLightboxNext default is ArrowRight; rebinding to a bare letter is
    // fine because the scoped handler only fires while the lightbox is mounted.
    expect(store.setBinding(appKeyboardCommands.mediaLightboxNext, 'l').kind).toBe('none')
  })

  it('finds a same-scope collision even when a cross-scope match was iterated first', () => {
    const store = useKeyboardShortcutsStore()
    // First rebind a global to a key the mediaLightbox scope already uses:
    // this is allowed as cross-scope and overrides.
    store.setBinding(appKeyboardCommands.saveActiveFile, 'Mod+ArrowLeft')
    // Now another global wants the same combo. Scanning must keep going past
    // the mediaLightboxPrev cross-scope match to find saveActiveFile.
    const result = store.setBinding(appKeyboardCommands.toggleSidebar, 'Mod+ArrowLeft')
    expect(result.kind).toBe('same-scope')
    expect(result.collidesWith).toBe(appKeyboardCommands.saveActiveFile)
    expect(store.isOverridden(appKeyboardCommands.toggleSidebar)).toBe(false)
  })

  it('treats cross-scope collisions as a soft warning that still applies', () => {
    const store = useKeyboardShortcutsStore()
    // mediaLightboxPrev default is bare ArrowLeft (mediaLightbox scope is OK
    // with bare keys); bind global toggleSidebar to Mod+ArrowLeft so the
    // global-needs-modifier rule doesn't pre-empt the cross-scope check, and
    // also rebind mediaLightboxPrev to Mod+ArrowLeft to surface the collision.
    store.setBinding(appKeyboardCommands.mediaLightboxPrev, 'Mod+ArrowLeft')
    const result = store.setBinding(appKeyboardCommands.toggleSidebar, 'Mod+ArrowLeft')
    expect(result.kind).toBe('cross-scope')
    expect(result.collidesWith).toBe(appKeyboardCommands.mediaLightboxPrev)
    expect(store.isOverridden(appKeyboardCommands.toggleSidebar)).toBe(true)
  })

  it('resetBinding removes a single override', () => {
    const store = useKeyboardShortcutsStore()
    store.setBinding(appKeyboardCommands.saveActiveFile, 'Mod+Shift+s')
    store.resetBinding(appKeyboardCommands.saveActiveFile)
    expect(store.isOverridden(appKeyboardCommands.saveActiveFile)).toBe(false)
    const save = store.effectiveBindings.find(b => b.command === appKeyboardCommands.saveActiveFile)
    expect(comboFromBinding(save!)).toEqual({ mod: true, alt: false, shift: false, key: 's' })
  })

  it('resetAll clears every override', () => {
    const store = useKeyboardShortcutsStore()
    store.setBinding(appKeyboardCommands.saveActiveFile, 'Mod+Shift+s')
    store.setBinding(appKeyboardCommands.toggleSidebar, 'Mod+Shift+b')
    store.resetAll()
    expect(Object.keys(store.overrides)).toEqual([])
  })

  it('rebinding to the original default still wipes the override entry', () => {
    const store = useKeyboardShortcutsStore()
    store.setBinding(appKeyboardCommands.saveActiveFile, 'Mod+Shift+s')
    store.resetBinding(appKeyboardCommands.saveActiveFile)
    expect(store.overrides[appKeyboardCommands.saveActiveFile]).toBeUndefined()
  })

  it('overridden command can collide with another override after the swap', () => {
    const store = useKeyboardShortcutsStore()
    store.setBinding(appKeyboardCommands.saveActiveFile, 'Mod+Shift+k')
    const result = store.setBinding(appKeyboardCommands.toggleSidebar, 'Mod+Shift+k')
    expect(result.kind).toBe('same-scope')
    expect(result.collidesWith).toBe(appKeyboardCommands.saveActiveFile)
  })

  it('an override forces browser:intercept so the dispatcher claims the new combo', () => {
    const store = useKeyboardShortcutsStore()
    // closeCurrentWorkspaceTab default is Mod+W (browser passthrough). Rebind to Mod+Shift+k.
    store.setBinding(appKeyboardCommands.closeCurrentWorkspaceTab, 'Mod+Shift+k')
    const binding = store.effectiveBindings.find(b => b.command === appKeyboardCommands.closeCurrentWorkspaceTab)
    expect(binding?.browser).toBe('intercept')
  })

  it('scoped bindings precede global ones so dispatcher picks them up first', () => {
    const store = useKeyboardShortcutsStore()
    const scopes = store.effectiveBindings.map(b => b.scope)
    const firstGlobal = scopes.indexOf('global')
    const lastScoped = scopes.lastIndexOf('mediaLightbox')
    expect(firstGlobal).toBeGreaterThan(lastScoped)
  })

  it.each([
    'new-chat-session', 'focus-chat-input', 'show-sessions', 'show-files',
    'show-schedule', 'show-supermarket', 'next-workspace-tab', 'previous-workspace-tab',
    'split-workspace-right', 'split-workspace-below', 'new-terminal', 'new-browser',
  ])('keeps applying an override saved under the shipped command id %s', (command) => {
    localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({ [command]: 'Mod+Alt+Shift+F9' }))
    const store = useKeyboardShortcutsStore()
    expect(store.effectiveBindings.find(binding => binding.command === command)).toMatchObject({ key: 'F9', mod: true, alt: true, shift: true })
  })

  it('garbage stored overrides do not poison the effective bindings', () => {
    localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({ [appKeyboardCommands.saveActiveFile]: '!!!' }))
    const store = useKeyboardShortcutsStore()
    const save = store.effectiveBindings.find(b => b.command === appKeyboardCommands.saveActiveFile)
    expect(save).toMatchObject({ key: 's', mod: true })
  })

  describe('overrides saved before the current checks', () => {
    function press(store: ReturnType<typeof useKeyboardShortcutsStore>, init: KeyboardEventInit) {
      const dispatch = vi.fn(() => true)
      const event = new KeyboardEvent('keydown', { cancelable: true, ...init })
      handleBrowserKeyboardShortcut(event, { dispatch }, store.effectiveBindings, 'linux')
      return { dispatch, event }
    }

    it('falls back to the default instead of taking over copy, paste and undo', () => {
      localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({
        [appKeyboardCommands.newTerminal]: 'Mod+c',
        [appKeyboardCommands.newBrowser]: 'Mod+v',
        [appKeyboardCommands.toggleSidebar]: 'Mod+z',
        [appKeyboardCommands.showFiles]: 'Mod+Alt+Shift+F9',
      }))
      const store = useKeyboardShortcutsStore()
      for (const key of ['c', 'v', 'z']) {
        const { dispatch, event } = press(store, { key, ctrlKey: true })
        expect(dispatch, key).not.toHaveBeenCalled()
        expect(event.defaultPrevented, key).toBe(false)
      }
      expect(press(store, { key: 'x', altKey: true, shiftKey: true }).dispatch).toHaveBeenCalledWith(appKeyboardCommands.newTerminal)
      expect(press(store, { key: 'F9', ctrlKey: true, altKey: true, shiftKey: true }).dispatch).toHaveBeenCalledWith(appKeyboardCommands.showFiles)
      expect(store.ignoredOverrides).toEqual({
        [appKeyboardCommands.newTerminal]: { kind: 'editing' },
        [appKeyboardCommands.newBrowser]: { kind: 'editing' },
        [appKeyboardCommands.toggleSidebar]: { kind: 'editing' },
      })
      expect(store.isOverridden(appKeyboardCommands.newTerminal)).toBe(true)
      store.resetBinding(appKeyboardCommands.newTerminal)
      expect(store.ignoredOverrides[appKeyboardCommands.newTerminal]).toBeUndefined()
    })

    it.each([
      ['Win32', appKeyboardCommands.newTerminal, 'Mod+y', 'editing'],
      ['Linux x86_64', appKeyboardCommands.mediaLightboxNext, 'F11', 'reserved'],
      ['Linux x86_64', appKeyboardCommands.openSettings, 'Mod+r', 'reserved'],
      ['Linux x86_64', appKeyboardCommands.toggleSidebar, 'b', 'no-modifier'],
      ['Linux x86_64', appKeyboardCommands.saveActiveFile, 'Mod+Mod', 'invalid'],
      ['MacIntel', appKeyboardCommands.newTerminal, 'Alt+å', 'typing'],
      ['MacIntel', appKeyboardCommands.mediaLightboxNext, 'Alt+Dead', 'typing'],
    ])('on %s ignores %s saved as %s (%s)', (platform, command, combo, kind) => {
      onPlatform(platform)
      localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({ [command]: combo }))
      const store = useKeyboardShortcutsStore()
      const fallback = store.effectiveBindings.find(binding => binding.command === command)!
      const shipped = keyboardBindings.find(binding => binding.command === command)!
      expect(comboFromBinding(fallback)).toEqual(comboFromBinding(resolveKeyboardBinding(shipped, detectPlatform())))
      expect(store.ignoredOverrides).toEqual({ [command]: { kind } })
    })

    it('keeps Option combos that type nothing or include Command on macOS', () => {
      onPlatform('MacIntel')
      localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({
        [appKeyboardCommands.mediaLightboxNext]: 'Alt+ArrowRight',
        [appKeyboardCommands.newTerminal]: 'Mod+Alt+∫',
      }))
      const store = useKeyboardShortcutsStore()
      expect(store.ignoredOverrides).toEqual({})
    })

    it('keeps a saved override that a new default now uses and turns that default off', () => {
      localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({ [appKeyboardCommands.newTerminal]: 'Alt+Shift+Enter' }))
      const store = useKeyboardShortcutsStore()
      const { dispatch } = press(store, { key: 'Enter', altKey: true, shiftKey: true })
      expect(dispatch.mock.calls).toEqual([[appKeyboardCommands.newTerminal]])
      expect(store.shadowedDefaults).toEqual({ [appKeyboardCommands.focusChatInput]: appKeyboardCommands.newTerminal })
      expect(store.effectiveBindings.map(binding => binding.command)).not.toContain(appKeyboardCommands.focusChatInput)
      expect(store.allBindings.map(binding => binding.command)).toContain(appKeyboardCommands.focusChatInput)

      expect(store.setBinding(appKeyboardCommands.newTerminal, 'Mod+Alt+Shift+F9').kind).toBe('none')
      expect(store.shadowedDefaults).toEqual({})
      expect(press(store, { key: 'Enter', altKey: true, shiftKey: true }).dispatch.mock.calls).toEqual([[appKeyboardCommands.focusChatInput]])
    })

    it.each([
      ['MacIntel', { [appKeyboardCommands.focusChatInput]: appKeyboardCommands.newTerminal }],
      ['Linux x86_64', {}],
    ])('resolves an override against the defaults of the platform it is read on (%s)', (platform, shadowed) => {
      onPlatform(platform)
      localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({ [appKeyboardCommands.newTerminal]: 'Mod+Alt+Enter' }))
      expect(useKeyboardShortcutsStore().shadowedDefaults).toEqual(shadowed)
    })

    it('lists every command in table order for settings, whatever is overridden', () => {
      localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({ [appKeyboardCommands.newTerminal]: 'Alt+Shift+Enter' }))
      expect(useKeyboardShortcutsStore().allBindings.map(binding => binding.command)).toEqual(keyboardBindings.map(binding => binding.command))
    })

    it('ignores the later of two saved overrides on one combo', () => {
      localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({
        [appKeyboardCommands.newBrowser]: 'Mod+Alt+F9',
        [appKeyboardCommands.newTerminal]: 'Mod+Alt+F9',
      }))
      const store = useKeyboardShortcutsStore()
      expect(store.ignoredOverrides).toEqual({ [appKeyboardCommands.newBrowser]: { kind: 'same-scope', collidesWith: appKeyboardCommands.newTerminal } })
      expect(press(store, { key: 'F9', ctrlKey: true, altKey: true }).dispatch.mock.calls).toEqual([[appKeyboardCommands.newTerminal]])
      expect(press(store, { key: 'O', altKey: true, shiftKey: true }).dispatch.mock.calls).toEqual([[appKeyboardCommands.newBrowser]])
    })

    it('counts an override that restates its default when two saved overrides share a combo', () => {
      localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({
        [appKeyboardCommands.toggleSidebar]: 'Mod+b',
        [appKeyboardCommands.newBrowser]: 'Mod+b',
      }))
      const store = useKeyboardShortcutsStore()
      expect(store.ignoredOverrides).toEqual({ [appKeyboardCommands.newBrowser]: { kind: 'same-scope', collidesWith: appKeyboardCommands.toggleSidebar } })
      expect(store.shadowedDefaults).toEqual({})
      expect(press(store, { key: 'b', ctrlKey: true }).dispatch.mock.calls).toEqual([[appKeyboardCommands.toggleSidebar]])
      store.resetBinding(appKeyboardCommands.toggleSidebar)
      expect(store.ignoredOverrides).toEqual({})
      expect(store.shadowedDefaults).toEqual({ [appKeyboardCommands.toggleSidebar]: appKeyboardCommands.newBrowser })
      expect(press(store, { key: 'b', ctrlKey: true }).dispatch.mock.calls).toEqual([[appKeyboardCommands.newBrowser]])
    })

    it('lets an override saved as a shifted digit character win over the digit default', () => {
      localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({ [appKeyboardCommands.newBrowser]: 'Alt+Shift+!' }))
      const store = useKeyboardShortcutsStore()
      expect(press(store, { key: '!', code: 'Digit1', altKey: true, shiftKey: true }).dispatch.mock.calls).toEqual([[appKeyboardCommands.newBrowser]])
    })

    it('keeps an override that restates the default, even on a browser-owned combo', () => {
      localStorage.setItem('keyboard-shortcuts-overrides', JSON.stringify({ [appKeyboardCommands.closeCurrentWorkspaceTab]: 'Mod+w' }))
      const store = useKeyboardShortcutsStore()
      expect(store.ignoredOverrides).toEqual({})
      expect(store.effectiveBindings.find(binding => binding.command === appKeyboardCommands.closeCurrentWorkspaceTab)?.browser).toBe('passthrough')
    })
  })
})
