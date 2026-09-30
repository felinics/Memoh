import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useKeyboardShortcutsStore } from './keyboard-shortcuts'
import { appKeyboardCommands } from '@/lib/keyboard-commands'
import { comboFromBinding } from '@/lib/keyboard-combo'

class MemoryStorage implements Storage {
  private data = new Map<string, string>()
  get length() { return this.data.size }
  clear() { this.data.clear() }
  getItem(key: string) { return this.data.get(key) ?? null }
  setItem(key: string, value: string) { this.data.set(key, value) }
  removeItem(key: string) { this.data.delete(key) }
  key(index: number) { return Array.from(this.data.keys())[index] ?? null }
}

beforeEach(() => {
  (globalThis as unknown as { localStorage: Storage }).localStorage = new MemoryStorage()
  setActivePinia(createPinia())
})

describe('useKeyboardShortcutsStore', () => {
  it('returns the default table when no overrides exist', () => {
    const store = useKeyboardShortcutsStore()
    const save = store.effectiveBindings.find(b => b.command === appKeyboardCommands.saveActiveFile)
    expect(save).toMatchObject({ key: 's', mod: true })
    expect(store.isOverridden(appKeyboardCommands.saveActiveFile)).toBe(false)
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

  it('garbage stored overrides do not poison the effective bindings', () => {
    (globalThis as unknown as { localStorage: Storage }).localStorage.setItem(
      'keyboard-shortcuts-overrides',
      JSON.stringify({ [appKeyboardCommands.saveActiveFile]: '!!!' }),
    )
    const store = useKeyboardShortcutsStore()
    const save = store.effectiveBindings.find(b => b.command === appKeyboardCommands.saveActiveFile)
    expect(save).toMatchObject({ key: 's', mod: true })
  })
})
