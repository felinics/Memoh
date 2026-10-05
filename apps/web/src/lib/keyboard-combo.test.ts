import { describe, expect, it } from 'vitest'
import {
  comboFromBinding,
  displayKeyCombo,
  formatKeyCombo,
  isModifierKey,
  keyCombosEqual,
  keyComboFromEvent,
  parseKeyCombo,
  type ParsedKeyCombo,
} from './keyboard-combo'

describe('parseKeyCombo', () => {
  it('parses a single named key', () => {
    expect(parseKeyCombo('Escape')).toEqual({ mod: false, alt: false, shift: false, key: 'Escape' })
  })

  it('lowercases single-character keys', () => {
    expect(parseKeyCombo('S')).toEqual({ mod: false, alt: false, shift: false, key: 's' })
    expect(parseKeyCombo('s')).toEqual({ mod: false, alt: false, shift: false, key: 's' })
  })

  it('extracts modifiers in any token order', () => {
    expect(parseKeyCombo('Mod+Shift+K')).toEqual({ mod: true, alt: false, shift: true, key: 'k' })
    expect(parseKeyCombo('Shift+Mod+K')).toEqual({ mod: true, alt: false, shift: true, key: 'k' })
  })

  it('treats Cmd/Ctrl/Meta as aliases for Mod (platform mod key)', () => {
    expect(parseKeyCombo('Cmd+S')?.mod).toBe(true)
    expect(parseKeyCombo('Ctrl+S')?.mod).toBe(true)
    expect(parseKeyCombo('Meta+S')?.mod).toBe(true)
  })

  it('treats Option/Opt as aliases for Alt', () => {
    expect(parseKeyCombo('Option+ArrowLeft')?.alt).toBe(true)
    expect(parseKeyCombo('Opt+ArrowLeft')?.alt).toBe(true)
  })

  it('preserves named arrow/function keys', () => {
    expect(parseKeyCombo('Alt+ArrowLeft')).toEqual({ mod: false, alt: true, shift: false, key: 'ArrowLeft' })
    expect(parseKeyCombo('F12')).toEqual({ mod: false, alt: false, shift: false, key: 'F12' })
  })

  it('rejects combos with no non-modifier key', () => {
    expect(parseKeyCombo('Mod+Shift')).toBeNull()
    expect(parseKeyCombo('Mod')).toBeNull()
  })

  it('rejects combos with more than one non-modifier key', () => {
    expect(parseKeyCombo('K+L')).toBeNull()
  })

  it('rejects empty input', () => {
    expect(parseKeyCombo('')).toBeNull()
    expect(parseKeyCombo('   ')).toBeNull()
  })
})

describe('formatKeyCombo', () => {
  it('emits Mod, Alt, Shift in canonical order', () => {
    expect(formatKeyCombo({ mod: true, alt: true, shift: true, key: 'k' })).toBe('Mod+Alt+Shift+k')
  })

  it('omits absent modifiers', () => {
    expect(formatKeyCombo({ mod: false, alt: false, shift: false, key: 'Escape' })).toBe('Escape')
    expect(formatKeyCombo({ mod: true, alt: false, shift: false, key: 's' })).toBe('Mod+s')
  })

  it('represents the space key as Space', () => {
    expect(formatKeyCombo({ mod: true, alt: false, shift: false, key: ' ' })).toBe('Mod+Space')
  })

  it('round-trips through parseKeyCombo', () => {
    const samples = ['Mod+s', 'Mod+Alt+Shift+k', 'Escape', 'Alt+ArrowLeft', 'F1', 'Mod+/']
    for (const sample of samples) {
      const parsed = parseKeyCombo(sample)
      expect(parsed).not.toBeNull()
      expect(formatKeyCombo(parsed as ParsedKeyCombo)).toBe(sample)
    }
  })

  it('encodes literal + as Plus so parse(format(x)) survives the split delimiter', () => {
    // Naive serialization "Shift++" would split to ['Shift','',''] and drop the
    // key; use the explicit 'Plus' token both directions.
    expect(formatKeyCombo({ mod: false, alt: false, shift: true, key: '+' })).toBe('Shift+Plus')
    expect(parseKeyCombo('Shift+Plus')).toEqual({ mod: false, alt: false, shift: true, key: '+' })
    expect(parseKeyCombo('Mod+Plus')).toEqual({ mod: true, alt: false, shift: false, key: '+' })
  })
})

describe('isModifierKey', () => {
  it('identifies bare modifier keys so capture can ignore them', () => {
    expect(isModifierKey('Shift')).toBe(true)
    expect(isModifierKey('Control')).toBe(true)
    expect(isModifierKey('Alt')).toBe(true)
    expect(isModifierKey('Meta')).toBe(true)
    expect(isModifierKey('s')).toBe(false)
    expect(isModifierKey('Escape')).toBe(false)
  })
})

describe('keyComboFromEvent', () => {
  it('returns null for bare modifier presses', () => {
    expect(keyComboFromEvent({ key: 'Shift', ctrlKey: false, metaKey: false, altKey: false, shiftKey: true }, false)).toBeNull()
    expect(keyComboFromEvent({ key: 'Meta', ctrlKey: false, metaKey: true, altKey: false, shiftKey: false }, true)).toBeNull()
  })

  it('on mac maps metaKey to mod', () => {
    expect(keyComboFromEvent({ key: 's', ctrlKey: false, metaKey: true, altKey: false, shiftKey: false }, true))
      .toEqual({ mod: true, alt: false, shift: false, key: 's' })
  })

  it('on mac rejects any combo with ctrlKey to avoid silent downgrade to plain key', () => {
    // Without this guard Ctrl+S on macOS would store as plain "s", which then
    // matches normal typing once dispatched. We can't represent literal Ctrl in
    // the current binding shape, so reject the capture rather than corrupt it.
    expect(keyComboFromEvent({ key: 's', ctrlKey: true, metaKey: false, altKey: false, shiftKey: false }, true))
      .toBeNull()
    expect(keyComboFromEvent({ key: 's', ctrlKey: true, metaKey: true, altKey: false, shiftKey: false }, true))
      .toBeNull()
  })

  it('on mac records Command+Option by the physical key instead of the Option character', () => {
    expect(keyComboFromEvent({ key: '¡', code: 'Digit1', ctrlKey: false, metaKey: true, altKey: true, shiftKey: false }, true))
      .toEqual({ mod: true, alt: true, shift: false, key: '1' })
    expect(keyComboFromEvent({ key: 'Dead', code: 'KeyN', ctrlKey: false, metaKey: true, altKey: true, shiftKey: false }, true))
      .toEqual({ mod: true, alt: true, shift: false, key: 'n' })
    expect(keyComboFromEvent({ key: '’', code: 'BracketRight', ctrlKey: false, metaKey: true, altKey: true, shiftKey: true }, true))
      .toEqual({ mod: true, alt: true, shift: true, key: ']' })
  })

  it('keeps the produced character for Ctrl+Alt outside macOS', () => {
    expect(keyComboFromEvent({ key: 'ń', code: 'KeyN', ctrlKey: true, metaKey: false, altKey: true, shiftKey: false }, false))
      .toEqual({ mod: true, alt: true, shift: false, key: 'ń' })
  })

  it('on non-mac maps ctrlKey to mod', () => {
    expect(keyComboFromEvent({ key: 's', ctrlKey: true, metaKey: false, altKey: false, shiftKey: false }, false))
      .toEqual({ mod: true, alt: false, shift: false, key: 's' })
  })

  it('normalizes letter case and preserves named keys', () => {
    expect(keyComboFromEvent({ key: 'K', ctrlKey: false, metaKey: false, altKey: false, shiftKey: true }, false))
      .toEqual({ mod: false, alt: false, shift: true, key: 'k' })
    expect(keyComboFromEvent({ key: 'ArrowLeft', ctrlKey: false, metaKey: false, altKey: true, shiftKey: false }, false))
      .toEqual({ mod: false, alt: true, shift: false, key: 'ArrowLeft' })
  })
})

describe('keyCombosEqual', () => {
  it('matches identical combos', () => {
    expect(keyCombosEqual(
      { mod: true, alt: false, shift: false, key: 's' },
      { mod: true, alt: false, shift: false, key: 's' },
    )).toBe(true)
  })

  it('distinguishes different modifier sets', () => {
    expect(keyCombosEqual(
      { mod: true, alt: false, shift: false, key: 's' },
      { mod: true, alt: false, shift: true, key: 's' },
    )).toBe(false)
  })
})

describe('displayKeyCombo', () => {
  it('renders mac glyphs for modifiers and named keys', () => {
    expect(displayKeyCombo({ mod: true, alt: false, shift: true, key: 's' }, 'mac'))
      .toEqual(['⌘', '⇧', 'S'])
    expect(displayKeyCombo({ mod: false, alt: false, shift: false, key: 'Escape' }, 'mac'))
      .toEqual(['⎋'])
    expect(displayKeyCombo({ mod: false, alt: true, shift: false, key: 'ArrowLeft' }, 'mac'))
      .toEqual(['⌥', '←'])
  })

  it('renders text labels on non-mac platforms', () => {
    expect(displayKeyCombo({ mod: true, alt: false, shift: true, key: 's' }, 'win'))
      .toEqual(['Ctrl', 'Shift', 'S'])
    expect(displayKeyCombo({ mod: false, alt: false, shift: false, key: 'Escape' }, 'linux'))
      .toEqual(['Esc'])
  })

  it('uppercases single-character keys for display', () => {
    expect(displayKeyCombo({ mod: false, alt: false, shift: false, key: 'a' }, 'win')).toEqual(['A'])
    expect(displayKeyCombo({ mod: false, alt: false, shift: false, key: '/' }, 'win')).toEqual(['/'])
  })
})

describe('comboFromBinding', () => {
  it('lifts the existing KeyboardBinding shape into a ParsedKeyCombo', () => {
    expect(comboFromBinding({ key: 'w', mod: true })).toEqual({ mod: true, alt: false, shift: false, key: 'w' })
    expect(comboFromBinding({ key: 'Escape' })).toEqual({ mod: false, alt: false, shift: false, key: 'Escape' })
  })
})
