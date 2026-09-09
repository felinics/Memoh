// Desktop renderer entry. Owns its bootstrap chain so desktop can layer
// on Electron-specific plugins / stores / providers without touching
// @memohai/web. Pairs with `src/renderer/index.html`.

import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { PiniaColada, useQueryCache } from '@pinia/colada'
import { createQueryCachePersistencePlugin, whenQueryCacheRestored } from '@memohai/web/lib/query-cache-persistence'
import piniaPluginPersistedstate from 'pinia-plugin-persistedstate'

import { watchEffect } from 'vue'
import i18n from '@memohai/web/i18n'
import { setupApiClient } from '@memohai/web/api-client'
import { installFileDropGuard } from '@memohai/web/lib/file-drop-guard'
import { appKeyboardCommands, createKeyboardCommandRegistry, type AppKeyboardCommand } from '@memohai/web/lib/keyboard-commands'
import { connectBrowserKeyboardShortcutsLive } from '@memohai/web/lib/browser-keyboard-shortcuts'
import { selectDesktopKeydownBindings, toElectronAccelerator } from '@memohai/web/lib/keyboard-bindings'
import { KEYBOARD_REGISTRY } from '@memohai/web/composables/useKeyboardCommand'
import { registerWorkspaceTabCommands } from '@memohai/web/pages/home/commands/workspace-tab-commands'
import { useWorkspaceTabsStore } from '@memohai/web/store/workspace-tabs'
import { useKeyboardShortcutsStore } from '@memohai/web/store/keyboard-shortcuts'
import { useChatStore } from '@memohai/web/store/chat-list'
import { normalizeDesktopThemeSource } from '../../shared/theme'

import '@fontsource-variable/inter'
import 'markstream-vue/index.css'
import '@memohai/web/style.css'
import './desktop-shell.css'
import 'katex/dist/katex.min.css'

import App from './chat/App.vue'
import router from './chat/router'
import { setupRendererCacheSync } from './renderer-cache-sync'
import { handleRendererNavigate } from './renderer-navigation'
import { isCurrentServerProbe } from './connect/connection-navigation'

// Native sidebar material exists only on macOS. Marking this before Vue mounts
// lets desktop-shell.css expose the native backdrop without changing Web or the
// opaque fallback used by Windows and Linux.
if (navigator.platform.toLowerCase().includes('mac')) {
  document.documentElement.dataset.nativeSidebarMaterial = ''
}

// Window-management fallback, intentionally kept separate from the close-tab app
// command. Cmd/Ctrl+W closes the active workspace tab; when the registry reports
// no tab claimed the command, the same key closes the window. The
// tab-close binding lives in the shared table; this window decision is an explicit
// desktop product rule, not something derived from that binding.
function closeWindowWhenNoTab(command: AppKeyboardCommand): void {
  if (command !== appKeyboardCommands.closeCurrentWorkspaceTab) return
  void window.api.window.closeSelf()
}

async function bootstrap() {
  // An unclaimed OS file drop would otherwise reach the shell's will-navigate
  // guard. Same guard as web, installed per renderer (see file-drop-guard.ts).
  installFileDropGuard()

  const pendingNavigationTargets: string[] = []
  let navigationReady = false
  window.api.window.onNavigate((target) => {
    if (!navigationReady) {
      pendingNavigationTargets.push(target)
      return
    }
    handleRendererNavigate(router, target)
  })

  const status = await window.api.desktop.getServerStatus()
  setupApiClient({
    baseUrl: status.baseUrl || 'http://127.0.0.1:0',
    onUnauthorized: () => router.replace({ name: 'Login' }),
  })
  const serverProbe = window.api.desktop.probeServer()

  const pinia = createPinia().use(piniaPluginPersistedstate)
  // NSVisualEffectView follows Electron's nativeTheme rather than the page's
  // `.dark` class. Seed it before mounting so a persisted manual preference
  // cannot flash the opposite material; App.vue keeps later changes in sync
  // from component setup, where the settings store can safely use vue-i18n.
  try {
    await window.api.desktop.setThemeSource(
      normalizeDesktopThemeSource(localStorage.getItem('theme')),
    )
  } catch (error) {
    console.warn('failed to synchronize desktop native theme', error)
  }
  const keyboardCommands = createKeyboardCommandRegistry()
  registerWorkspaceTabCommands(keyboardCommands, useWorkspaceTabsStore(pinia))
  // Menu-delivered commands arrive over IPC; closing the window when no tab
  // remains is a distinct window-management concern (see closeWindowWhenNoTab).
  keyboardCommands.connect(window.api.window, closeWindowWhenNoTab)
  // keydown-delivered commands (e.g. save) share the exact web matcher; menu
  // commands are excluded here so they never double-fire with the accelerator.
  // Reading bindings from the store on every keydown lets user overrides from
  // the Keyboard Shortcuts settings page take effect immediately.
  const shortcutsStore = useKeyboardShortcutsStore(pinia)
  connectBrowserKeyboardShortcutsLive(
    keyboardCommands,
    () => selectDesktopKeydownBindings(shortcutsStore.effectiveBindings),
  )
  // Push the latest accelerators for menu-delivered commands to main so the
  // native menu items stay in sync with whatever the user has bound — without
  // this, the menu's accelerator label and matching combo would freeze at the
  // table's default.
  watchEffect(() => {
    const overrides: Record<string, string> = {}
    for (const binding of shortcutsStore.effectiveBindings) {
      if (binding.desktop !== 'menu') continue
      overrides[binding.command] = toElectronAccelerator(binding)
    }
    // Surface IPC failures: a silent reject would leave the native menu frozen
    // at the table's default accelerator until the next mutation, which the
    // user has no way to diagnose.
    window.api.desktop.setMenuAccelerators(overrides).catch((error) => {
      console.warn('failed to push menu accelerators to main', error)
    })
  })

  keyboardCommands.register(appKeyboardCommands.openSettings, () => {
    // Already inside settings → no-op. Pushing /settings would redirect to
    // /settings/bots and yank the user off whatever settings page they were on.
    if (router.currentRoute.value.path.startsWith('/settings')) return true
    void router.push('/settings').catch(() => {})
    return true
  })

  const app = createApp(App)
    .use(pinia)
    .use(PiniaColada, {
      plugins: [
        // Same cross-reload snapshot as the web app (see
        // @memohai/web/lib/query-cache-persistence.ts).
        createQueryCachePersistencePlugin(),
      ],
    })
    .use(router)
    .use(i18n)
    .provide(KEYBOARD_REGISTRY, keyboardCommands)

  // Bridge query-cache invalidations to other renderer hosts.
  // Must run after `PiniaColada` is installed so the store is registered.
  setupRendererCacheSync(useQueryCache())

  // The composer reads its agent list from the chat store's one-shot bot
  // snapshot, not the Colada cache, so the sync above can't refresh it. When the
  // settings window mutates bot config — enabling an ACP agent, renaming,
  // switching model — it invalidates the bots list / ['bot', <id>]; mirror that
  // into a store re-pull so the chat window's agent menu updates without a
  // manual reload. (Web does this via its route watcher when leaving the
  // settings overlay; desktop's chat window route never enters settings.)
  const chatStore = useChatStore(pinia)
  window.api.desktop.onInvalidate((payload) => {
    const key = payload?.filters?.key
    const head: unknown = Array.isArray(key) ? key[0] : undefined
    // The bots list entry lives under the SDK-generated object key
    // ([{ _id: 'getBots', … }]); accept the legacy string head 'bots' too so
    // an older settings window's broadcast still lands.
    const isBotsList = typeof head === 'object' && head !== null
      && (head as { _id?: unknown })._id === 'getBots'
    if (head === 'bots' || head === 'bot' || isBotsList) {
      void chatStore.refreshBots()
    }
  })

  // Mirror the chat store's bot list into the native tray menu. The main
  // process holds no credentials, so the renderer — owner of the
  // authenticated SDK session — pushes the list whenever it changes
  // (initial fetch, invalidation-driven refreshes, sign-out clearing it).
  watchEffect(() => {
    const trayBots = chatStore.bots
      .map(bot => ({
        id: (bot.id ?? '').trim(),
        displayName: bot.display_name?.trim() || bot.name?.trim() || (bot.id ?? '').trim(),
      }))
      .filter(bot => bot.id)
    window.api.desktop.setTrayBots(trayBots).catch((error) => {
      console.warn('failed to push tray bots to main', error)
    })
  })

  // Mount only after the snapshot is hydrated so the first render already
  // has the last-known values (storage is sync, so this resolves fast).
  await whenQueryCacheRestored()
  app.mount('#app')

  // The first frame stays optimistic: render the normal auth/app route while
  // the main process checks the configured server in the background. Local
  // connection refusals resolve immediately; only a genuinely slow endpoint
  // consumes the full timeout.
  void serverProbe
    .then(async (result) => {
      if (result.ok || router.currentRoute.value.name === 'ConnectServer') return

      // A user can switch servers while the startup probe is still pending.
      // Ignore a late failure from the old server instead of pulling the newly
      // connected session back to the connection screen.
      const currentStatus = await window.api.desktop.getServerStatus()
      if (!isCurrentServerProbe(result.baseUrl, currentStatus.baseUrl)) return
      if (router.currentRoute.value.name !== 'ConnectServer') {
        await router.replace({ name: 'ConnectServer' })
      }
    })
    .catch(async () => {
      if (router.currentRoute.value.name !== 'ConnectServer') {
        const currentStatus = await window.api.desktop.getServerStatus()
        if (isCurrentServerProbe(status.baseUrl, currentStatus.baseUrl)) {
          await router.replace({ name: 'ConnectServer' })
        }
      }
    })

  for (const target of pendingNavigationTargets.splice(0)) {
    handleRendererNavigate(router, target)
  }
  navigationReady = true
}

void bootstrap()
