import { computed } from 'vue'
import { defineStore } from 'pinia'
import { useStorage } from '@vueuse/core'
import {
  detectPlatform,
  keyboardBindings,
  resolveKeyboardBinding,
  RESERVED_APP_MENU_COMBOS,
  RESERVED_BROWSER_COMBOS,
  TEXT_EDITING_COMBOS,
  type KeyboardBinding,
  type KeyboardScope,
} from '@/lib/keyboard-bindings'
import {
  comboFromBinding,
  formatKeyCombo,
  keyCombosEqual,
  parseKeyCombo,
  type ParsedKeyCombo,
} from '@/lib/keyboard-combo'
import type { AppKeyboardCommand } from '@/lib/keyboard-commands'

export type ConflictKind = 'none' | 'same-scope' | 'cross-scope' | 'reserved' | 'editing' | 'invalid' | 'no-modifier' | 'typing'

export interface ConflictResult {
  kind: ConflictKind
  collidesWith?: AppKeyboardCommand
}


function isReservedCombo(combo: ParsedKeyCombo): boolean {
  return combo.mod && !combo.alt && !combo.shift && RESERVED_BROWSER_COMBOS.has(combo.key.toLowerCase())
}

function canRunTogether(a: KeyboardScope, b: KeyboardScope): boolean {
  return a === b || (a !== 'mediaLightbox' && b !== 'mediaLightbox')
}

interface AppliedBinding {
  binding: KeyboardBinding
  overridden: boolean
  ignored?: ConflictResult
  shadowedBy?: AppKeyboardCommand
}

export const useKeyboardShortcutsStore = defineStore('keyboard-shortcuts', () => {
  const platform = detectPlatform()
  const appMenuCombos = RESERVED_APP_MENU_COMBOS[platform].map(combo => parseKeyCombo(combo)!)
  const textEditingCombos = TEXT_EDITING_COMBOS[platform].map(combo => parseKeyCombo(combo)!)
  const overrides = useStorage<Record<string, string>>('keyboard-shortcuts-overrides', {}, undefined, {
    mergeDefaults: true,
  })

  // Rules a combo must pass no matter how it was saved, so an override stored
  // before a rule existed cannot take over copy, paste or a menu shortcut.
  function unsafeCombo(scope: KeyboardScope, combo: ParsedKeyCombo): ConflictKind | null {
    if (isReservedCombo(combo) || appMenuCombos.some(reserved => keyCombosEqual(reserved, combo))) return 'reserved'
    if (textEditingCombos.some(editing => keyCombosEqual(editing, combo))) return 'editing'
    // macOS Option without Command types a character (or starts a dead key).
    if (platform === 'mac' && combo.alt && !combo.mod && (combo.key.length === 1 || combo.key === 'Dead')) return 'typing'
    // The window-level listener also runs while text inputs have focus, so a
    // bare key would fire on every keystroke. Lightbox bindings are only active
    // while the overlay is open, so a bare arrow key is fine there.
    if (scope !== 'mediaLightbox' && !combo.mod && !combo.alt) return 'no-modifier'
    return null
  }

  function applyOverride(binding: KeyboardBinding, override: string | undefined): AppliedBinding {
    if (!override) return { binding, overridden: false }
    const parsed = parseKeyCombo(override)
    if (!parsed) return { binding, overridden: false, ignored: { kind: 'invalid' } }
    if (keyCombosEqual(parsed, comboFromBinding(binding))) return { binding, overridden: true }
    const unsafe = unsafeCombo(binding.scope, parsed)
    if (unsafe) return { binding, overridden: false, ignored: { kind: unsafe } }
    return {
      overridden: true,
      binding: {
        ...binding,
        key: parsed.key,
        mod: parsed.mod || undefined,
        alt: parsed.alt || undefined,
        shift: parsed.shift || undefined,
        // The default's passthrough would let the new combo leak to the browser.
        browser: 'intercept',
      },
    }
  }

  // A saved override keeps working when a later default takes its combo: the
  // default is switched off instead. Two overrides on one combo keep the first.
  const applied = computed(() => {
    const defaults = keyboardBindings.map(binding => resolveKeyboardBinding(binding, platform))
    const results = defaults.map(binding => applyOverride(binding, overrides.value[binding.command]))
    const collides = (a: KeyboardBinding, b: KeyboardBinding) => a.command !== b.command
      && canRunTogether(a.scope, b.scope)
      && keyCombosEqual(comboFromBinding(a), comboFromBinding(b))
    results.forEach((entry, index) => {
      if (!entry.overridden) return
      const earlier = results.slice(0, index).find(other => other.overridden && collides(entry.binding, other.binding))
      if (earlier) results[index] = { binding: defaults[index]!, overridden: false, ignored: { kind: 'same-scope', collidesWith: earlier.binding.command } }
    })
    for (const entry of results) {
      if (!entry.overridden) entry.shadowedBy = results.find(other => other.overridden && collides(entry.binding, other.binding))?.binding.command
    }
    return results
  })

  // Lightbox bindings come first so a combo shared with the workspace resolves
  // to the open lightbox; selectActiveKeyboardBindings drops them while no
  // lightbox is open. Saved overrides precede defaults, then narrower scopes.
  const ordered = computed(() => [...applied.value].sort((a, b) => {
    const rank = (entry: AppliedBinding) => [
      entry.binding.scope === 'mediaLightbox' ? 0 : 1,
      entry.overridden ? 0 : 1,
      entry.binding.scope === 'global' ? 1 : 0,
    ]
    const [x, y] = [rank(a), rank(b)]
    return x[0]! - y[0]! || x[1]! - y[1]! || x[2]! - y[2]!
  }))

  /** Every command with the chord shown in settings, including defaults that are switched off. */
  const allBindings = computed(() => applied.value.map(({ binding }) => binding))

  /** The bindings the dispatcher acts on. */
  const effectiveBindings = computed(() => ordered.value.filter(({ shadowedBy }) => !shadowedBy).map(({ binding }) => binding))

  /** Saved overrides that cannot apply; their commands keep the default. */
  const ignoredOverrides = computed(() => Object.fromEntries(applied.value
    .filter(({ ignored }) => ignored)
    .map(({ binding, ignored }) => [binding.command, ignored!])) as Partial<Record<AppKeyboardCommand, ConflictResult>>)

  /** Defaults switched off because a saved override took their combo, mapped to that override's command. */
  const shadowedDefaults = computed(() => Object.fromEntries(applied.value
    .filter(({ shadowedBy }) => shadowedBy)
    .map(({ binding, shadowedBy }) => [binding.command, shadowedBy!])) as Partial<Record<AppKeyboardCommand, AppKeyboardCommand>>)

  function getEffectiveCombo(command: AppKeyboardCommand): ParsedKeyCombo | null {
    const binding = effectiveBindings.value.find(b => b.command === command)
    return binding ? comboFromBinding(binding) : null
  }

  function isOverridden(command: AppKeyboardCommand): boolean {
    return Object.prototype.hasOwnProperty.call(overrides.value, command)
  }

  function detectConflict(command: AppKeyboardCommand, combo: ParsedKeyCombo): ConflictResult {
    const ownBinding = keyboardBindings.find(b => b.command === command)
    if (!ownBinding) return { kind: 'none' }
    const unsafe = unsafeCombo(ownBinding.scope, combo)
    if (unsafe) return { kind: unsafe }
    // Scan every matching binding before deciding: a same-scope collision must
    // block the save even when an earlier-iterated cross-scope binding shares
    // the combo. Otherwise the first cross-scope match would short-circuit and
    // we'd silently let two global commands share the same key.
    let crossScopeMatch: AppKeyboardCommand | undefined
    for (const binding of effectiveBindings.value) {
      if (binding.command === command) continue
      if (!keyCombosEqual(comboFromBinding(binding), combo)) continue
      if (binding.scope === ownBinding.scope || (binding.scope !== 'mediaLightbox' && ownBinding.scope !== 'mediaLightbox')) {
        return { kind: 'same-scope', collidesWith: binding.command }
      }
      crossScopeMatch = crossScopeMatch ?? binding.command
    }
    if (crossScopeMatch) return { kind: 'cross-scope', collidesWith: crossScopeMatch }
    return { kind: 'none' }
  }

  function detectConflictFromString(command: AppKeyboardCommand, combo: string): ConflictResult {
    const parsed = parseKeyCombo(combo)
    if (!parsed) return { kind: 'invalid' }
    return detectConflict(command, parsed)
  }

  function setBinding(command: AppKeyboardCommand, combo: string): ConflictResult {
    const parsed = parseKeyCombo(combo)
    if (!parsed) return { kind: 'invalid' }
    const conflict = detectConflict(command, parsed)
    if (conflict.kind !== 'none' && conflict.kind !== 'cross-scope') return conflict
    overrides.value = { ...overrides.value, [command]: formatKeyCombo(parsed) }
    return conflict
  }

  function resetBinding(command: AppKeyboardCommand): void {
    if (!isOverridden(command)) return
    const next = { ...overrides.value }
    delete next[command]
    overrides.value = next
  }

  function resetAll(): void {
    overrides.value = {}
  }

  return {
    overrides,
    allBindings,
    effectiveBindings,
    ignoredOverrides,
    shadowedDefaults,
    getEffectiveCombo,
    isOverridden,
    detectConflict,
    detectConflictFromString,
    setBinding,
    resetBinding,
    resetAll,
  }
})
