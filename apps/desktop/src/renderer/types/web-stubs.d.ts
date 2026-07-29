// Type stubs for @memohai/web subpath imports consumed by the renderer.
// We route typechecking through these stubs (via tsconfig `paths`) so vue-tsc
// does not recursively typecheck @memohai/web's source tree. @memohai/web
// owns its own types/CI. Vite ignores `paths` and resolves the real `exports`
// entries at bundle time, so runtime behavior is unchanged.

// Marker to make this file a module (not an ambient script). Required so
// that paths-mapped dynamic `import('@memohai/web/...')` calls succeed.
// TS otherwise complains that the resolved file "is not a module".
export {}

declare module '@memohai/web/router-guards/onboarding' {
  export function ensureOnboarding(): Promise<boolean>
}

declare module '@memohai/web/router' {
  import type { Router } from 'vue-router'
  const router: Router
  export default router
}

declare module '@memohai/web/i18n' {
  import type { I18n } from 'vue-i18n'
  import type { ComputedRef } from 'vue'
  const i18n: I18n
  export default i18n
  export function i18nRef(key: string): ComputedRef<string>
}

declare module '@memohai/web/api-client' {
  export interface SetupApiClientOptions {
    baseUrl?: string
    fetch?: typeof fetch
    onUnauthorized?: () => void
  }
  export interface SdkUrlOptions {
    url: string
    path?: Record<string, unknown>
    query?: Record<string, unknown>
  }
  export function sdkAuthQuery(): { token?: string }
  export function sdkApiBaseUrl(): string
  export function sdkApiUrl(options: SdkUrlOptions): string
  export function sdkWebSocketUrl(options: SdkUrlOptions): string
  export function setupApiClient(options?: SetupApiClientOptions): void
}

declare module '@memohai/web/lib/keyboard-commands' {
  export const appKeyboardCommands: {
    readonly closeCurrentWorkspaceTab: 'close-current-workspace-tab'
    readonly saveActiveFile: 'save-active-file'
    readonly toggleSidebar: 'toggle-sidebar'
    readonly openSettings: 'open-settings'
    readonly closeMediaLightbox: 'close-media-lightbox'
    readonly mediaLightboxPrev: 'media-lightbox-prev'
    readonly mediaLightboxNext: 'media-lightbox-next'
  }
  export type AppKeyboardCommand =
    typeof appKeyboardCommands[keyof typeof appKeyboardCommands]
  export type KeyboardCommandHandler = () => boolean | void
  export type UnhandledKeyboardCommandCallback = (command: AppKeyboardCommand) => void
  export interface KeyboardCommandApi {
    onKeyboardCommand(cb: (command: AppKeyboardCommand) => void): (() => void) | void
  }
  export interface KeyboardCommandRegistry {
    register(command: AppKeyboardCommand, handler: KeyboardCommandHandler): () => void
    dispatch(command: AppKeyboardCommand): boolean
    connect(api: KeyboardCommandApi, onUnhandled?: UnhandledKeyboardCommandCallback): () => void
  }
  export function isAppKeyboardCommand(value: unknown): value is AppKeyboardCommand
  export function createKeyboardCommandRegistry(): KeyboardCommandRegistry
}

declare module '@memohai/web/lib/keyboard-bindings' {
  import type { AppKeyboardCommand } from '@memohai/web/lib/keyboard-commands'
  export type DesktopDelivery = 'menu' | 'keydown'
  export type BrowserBehavior = 'intercept' | 'passthrough'
  export type KeyboardScope = 'global' | 'mediaLightbox'
  export interface KeyboardBinding {
    command: AppKeyboardCommand
    key: string
    mod?: boolean
    alt?: boolean
    shift?: boolean
    desktop: DesktopDelivery
    browser: BrowserBehavior
    scope: KeyboardScope
    i18nKey: string
  }
  export const keyboardBindings: KeyboardBinding[]
  export const RESERVED_BROWSER_COMBOS: Set<string>
  export function toElectronAccelerator(binding: KeyboardBinding): string
  export function acceleratorForCommand(command: AppKeyboardCommand): string | undefined
  export function selectWebBindings(bindings: KeyboardBinding[]): KeyboardBinding[]
  export function selectDesktopKeydownBindings(bindings: KeyboardBinding[]): KeyboardBinding[]
}

declare module '@memohai/web/lib/browser-keyboard-shortcuts' {
  import type { AppKeyboardCommand, KeyboardCommandRegistry } from '@memohai/web/lib/keyboard-commands'
  export interface BrowserKeyboardShortcutBinding {
    command: AppKeyboardCommand
    key: string
    mod?: boolean
    alt?: boolean
    shift?: boolean
  }
  export function handleBrowserKeyboardShortcut(
    event: {
      key: string
      metaKey: boolean
      ctrlKey: boolean
      altKey: boolean
      shiftKey: boolean
      preventDefault(): void
    },
    registry: Pick<KeyboardCommandRegistry, 'dispatch'>,
    bindings: BrowserKeyboardShortcutBinding[],
  ): boolean
  export function connectBrowserKeyboardShortcuts(
    registry: Pick<KeyboardCommandRegistry, 'dispatch'>,
    bindings: BrowserKeyboardShortcutBinding[],
    target?: unknown,
  ): () => void
  export function connectBrowserKeyboardShortcutsLive(
    registry: Pick<KeyboardCommandRegistry, 'dispatch'>,
    getBindings: () => BrowserKeyboardShortcutBinding[],
    target?: unknown,
  ): () => void
}

declare module '@memohai/web/store/keyboard-shortcuts' {
  import type { KeyboardBinding } from '@memohai/web/lib/keyboard-bindings'
  import type { AppKeyboardCommand } from '@memohai/web/lib/keyboard-commands'
  export type ConflictKind = 'none' | 'same-scope' | 'cross-scope' | 'reserved' | 'invalid'
  export interface ConflictResult {
    kind: ConflictKind
    collidesWith?: AppKeyboardCommand
  }
  export function useKeyboardShortcutsStore(pinia?: unknown): {
    overrides: Record<string, string>
    effectiveBindings: KeyboardBinding[]
    isOverridden(command: AppKeyboardCommand): boolean
    detectConflict(command: AppKeyboardCommand, combo: unknown): ConflictResult
    setBinding(command: AppKeyboardCommand, combo: string): ConflictResult
    resetBinding(command: AppKeyboardCommand): void
    resetAll(): void
  }
}

declare module '@memohai/web/composables/useKeyboardCommand' {
  import type { InjectionKey } from 'vue'
  import type {
    AppKeyboardCommand,
    KeyboardCommandHandler,
    KeyboardCommandRegistry,
  } from '@memohai/web/lib/keyboard-commands'
  export const KEYBOARD_REGISTRY: InjectionKey<KeyboardCommandRegistry>
  export function useKeyboardCommand(command: AppKeyboardCommand, handler: KeyboardCommandHandler): void
}

declare module '@memohai/web/pages/home/commands/workspace-tab-commands' {
  import type { AppKeyboardCommand, KeyboardCommandRegistry } from '@memohai/web/lib/keyboard-commands'
  export interface WorkspaceTabCommandStore {
    activeId: string | null
    closeTab(id: string): void
  }
  export function handleWorkspaceKeyboardCommand(
    command: AppKeyboardCommand,
    store: WorkspaceTabCommandStore,
  ): boolean
  export function registerWorkspaceTabCommands(
    registry: Pick<KeyboardCommandRegistry, 'register'>,
    store: WorkspaceTabCommandStore,
  ): () => void
}

declare module '@memohai/web/store/settings' {
  // We don't need the concrete Pinia store type here. Desktop just calls the
  // composable for its registration side-effect.
  export function useSettingsStore(): unknown
}

declare module '@memohai/web/store/workspace-tabs' {
  export function useWorkspaceTabsStore(pinia?: unknown): {
    activeId: string | null
    closeTab: (id: string) => void
  }
}

declare module '@memohai/web/store/user' {
  export function useUserStore(): {
    userInfo: {
      role: string
    }
  }
}

declare module '@memohai/web/store/chat-list' {
  // Desktop needs the bot snapshot for two things: re-pulling it when a
  // sibling window invalidates bot config (composer agent menu), and
  // mirroring the list into the native tray's Bots submenu.
  export function useChatStore(pinia?: unknown): {
    bots: Array<{
      id?: string
      name?: string
      display_name?: string
    }>
    refreshBots: () => Promise<void>
  }
}

declare module '@memohai/web/store/capabilities' {
  export function useCapabilitiesStore(): {
    load: () => Promise<void>
  }
}

declare module '@memohai/web/store/update' {
  export function useUpdateStore(): {
    checking: boolean
    checked: boolean
    hasUpdate: boolean
    latestVersion: string
    releaseBody: string
    releaseUrl: string
    check: () => Promise<boolean>
    checkAtStartup: () => Promise<void>
  }
}

declare module '@memohai/web/composables/useDialogMutation' {
  export function useDialogMutation(): {
    run: <T>(action: () => Promise<T>, options?: { fallbackMessage?: string, onSuccess?: () => void }) => Promise<T | undefined>
  }
}

declare module '@memohai/web/constants/acl-presets' {
  export const defaultAclPreset: string
  export const aclPresetOptions: Array<{ value: string, titleKey: string, descriptionKey?: string }>
}

declare module '@memohai/web/utils/timezones' {
  export const emptyTimezoneValue: string
}

declare module '@memohai/web/lib/desktop-shell' {
  import type { InjectionKey } from 'vue'
  export const DesktopShellKey: InjectionKey<boolean>
  export type DesktopRuntimeStatus = 'disabled' | 'connecting' | 'connected' | 'disconnected' | 'stopped' | 'error'
  export interface DesktopRuntimeState {
    enabled: boolean
    runtimeId?: string
    runtimeName?: string
    status: DesktopRuntimeStatus
    deviceName: string
    error?: string
  }
  export interface DesktopRuntimeBridge {
    runtimeState(): Promise<DesktopRuntimeState>
    configureRuntime(config: { runtimeId: string, name: string, key: string, teamId?: string } | null): Promise<DesktopRuntimeState>
    onRuntimeStateChanged(listener: (state: DesktopRuntimeState) => void): () => void
  }
  export const DesktopRuntimeKey: InjectionKey<DesktopRuntimeBridge | undefined>
  export type DesktopUpdateStatus =
    | 'idle'
    | 'checking'
    | 'up-to-date'
    | 'available'
    | 'downloading'
    | 'downloaded'
    | 'error'
    | 'unavailable'
  export interface DesktopUpdateInfo {
    version: string
    platform: string
    enabled: boolean
  }
  export interface DesktopUpdateState {
    status: DesktopUpdateStatus
    currentVersion: string
    latestVersion: string | null
    progress: number | null
    error: string | null
  }
  export interface DesktopUpdateBridge {
    getInfo(): Promise<DesktopUpdateInfo>
    getState(): Promise<DesktopUpdateState>
    check(): Promise<DesktopUpdateState>
    download(): Promise<DesktopUpdateState>
    install(): Promise<DesktopUpdateState>
    onStateChanged(listener: (state: DesktopUpdateState) => void): () => void
  }
  export const DesktopUpdatesKey: InjectionKey<DesktopUpdateBridge | undefined>
}

declare module '@memohai/web/lib/auth-session' {
  export type AuthSessionClearReason = 'login' | 'logout' | 'token-cleared' | 'unauthorized'
  export function notifyAuthSessionCleared(reason: AuthSessionClearReason): void
}

declare module '@memohai/web/pages/login/transition' {
  export const LOGIN_ENTRY_ANIMATION_KEY: string
}

declare module '@memohai/web/composables/useBackOr' {
  import type { ComputedRef } from 'vue'
  import type { RouteLocationRaw, Router } from 'vue-router'
  export function installBackHistory(router: Router): void
  export function useBackOr(fallback: RouteLocationRaw): () => void
  export function useBackAffordance(fallback: RouteLocationRaw): {
    onBack: () => void
    label: ComputedRef<string>
  }
}

declare module '@memohai/web/style.css'

// Fallback for every Vue SFC reachable through the @memohai/web/* wildcard
// export. The TS ambient-module `*` token matches multi-segment paths
// (slashes included), so this single declaration covers `pages/.../*.vue`,
// `components/.../*.vue`, `layout/.../*.vue`, etc.
declare module '@memohai/web/*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<Record<string, never>, Record<string, never>, unknown>
  export default component
}
