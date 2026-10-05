import { computed } from 'vue'
import { defineStore } from 'pinia'
import { useStorage } from '@vueuse/core'
import {
  keyboardBindings,
  RESERVED_APP_MENU_COMBOS,
  RESERVED_BROWSER_COMBOS,
  TEXT_EDITING_COMBOS,
  type KeyboardBinding,
} from '@/lib/keyboard-bindings'
import {
  comboFromBinding,
  formatKeyCombo,
  keyCombosEqual,
  parseKeyCombo,
  type ParsedKeyCombo,
} from '@/lib/keyboard-combo'
import type { AppKeyboardCommand } from '@/lib/keyboard-commands'

export type ConflictKind = 'none' | 'same-scope' | 'cross-scope' | 'reserved' | 'editing' | 'invalid' | 'no-modifier'

export interface ConflictResult {
  kind: ConflictKind
  collidesWith?: AppKeyboardCommand
}

const appMenuCombos = RESERVED_APP_MENU_COMBOS.map(combo => parseKeyCombo(combo)!)
const textEditingCombos = TEXT_EDITING_COMBOS.map(combo => parseKeyCombo(combo)!)

function isReservedCombo(combo: ParsedKeyCombo): boolean {
  return combo.mod && !combo.alt && !combo.shift && RESERVED_BROWSER_COMBOS.has(combo.key.toLowerCase())
}

function applyOverride(binding: KeyboardBinding, override: string | undefined): KeyboardBinding {
  if (!override) return binding
  const parsed = parseKeyCombo(override)
  if (!parsed || (binding.scope !== 'mediaLightbox' && !parsed.mod && !parsed.alt)) return binding
  return {
    ...binding,
    key: parsed.key,
    mod: parsed.mod || undefined,
    alt: parsed.alt || undefined,
    shift: parsed.shift || undefined,
    mac: undefined,
    win: undefined,
    linux: undefined,
    // A user override is never on a reserved combo (set/setBinding blocks it),
    // so we can claim browser intercept; without this the default's passthrough
    // would silently leak through and the dispatcher would not preventDefault.
    browser: isReservedCombo(parsed) ? binding.browser : 'intercept',
  }
}

export const useKeyboardShortcutsStore = defineStore('keyboard-shortcuts', () => {
  const overrides = useStorage<Record<string, string>>('keyboard-shortcuts-overrides', {}, undefined, {
    mergeDefaults: true,
  })

  const effectiveBindings = computed<KeyboardBinding[]>(() => {
    const merged = keyboardBindings.map(binding => applyOverride(binding, overrides.value[binding.command]))
    // Narrower scopes come first so a combo shared across scopes resolves to the
    // active narrower one; selectActiveKeyboardBindings drops lightbox bindings
    // while no lightbox is open. Stable sort keeps the table order per scope.
    return [...merged].sort((a, b) => {
      const order = { mediaLightbox: 0, workspace: 1, global: 2 }
      return order[a.scope] - order[b.scope]
    })
  })

  function getEffectiveCombo(command: AppKeyboardCommand): ParsedKeyCombo | null {
    const binding = effectiveBindings.value.find(b => b.command === command)
    return binding ? comboFromBinding(binding) : null
  }

  function isOverridden(command: AppKeyboardCommand): boolean {
    return Object.prototype.hasOwnProperty.call(overrides.value, command)
  }

  function detectConflict(command: AppKeyboardCommand, combo: ParsedKeyCombo): ConflictResult {
    if (isReservedCombo(combo) || appMenuCombos.some(reserved => keyCombosEqual(reserved, combo))) return { kind: 'reserved' }
    if (textEditingCombos.some(editing => keyCombosEqual(editing, combo))) return { kind: 'editing' }
    const ownBinding = keyboardBindings.find(b => b.command === command)
    if (!ownBinding) return { kind: 'none' }
    // The window-level listener also runs while text inputs have focus, so a
    // bare key would fire on every keystroke. Lightbox bindings are only active
    // while the overlay is open, so a bare arrow key is fine there.
    if (ownBinding.scope !== 'mediaLightbox' && !combo.mod && !combo.alt) {
      return { kind: 'no-modifier' }
    }
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
    effectiveBindings,
    getEffectiveCombo,
    isOverridden,
    detectConflict,
    detectConflictFromString,
    setBinding,
    resetBinding,
    resetAll,
  }
})
