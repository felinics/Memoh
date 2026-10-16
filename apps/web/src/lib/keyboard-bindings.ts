import { appKeyboardCommands, type AppKeyboardCommand } from './keyboard-commands'

/**
 * `menu` also exposes the binding in the native menu; menu clicks route via IPC.
 * Physical keys always go through the shared DOM listener in the chat window.
 */
export type DesktopDelivery = 'menu'

/**
 * How a binding behaves in a plain browser:
 * - `intercept`   - we match it, dispatch the command, and call preventDefault.
 * - `passthrough` - we never touch it; the browser/OS keeps its native behavior
 *                   (e.g. Cmd/Ctrl+W closes the browser tab).
 */
export type BrowserBehavior = 'intercept' | 'passthrough'

export type KeyboardPlatform = 'mac' | 'win' | 'linux'

/**
 * Logical grouping for the settings page and conflict-detection rules:
 * - `global`        - application navigation outside modal overlays.
 * - `workspace`     - operations in the active chat workbench.
 * - `mediaLightbox` - operations in the active media preview overlay.
 */
export type KeyboardScope = 'global' | 'workspace' | 'mediaLightbox'

export interface KeyChord {
  key: string
  /** Command on macOS, Ctrl on Windows/Linux. Resolved per platform, not "meta or ctrl". */
  mod?: boolean
  alt?: boolean
  shift?: boolean
}

export interface KeyboardBinding extends KeyChord {
  command: AppKeyboardCommand
  /**
   * The base chord applies on every platform that does not declare its own.
   * A platform chord replaces the whole base chord, modifiers included.
   */
  mac?: KeyChord
  win?: KeyChord
  linux?: KeyChord
  repeat?: boolean
  /** A focused terminal hands this chord to the workbench instead of the shell. */
  escapesTerminal?: boolean
  desktop?: DesktopDelivery
  browser: BrowserBehavior
  scope: KeyboardScope
  /** camelCase id used as the i18n branch under `settings.keyboard.commands.<i18nKey>`. */
  i18nKey: string
}

/**
 * Single source of truth. Adding a shortcut is one row here; the Electron menu,
 * the Electron renderer keydown listener, and the web keydown listener all derive
 * their behavior from this table.
 */
export const keyboardBindings: KeyboardBinding[] = [
  {
    command: appKeyboardCommands.closeCurrentWorkspaceTab,
    key: 'w',
    mod: true,
    desktop: 'menu',
    browser: 'passthrough',
    scope: 'workspace',
    i18nKey: 'closeCurrentWorkspaceTab',
  },
  {
    command: appKeyboardCommands.saveActiveFile,
    key: 's',
    mod: true,
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'saveActiveFile',
  },
  {
    command: appKeyboardCommands.toggleSidebar,
    key: 'b',
    mod: true,
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'toggleSidebar',
  },
  {
    command: appKeyboardCommands.openSettings,
    key: 'k',
    mod: true,
    browser: 'intercept',
    scope: 'global',
    i18nKey: 'openSettings',
  },
  {
    command: appKeyboardCommands.newChatSession,
    key: 'n',
    alt: true,
    shift: true,
    mac: { key: 'n', mod: true, alt: true },
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'newChatSession',
  },
  {
    command: appKeyboardCommands.focusChatInput,
    key: 'Enter',
    alt: true,
    shift: true,
    mac: { key: 'Enter', mod: true, alt: true },
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'focusChatInput',
    escapesTerminal: true,
  },
  {
    command: appKeyboardCommands.showSessions,
    key: '1',
    alt: true,
    shift: true,
    mac: { key: '1', mod: true, alt: true },
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'showSessions',
    escapesTerminal: true,
  },
  {
    command: appKeyboardCommands.showFiles,
    key: '2',
    alt: true,
    shift: true,
    mac: { key: '2', mod: true, alt: true },
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'showFiles',
    escapesTerminal: true,
  },
  {
    command: appKeyboardCommands.showSchedule,
    key: '3',
    alt: true,
    shift: true,
    mac: { key: '3', mod: true, alt: true },
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'showSchedule',
    escapesTerminal: true,
  },
  {
    command: appKeyboardCommands.showSupermarket,
    key: '4',
    alt: true,
    shift: true,
    mac: { key: '4', mod: true, alt: true },
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'showSupermarket',
    escapesTerminal: true,
  },
  {
    command: appKeyboardCommands.nextWorkspaceTab,
    key: 'PageDown',
    alt: true,
    shift: true,
    mac: { key: ']', mod: true, alt: true },
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'nextWorkspaceTab',
    escapesTerminal: true,
    repeat: true,
  },
  {
    command: appKeyboardCommands.previousWorkspaceTab,
    key: 'PageUp',
    alt: true,
    shift: true,
    mac: { key: '[', mod: true, alt: true },
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'previousWorkspaceTab',
    escapesTerminal: true,
    repeat: true,
  },
  {
    command: appKeyboardCommands.splitWorkspaceRight,
    key: 'r',
    alt: true,
    shift: true,
    mac: { key: '.', mod: true, alt: true },
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'splitWorkspaceRight',
  },
  {
    command: appKeyboardCommands.splitWorkspaceBelow,
    key: 'b',
    alt: true,
    shift: true,
    mac: { key: ',', mod: true, alt: true },
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'splitWorkspaceBelow',
  },
  {
    command: appKeyboardCommands.newTerminal,
    key: 'x',
    alt: true,
    shift: true,
    mac: { key: 'x', mod: true, alt: true },
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'newTerminal',
  },
  {
    command: appKeyboardCommands.newBrowser,
    key: 'o',
    alt: true,
    shift: true,
    mac: { key: 'o', mod: true, alt: true },
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'newBrowser',
  },
  {
    command: appKeyboardCommands.closeMediaLightbox,
    key: 'Escape',
    browser: 'intercept',
    scope: 'mediaLightbox',
    i18nKey: 'closeMediaLightbox',
  },
  {
    command: appKeyboardCommands.mediaLightboxPrev,
    key: 'ArrowLeft',
    browser: 'intercept',
    scope: 'mediaLightbox',
    repeat: true,
    i18nKey: 'mediaLightboxPrev',
  },
  {
    command: appKeyboardCommands.mediaLightboxNext,
    key: 'ArrowRight',
    browser: 'intercept',
    scope: 'mediaLightbox',
    repeat: true,
    i18nKey: 'mediaLightboxNext',
  },
]

/**
 * Combos that the OS / browser owns when pressed with the platform mod key. A
 * binding must never claim `browser: 'intercept'` on one of these. Browsers do
 * not reliably let JS preventDefault them. Enforced by test, not at runtime, so
 * the invariant is visible at authoring time.
 */
export const RESERVED_BROWSER_COMBOS = new Set<string>(['w', 'q', 't', 'n'])

const SHARED_APP_MENU_COMBOS = ['Mod+r', 'Mod+Shift+r', 'Mod+m', 'Mod+0', 'Mod+=', 'Mod+Plus', 'Mod+Shift+Plus', 'Mod+-']

/** Accelerators of the desktop app menu roles (Electron 42 menu-item-roles) on each platform. */
export const RESERVED_APP_MENU_COMBOS: Record<KeyboardPlatform, string[]> = {
  mac: [...SHARED_APP_MENU_COMBOS, 'Mod+h', 'Mod+Alt+h', 'Mod+Alt+i'],
  win: [...SHARED_APP_MENU_COMBOS, 'Mod+Shift+i', 'F11'],
  linux: [...SHARED_APP_MENU_COMBOS, 'Mod+Shift+i', 'F11'],
}

const SHARED_TEXT_EDITING_COMBOS = ['Mod+c', 'Mod+v', 'Mod+x', 'Mod+z', 'Mod+Shift+z', 'Mod+a']

export const TEXT_EDITING_COMBOS: Record<KeyboardPlatform, string[]> = {
  mac: SHARED_TEXT_EDITING_COMBOS,
  win: [...SHARED_TEXT_EDITING_COMBOS, 'Mod+y'],
  linux: SHARED_TEXT_EDITING_COMBOS,
}

/** The binding with its chord for a platform; the platform chords are dropped once applied. */
export function resolveKeyboardBinding<T extends KeyChord & Partial<Record<KeyboardPlatform, KeyChord>>>(binding: T, platform: KeyboardPlatform): T {
  const chord = binding[platform]
  return {
    ...binding,
    ...(chord && { key: chord.key, mod: chord.mod, alt: chord.alt, shift: chord.shift }),
    mac: undefined,
    win: undefined,
    linux: undefined,
  }
}

/**
 * Best-effort platform detection for the keydown listener. Accepts a
 * navigator-like object for testability; defaults to the global navigator.
 * The mac vs non-mac distinction decides `mod`; win and linux differ only where
 * a binding declares a platform chord.
 */
export function detectPlatform(
  navigatorLike: { platform?: string; userAgent?: string } | undefined =
    typeof window === 'undefined' ? undefined : window.navigator,
): KeyboardPlatform {
  const haystack = `${navigatorLike?.platform ?? ''} ${navigatorLike?.userAgent ?? ''}`
  if (/mac/i.test(haystack)) return 'mac'
  if (/win/i.test(haystack)) return 'win'
  return 'linux'
}

// DOM KeyboardEvent.key names → Electron accelerator names. Electron's
// accelerator parser uses 'Left'/'Right'/'Up'/'Down' (not 'ArrowLeft' etc.)
// and 'Esc' (not 'Escape'); pushing the raw DOM names produces an invalid
// accelerator that the native menu silently rejects, so the menu shortcut
// goes dead the moment a user rebinds to one of these keys.
// https://www.electronjs.org/docs/latest/api/accelerator
const DOM_TO_ELECTRON_KEY: Record<string, string> = {
  ArrowLeft: 'Left',
  ArrowRight: 'Right',
  ArrowUp: 'Up',
  ArrowDown: 'Down',
  Escape: 'Esc',
  ' ': 'Space',
  '+': 'Plus',
}

function normalizeAcceleratorKey(key: string): string {
  const mapped = DOM_TO_ELECTRON_KEY[key]
  if (mapped) return mapped
  return key.length === 1 ? key.toUpperCase() : key
}

/** Derive an Electron accelerator string, e.g. `{ key: 'w', mod: true }` becomes `CmdOrCtrl+W`. */
export function toElectronAccelerator(binding: Pick<KeyboardBinding, 'key' | 'mod' | 'alt' | 'shift'>): string {
  const parts: string[] = []
  if (binding.mod) parts.push('CmdOrCtrl')
  if (binding.alt) parts.push('Alt')
  if (binding.shift) parts.push('Shift')
  parts.push(normalizeAcceleratorKey(binding.key))
  return parts.join('+')
}

/** Accelerator for a command, or undefined if no binding declares it. */
export function acceleratorForCommand(command: AppKeyboardCommand): string | undefined {
  const binding = keyboardBindings.find(b => b.command === command)
  return binding ? toElectronAccelerator(binding) : undefined
}

/** Bindings the web keydown listener should act on (browser-owned combos excluded). */
export function selectWebBindings(bindings: KeyboardBinding[]): KeyboardBinding[] {
  return bindings.filter(b => b.browser === 'intercept')
}
