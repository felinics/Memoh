import { computed } from 'vue'
import { defineStore } from 'pinia'
import { useStorage } from '@vueuse/core'
import {
  keyboardBindings,
  RESERVED_BROWSER_COMBOS,
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

export type ConflictKind = 'none' | 'same-scope' | 'cross-scope' | 'reserved' | 'invalid' | 'no-modifier'

export interface ConflictResult {
  kind: ConflictKind
  collidesWith?: AppKeyboardCommand
}

function isReservedCombo(combo: ParsedKeyCombo): boolean {
  return combo.mod && !combo.alt && !combo.shift && RESERVED_BROWSER_COMBOS.has(combo.key.toLowerCase())
}

function applyOverride(binding: KeyboardBinding, override: string | undefined): KeyboardBinding {
  if (!override) return binding
  const parsed = parseKeyCombo(override)
  if (!parsed) return binding
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
    // Non-global (scoped) bindings come first so the dispatcher's first-handled-
    // wins iterator gives them a chance to claim their keys before any global
    // binding that happens to share a combo. A scoped command's handler is only
    // registered while its owning component is mounted, so when the scope is
    // inactive the matcher falls through to the global. Stable sort preserves
    // intra-scope order from the source table.
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
    if (isReservedCombo(combo)) return { kind: 'reserved' }
    const ownBinding = keyboardBindings.find(b => b.command === command)
    if (!ownBinding) return { kind: 'none' }
    // Global shortcuts dispatch from a window-level listener that does not skip
    // focused inputs, so a bare-key global binding would fire on every literal
    // keystroke (e.g. binding 'b' would make typing 'b' open the sidebar).
    // Scoped bindings only register their handler while the owning component is
    // mounted, so a bare arrow key for the lightbox is fine.
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
    if (conflict.kind === 'same-scope' || conflict.kind === 'reserved' || conflict.kind === 'no-modifier') return conflict
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
