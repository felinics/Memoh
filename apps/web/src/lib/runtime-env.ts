/**
 * Canonical runtime environment primitives (synchronous, framework-free).
 *
 * Every question about "where is this code running, what input/display
 * capabilities exist, and what OS/accessibility conventions apply?" goes
 * through THIS module (or its reactive Vue counterpart in
 * `@/composables/useEnvironment`). Business modules must never call
 * `window.matchMedia`, `useMediaQuery`, or inspect `navigator.platform` /
 * `navigator.userAgent` / `navigator.maxTouchPoints` directly (enforced by
 * ESLint).
 *
 * Four orthogonal layers — never conflate one for another:
 *   1. Layout space (`COMPACT_SHELL_MEDIA`): `< 768px` viewport width, strict
 *      complement of Tailwind `md:`. Selects single-stack vs multi-panel shell
 *      ONLY; says nothing about touch or virtual keyboards.
 *   2. Input modality (`COARSE_POINTER_MEDIA`, `CAN_HOVER_MEDIA`, and `any-*`
 *      coexisting variants): primary vs attached pointers/hover. Governs focus
 *      auto-placement (virtual keyboard avoidance), touch gestures, and
 *      submenu auto-close. Input modalities can coexist (touchscreen laptops,
 *      tablets with external trackpads/keyboards).
 *   3. Host shell (`DesktopShellKey` in `@/lib/desktop-shell`, read via
 *      `useDesktopShell()`): browser tab vs Electron desktop shell.
 *   4. OS family & accessibility preferences (`detectOsFamily`,
 *      `prefersReducedMotion`): modifier keys, CLI host defaults, macOS window
 *      chrome reserve, and motion reduction.
 */

// 1. Layout space — strict complement of Tailwind `md:` (`>= 768px`).
export const COMPACT_SHELL_MEDIA = '(width < 768px)'

// 2. Input modality — primary device vs coexisting attached devices.
export const COARSE_POINTER_MEDIA = '(pointer: coarse)'
export const CAN_HOVER_MEDIA = '(hover: hover)'
export const TOUCH_POINTER_MEDIA = '(hover: none) and (pointer: coarse)'
export const ANY_COARSE_POINTER_MEDIA = '(any-pointer: coarse)'
export const ANY_FINE_POINTER_MEDIA = '(any-pointer: fine)'
export const ANY_HOVER_MEDIA = '(any-hover: hover)'

// 4. Accessibility preferences.
export const REDUCED_MOTION_MEDIA = '(prefers-reduced-motion: reduce)'

export interface NavigatorIdentityLike {
  platform?: string
  userAgent?: string
}

export type OsFamily = 'mac' | 'windows' | 'linux' | 'ios' | 'android' | 'unknown'

/**
 * Identify the client OS family from `navigator` (or an explicit fixture in
 * tests). Omitting the argument reads `globalThis.navigator`; passing
 * `undefined` explicitly treats the identity as empty (`'unknown'`).
 *
 * Mobile families (`android`, `ios`) are checked before `linux` / `mac`
 * because Android user agents contain `Linux` and iPhone user agents contain
 * `like Mac OS X`. `/\bwin/i` avoids false-matching `darwin` in jsdom UAs.
 */
export function detectOsFamily(
  ...args: [navigatorLike?: NavigatorIdentityLike]
): OsFamily {
  const nav = args.length > 0
    ? args[0]
    : (typeof navigator === 'undefined' ? undefined : navigator)
  const haystack = `${nav?.platform ?? ''} ${nav?.userAgent ?? ''}`
  if (/android/i.test(haystack)) return 'android'
  if (/iphone|ipad|ipod/i.test(haystack)) return 'ios'
  if (/mac/i.test(haystack)) return 'mac'
  if (/\bwin/i.test(haystack)) return 'windows'
  if (/linux/i.test(haystack)) return 'linux'
  return 'unknown'
}

/** Resolve the current global `Window` at call time so tests stubbing `window` see the live mock. */
export function currentWindow(): Window | undefined {
  return typeof window === 'undefined' ? undefined : window
}

/** Synchronous media-query check for imperative code outside Vue setup. */
export function matchesMedia(
  query: string,
  win: Pick<Window, 'matchMedia'> | undefined = currentWindow(),
): boolean {
  return Boolean(win?.matchMedia?.(query).matches)
}

/** Synchronous check for the user's OS reduced-motion accessibility setting. */
export function prefersReducedMotion(win?: Pick<Window, 'matchMedia'>): boolean {
  return matchesMedia(REDUCED_MOTION_MEDIA, win)
}
