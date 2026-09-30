import { appKeyboardCommands, type AppKeyboardCommand } from './keyboard-commands'

/**
 * - `menu`    - also exposed in the native menu; menu clicks route via IPC.
 * - `keydown` - exposed through the shared renderer keydown listener.
 * Both use the shared DOM listener for physical keys in the chat window.
 */
export type DesktopDelivery = 'menu' | 'keydown'

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

export interface KeyboardBinding {
  command: AppKeyboardCommand
  /**
   * Default key. `mod`-based bindings that use the same key on every platform
   * only need this. Use per-platform overrides only when a shortcut genuinely
   * diverges.
   */
  key: string
  mac?: string
  win?: string
  linux?: string
  /** Command on macOS, Ctrl on Windows/Linux. Resolved per platform, not "meta or ctrl". */
  mod?: boolean
  alt?: boolean
  shift?: boolean
  repeat?: boolean
  desktop: DesktopDelivery
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
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'saveActiveFile',
  },
  {
    command: appKeyboardCommands.toggleSidebar,
    key: 'b',
    mod: true,
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'toggleSidebar',
  },
  {
    command: appKeyboardCommands.openSettings,
    key: 'k',
    mod: true,
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'global',
    i18nKey: 'openSettings',
  },
  {
    command: appKeyboardCommands.newChatSession,
    key: 'n',
    mod: true,
    alt: true,
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'newChatSession',
  },
  {
    command: appKeyboardCommands.focusChatInput,
    key: 'Enter',
    mod: true,
    alt: true,
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'focusChatInput',
  },
  {
    command: appKeyboardCommands.showSessions,
    key: '1',
    mod: true,
    alt: true,
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'showSessions',
  },
  {
    command: appKeyboardCommands.showFiles,
    key: '2',
    mod: true,
    alt: true,
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'showFiles',
  },
  {
    command: appKeyboardCommands.showSchedule,
    key: '3',
    mod: true,
    alt: true,
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'showSchedule',
  },
  {
    command: appKeyboardCommands.showSupermarket,
    key: '4',
    mod: true,
    alt: true,
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'showSupermarket',
  },
  {
    command: appKeyboardCommands.nextWorkspaceTab,
    key: ']',
    mod: true,
    alt: true,
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'nextWorkspaceTab',
    repeat: true,
  },
  {
    command: appKeyboardCommands.previousWorkspaceTab,
    key: '[',
    mod: true,
    alt: true,
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'previousWorkspaceTab',
    repeat: true,
  },
  {
    command: appKeyboardCommands.splitWorkspaceRight,
    key: '.',
    mod: true,
    alt: true,
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'splitWorkspaceRight',
  },
  {
    command: appKeyboardCommands.splitWorkspaceBelow,
    key: ',',
    mod: true,
    alt: true,
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'splitWorkspaceBelow',
  },
  {
    command: appKeyboardCommands.newTerminal,
    key: 'x',
    mod: true,
    alt: true,
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'newTerminal',
  },
  {
    command: appKeyboardCommands.newBrowser,
    key: 'o',
    mod: true,
    alt: true,
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'workspace',
    i18nKey: 'newBrowser',
  },
  {
    command: appKeyboardCommands.closeMediaLightbox,
    key: 'Escape',
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'mediaLightbox',
    i18nKey: 'closeMediaLightbox',
  },
  {
    command: appKeyboardCommands.mediaLightboxPrev,
    key: 'ArrowLeft',
    desktop: 'keydown',
    browser: 'intercept',
    scope: 'mediaLightbox',
    repeat: true,
    i18nKey: 'mediaLightboxPrev',
  },
  {
    command: appKeyboardCommands.mediaLightboxNext,
    key: 'ArrowRight',
    desktop: 'keydown',
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

/** The effective key for a platform: a per-platform override, else the base key. */
export function resolveBindingKey(
  binding: { key: string; mac?: string; win?: string; linux?: string },
  platform: KeyboardPlatform,
): string {
  return binding[platform] ?? binding.key
}

/**
 * Best-effort platform detection for the keydown listener. Accepts a
 * navigator-like object for testability; defaults to the global navigator.
 * Only the mac vs non-mac distinction affects `mod`; win vs linux matters only
 * for bindings that declare divergent per-platform keys.
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

/** Bindings the Electron renderer keydown listener should act on (menu-owned excluded). */
export function selectDesktopKeydownBindings(bindings: KeyboardBinding[]): KeyboardBinding[] {
  return bindings.filter(b => b.desktop === 'keydown')
}
