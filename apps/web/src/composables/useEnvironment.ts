import { computed, inject, type ComputedRef, type Ref } from 'vue'
import { useMediaQuery } from '@vueuse/core'
import { DesktopShellKey } from '@/lib/desktop-shell'
import {
  ANY_COARSE_POINTER_MEDIA,
  ANY_FINE_POINTER_MEDIA,
  ANY_HOVER_MEDIA,
  CAN_HOVER_MEDIA,
  COARSE_POINTER_MEDIA,
  COMPACT_SHELL_MEDIA,
  REDUCED_MOTION_MEDIA,
  TOUCH_POINTER_MEDIA,
  currentWindow,
} from '@/lib/runtime-env'

/**
 * Reactive environment & capability composables, split into orthogonal layers.
 *
 * Callers subscribe only to the layer that governs their behavior:
 *   - `useCompactShell()`       -> layout space (`< 768px` single-stack vs `md:` workbench)
 *   - `useCoarsePointer()`      -> primary input is coarse (phones, tablets, touch-mode 2-in-1s)
 *   - `useTouchPointer()`       -> primary input is coarse without hover (touch-only gestures)
 *   - `useCanHover()`           -> primary input can hover (matches CSS `can-hover:`)
 *   - `useInputModality()`      -> full primary + coexisting (`any-*`) input matrix & focus policy
 *   - `useDesktopShell()`       -> host container (Electron desktop shell vs web browser)
 *   - `useReducedMotion()`      -> OS `prefers-reduced-motion` preference
 *
 * `window` is resolved at call time so tests that stub or replace `window`
 * via `vi.hoisted` / `vi.stubGlobal` observe the active mock instead of
 * `@vueuse/core`'s module-load snapshot.
 */

function queryMedia(query: string): Ref<boolean> {
  return useMediaQuery(query, { window: currentWindow() })
}

/**
 * Layer 1 — Layout space (`width < 768px`).
 *
 * Strictly complements Tailwind `md:` (`>= 768px`). Use ONLY to pick between
 * the single-stack compact shell and the multi-panel workbench / master-detail
 * split. Never use this to infer touch input or software-keyboard presence:
 * iPads, tablets, and landscape phones are `>= 768px` while still touch-driven,
 * and a narrowed desktop browser window is `< 768px` while mouse/keyboard-driven.
 */
export function useCompactShell(): Ref<boolean> {
  return queryMedia(COMPACT_SHELL_MEDIA)
}

/**
 * Layer 2 — Primary pointer is coarse (`(pointer: coarse)`).
 *
 * True on phones, iPads, Android tablets, and 2-in-1s folded into tablet mode;
 * false on desktops and touchscreen laptops whose primary pointer is a mouse
 * or trackpad. Governs whether programmatic focus would raise an unrequested
 * virtual keyboard and whether tap-driven submenus should close on selection.
 */
export function useCoarsePointer(): Ref<boolean> {
  return queryMedia(COARSE_POINTER_MEDIA)
}

/**
 * Layer 2 — Primary input is touch without hover (`(hover: none) and (pointer: coarse)`).
 *
 * Governs touch-specific gesture affordances such as long-press context menus.
 */
export function useTouchPointer(): Ref<boolean> {
  return queryMedia(TOUCH_POINTER_MEDIA)
}

/**
 * Layer 2 — Primary input supports hover (`(hover: hover)`).
 *
 * JS counterpart to the `@custom-variant can-hover` CSS variant.
 */
export function useCanHover(): Ref<boolean> {
  return queryMedia(CAN_HOVER_MEDIA)
}

export interface InputModality {
  /** Primary pointing device is coarse (touch). */
  coarsePointer: Ref<boolean>
  /** Primary input device can comfortably hover. */
  canHover: Ref<boolean>
  /** Coexisting capability: at least one coarse pointer is attached (includes touchscreen laptops). */
  anyCoarsePointer: Ref<boolean>
  /** Coexisting capability: at least one fine pointer is attached (mouse, trackpad, stylus). */
  anyFinePointer: Ref<boolean>
  /** Coexisting capability: at least one hover-capable input device is attached. */
  anyHover: Ref<boolean>
  /**
   * Whether entering a view/menu may place focus into a text input
   * automatically without risking an unrequested on-screen virtual keyboard.
   *
   * Keyed on `!coarsePointer` rather than `!anyFinePointer`: pairing an Apple
   * Pencil on iPad flips `(any-pointer: fine)` to true while `<textarea>.focus()`
   * still raises the software keyboard, whereas touchscreen laptops keep
   * `coarsePointer === false` in laptop mode. On tablets with an attached
   * hardware keyboard, typing directly still routes into the composer via
   * `useUnfocusedComposerInput`.
   */
  canAutoFocusInput: ComputedRef<boolean>
}

/**
 * Layer 2 — Full primary + coexisting input-modality matrix.
 */
export function useInputModality(): InputModality {
  const coarsePointer = useCoarsePointer()
  const canHover = useCanHover()
  const anyCoarsePointer = queryMedia(ANY_COARSE_POINTER_MEDIA)
  const anyFinePointer = queryMedia(ANY_FINE_POINTER_MEDIA)
  const anyHover = queryMedia(ANY_HOVER_MEDIA)
  const canAutoFocusInput = computed(() => !coarsePointer.value)

  return {
    coarsePointer,
    canHover,
    anyCoarsePointer,
    anyFinePointer,
    anyHover,
    canAutoFocusInput,
  }
}

/**
 * Layer 3 — Host container: true inside the Electron desktop shell (or the
 * `?desktopShell=1` capture hatch), false in standard browser tabs.
 */
export function useDesktopShell(): boolean {
  return inject(DesktopShellKey, false)
}

/**
 * Layer 4 — Accessibility preference: reactive OS `prefers-reduced-motion`.
 */
export function useReducedMotion(): Ref<boolean> {
  return queryMedia(REDUCED_MOTION_MEDIA)
}
