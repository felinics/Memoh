import type { InjectionKey, Ref } from 'vue'

/**
 * Contract between the mobile settings shell (pages/settings-section) and the
 * content pages it hosts below the JS breakpoint. The desktop shell never
 * provides this — inject with a null default.
 *
 * `view` is the shell's current pane. `showList` swaps the shell back to the
 * full-screen settings list WITHOUT a route change: the list is a shell
 * state, not a route, so the caller's page keeps rendering (hidden) under it.
 * Pages that render their own full-screen chrome (the bot-detail
 * master-detail) use this to offer "back to the settings list" from their
 * own navigation.
 */
export interface SettingsMobileShell {
  readonly view: Readonly<Ref<'list' | 'content'>>
  showList: () => void
}

export const SettingsMobileShellKey: InjectionKey<SettingsMobileShell> = Symbol('memohai:settings-mobile-shell')
