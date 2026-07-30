import {
  app,
  Menu,
  shell,
  BrowserWindow,
  ipcMain,
  nativeImage,
  safeStorage,
  Tray,
  screen,
  type BrowserWindowConstructorOptions,
  type IpcMainInvokeEvent,
  type MenuItemConstructorOptions,
} from 'electron'
import { join } from 'node:path'
import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { homedir, hostname } from 'node:os'
import { electronApp, optimizer, is } from '@electron-toolkit/utils'
import iconPng from '../../resources/icon.png?asset'
import trayIconPng from '../../resources/tray-icon.png?asset'
import { acceleratorForCommand, appKeyboardCommands, type AppKeyboardCommand } from '../shared/keyboard-commands'
import { dispatchFocusedWindowCommand } from './window-commands'
import { dispatchRendererNavigate } from './window-navigation'
import { maybeSelfInstallMacOS } from './self-install'
import { DesktopRemoteRuntimeManager } from './remote-runtime'
import { isTrustedRendererUrl } from './renderer-trust'
import { registerDesktopUpdates } from './updates'
import {
  normalizeBaseUrl,
  normalizeServerInput,
  probeServerBaseUrl,
  resolveDesktopBaseUrl,
  type ServerConnectResult,
  type ServerConnectionResult,
} from '../shared/server-connection'
import type { DesktopRuntimeConfig } from '../shared/remote-runtime'

const DESKTOP_PRODUCT_NAME = 'Memoh'
const DEFAULT_BASE_URL = is.dev ? 'http://localhost:18080' : 'http://localhost:8080'
const guardedExternalLinkWebContents = new WeakSet<Electron.WebContents>()

// Must run before anything resolves `app.getPath('userData')`.
app.setName(DESKTOP_PRODUCT_NAME)

const CHAT_DEFAULTS = { width: 1280, height: 800, minWidth: 960, minHeight: 600 }
type WindowKind = 'chat'
type WindowDefaults = {
  width: number
  height: number
  minWidth: number
  minHeight: number
}
type StoredWindowState = {
  width?: number
  height?: number
  maximized?: boolean
}
type StoredWindowStates = Partial<Record<WindowKind, StoredWindowState>>
type TrayBot = {
  id: string
  displayName: string
}
type TraySettingsItem = {
  label: string
  target: string
}

let chatWindow: BrowserWindow | null = null
let appTray: Tray | null = null
let isQuitting = false
let windowStatesCache: StoredWindowStates | null = null
let sessionBaseUrlOverride = ''
let remoteRuntimeManager: DesktopRemoteRuntimeManager | null = null
let quitCleanupFinished = false
let quitCleanupPromise: Promise<void> | null = null

function windowStatePath(): string {
  return join(app.getPath('userData'), 'window-state.json')
}

function readWindowStates(): StoredWindowStates {
  if (windowStatesCache) return windowStatesCache
  const path = windowStatePath()
  if (!existsSync(path)) {
    windowStatesCache = {}
    return windowStatesCache
  }
  try {
    const parsed = JSON.parse(readFileSync(path, 'utf8')) as StoredWindowStates
    windowStatesCache = typeof parsed === 'object' && parsed !== null ? parsed : {}
  } catch (error) {
    console.error('failed to read desktop window state', error)
    windowStatesCache = {}
  }
  return windowStatesCache
}

function writeWindowState(kind: WindowKind, state: StoredWindowState): void {
  const states = {
    ...readWindowStates(),
    [kind]: state,
  }
  windowStatesCache = states
  mkdirSync(app.getPath('userData'), { recursive: true })
  writeFileSync(windowStatePath(), `${JSON.stringify(states, null, 2)}\n`, { mode: 0o600 })
}

function clampWindowDimension(raw: unknown, fallback: number, minimum: number, maximum: number): number {
  const value = typeof raw === 'number' && Number.isFinite(raw) ? Math.round(raw) : fallback
  return Math.min(Math.max(value, minimum), maximum)
}

function rememberedWindowOptions(kind: WindowKind, defaults: WindowDefaults): BrowserWindowConstructorOptions {
  const state = readWindowStates()[kind] ?? {}
  const area = screen.getPrimaryDisplay().workAreaSize
  const maxWidth = Math.max(defaults.minWidth, area.width)
  const maxHeight = Math.max(defaults.minHeight, area.height)
  return {
    ...defaults,
    width: clampWindowDimension(state.width, defaults.width, defaults.minWidth, maxWidth),
    height: clampWindowDimension(state.height, defaults.height, defaults.minHeight, maxHeight),
  }
}

function attachWindowStatePersistence(window: BrowserWindow, kind: WindowKind, defaults: WindowDefaults): void {
  let timer: ReturnType<typeof setTimeout> | null = null
  const save = (): void => {
    if (window.isDestroyed()) return
    const bounds = window.isMaximized() ? window.getNormalBounds() : window.getBounds()
    const area = screen.getDisplayMatching(window.getBounds()).workAreaSize
    const maxWidth = Math.max(defaults.minWidth, area.width)
    const maxHeight = Math.max(defaults.minHeight, area.height)
    writeWindowState(kind, {
      width: clampWindowDimension(bounds.width, defaults.width, defaults.minWidth, maxWidth),
      height: clampWindowDimension(bounds.height, defaults.height, defaults.minHeight, maxHeight),
      maximized: window.isMaximized(),
    })
  }
  const scheduleSave = (): void => {
    if (timer) clearTimeout(timer)
    timer = setTimeout(save, 300)
  }

  window.on('resize', scheduleSave)
  window.on('maximize', save)
  window.on('unmaximize', save)
  window.on('close', save)
  window.on('closed', () => {
    if (timer) clearTimeout(timer)
  })
}

function restoreWindowMaximized(window: BrowserWindow, kind: WindowKind): void {
  if (readWindowStates()[kind]?.maximized) {
    window.maximize()
  }
}

function desktopProfilePath(): string {
  return join(app.getPath('userData'), 'remote-profile.json')
}

function readProfileBaseUrl(): string {
  const path = desktopProfilePath()
  if (!existsSync(path)) return ''
  try {
    const parsed = JSON.parse(readFileSync(path, 'utf8')) as { baseUrl?: unknown }
    if (typeof parsed.baseUrl !== 'string' || !parsed.baseUrl.trim()) return ''
    return normalizeBaseUrl(parsed.baseUrl)
  } catch (error) {
    console.error('failed to read remote desktop profile', error)
    return ''
  }
}

function writeProfileBaseUrl(baseUrl: string): void {
  mkdirSync(app.getPath('userData'), { recursive: true })
  const path = desktopProfilePath()
  writeFileSync(path, `${JSON.stringify({ baseUrl }, null, 2)}\n`, { mode: 0o600 })
  chmodSync(path, 0o600)
}

function getDesktopApiBaseUrl(): string {
  return resolveDesktopBaseUrl({
    session: sessionBaseUrlOverride,
    desktop: process.env.MEMOH_DESKTOP_BASE_URL,
    proxy: process.env.MEMOH_WEB_PROXY_TARGET,
    vite: process.env.VITE_API_URL,
    profile: readProfileBaseUrl(),
    fallback: DEFAULT_BASE_URL,
  })
}

function getDesktopServerStatus() {
  const baseUrl = getDesktopApiBaseUrl()
  return {
    baseUrl,
    managed: Boolean(
      process.env.MEMOH_DESKTOP_BASE_URL?.trim()
      || process.env.MEMOH_WEB_PROXY_TARGET?.trim()
      || process.env.VITE_API_URL?.trim(),
    ),
  }
}

async function probeConfiguredServer(): Promise<ServerConnectionResult> {
  return probeServerBaseUrl(getDesktopApiBaseUrl())
}

async function connectToServer(rawBaseUrl: unknown): Promise<ServerConnectResult> {
  const previousBaseUrl = getDesktopApiBaseUrl()
  const normalized = normalizeServerInput(rawBaseUrl)
  if (!normalized.ok) return normalized
  const result = await probeServerBaseUrl(normalized.baseUrl)
  if (!result.ok) return result
  writeProfileBaseUrl(result.baseUrl)
  sessionBaseUrlOverride = result.baseUrl
  const changed = result.baseUrl !== previousBaseUrl
  if (changed) {
    await remoteRuntimeManager?.restore()
  }
  return {
    ...result,
    changed,
  }
}

async function finishQuitCleanup(): Promise<void> {
  if (quitCleanupFinished) return
  if (!quitCleanupPromise) {
    quitCleanupPromise = (remoteRuntimeManager?.stop() ?? Promise.resolve())
      .catch(error => console.warn('failed to stop desktop runtime during quit', error))
      .then(() => {
        quitCleanupFinished = true
      })
  }
  await quitCleanupPromise
}

app.on('before-quit', (event) => {
  isQuitting = true
  if (quitCleanupFinished) return
  event.preventDefault()
  void finishQuitCleanup().then(() => app.quit())
})

function applyExternalLinkHandler(window: BrowserWindow): void {
  attachExternalLinkGuards(window.webContents)
}

function productionRendererEntry(): string {
  return join(__dirname, '../renderer/index.html')
}

function isTrustedRendererNavigation(url: string): boolean {
  return isTrustedRendererUrl(url, {
    devBaseUrl: is.dev ? process.env.ELECTRON_RENDERER_URL : undefined,
    productionEntry: productionRendererEntry(),
  })
}

function assertTrustedRenderer(event: IpcMainInvokeEvent): void {
  const senderWindow = BrowserWindow.fromWebContents(event.sender)
  const senderUrl = event.senderFrame?.url || event.sender.getURL()
  if (!chatWindow || senderWindow !== chatWindow || !isTrustedRendererNavigation(senderUrl)) {
    throw new Error('IPC request rejected from an untrusted renderer')
  }
}

function normalizeExternalUrl(rawURL: unknown): { url: string, protocol: string, supported: boolean } {
  const url = typeof rawURL === 'string' ? rawURL.trim() : ''
  let protocol = ''
  try {
    protocol = new URL(url).protocol
  } catch {
    protocol = ''
  }
  return {
    url,
    protocol,
    supported: ['http:', 'https:', 'mailto:'].includes(protocol),
  }
}

function attachExternalLinkGuards(webContents: Electron.WebContents): void {
  if (guardedExternalLinkWebContents.has(webContents)) return
  guardedExternalLinkWebContents.add(webContents)

  webContents.setWindowOpenHandler(({ url }) => {
    const external = normalizeExternalUrl(url)
    if (!external.supported) {
      console.warn('blocked unsupported window.open URL', external.url || url)
      return { action: 'deny' }
    }
    void shell.openExternal(external.url).catch((error) => {
      console.error('failed to open external URL', external.url, error)
    })
    return { action: 'deny' }
  })

  const guardNavigation = (event: Electron.Event, url: string): void => {
    if (isTrustedRendererNavigation(url)) return
    event.preventDefault()
    const external = normalizeExternalUrl(url)
    if (!external.supported) {
      console.warn('blocked untrusted navigation URL', external.url || url)
      return
    }
    void shell.openExternal(external.url).catch((error) => {
      console.error('failed to open external navigation URL', external.url, error)
    })
  }
  webContents.on('will-navigate', guardNavigation)
  webContents.on('will-redirect', guardNavigation)
}

async function openExternalUrl(rawURL: unknown): Promise<void> {
  const external = normalizeExternalUrl(rawURL)
  if (!external.supported) {
    const blockedURL = external.url || String(rawURL ?? '')
    console.warn('blocked unsupported external URL', blockedURL)
    throw new Error(`Unsupported external URL: ${blockedURL || 'empty URL'}`)
  }
  await shell.openExternal(external.url)
}

function loadRendererEntry(window: BrowserWindow, entry: 'index'): void {
  const base = process.env.ELECTRON_RENDERER_URL
  if (is.dev && base) {
    window.loadURL(`${base}/${entry}.html`)
    return
  }
  window.loadFile(productionRendererEntry())
}

function createTrayIcon(): Electron.NativeImage {
  const image = nativeImage.createFromPath(trayIconPng)
  if (process.platform === 'darwin') {
    const trayImage = image.resize({ width: 18, height: 18 })
    trayImage.setTemplateImage(true)
    return trayImage
  }
  return image.resize({ width: 24, height: 24 })
}

function revealChatWindow(): void {
  const window = ensureWindow('chat')
  focusWindow(window)
}

function openBotWorkspace(botId: string): void {
  const id = botId.trim()
  if (!id) return
  const window = ensureWindow('chat')
  focusWindow(window)
  const target = `/bot/${encodeURIComponent(id)}`
  dispatchRendererNavigate(window, target)
}

const SETTINGS_TRAY_ITEMS: TraySettingsItem[] = [
  { label: 'Bots', target: '/settings/bots' },
  { label: 'Providers', target: '/settings/providers' },
  { label: 'Memory', target: '/settings/memory' },
  { label: 'Web Search', target: '/settings/web-search' },
  { label: 'Voice', target: '/settings/voice' },
  { label: 'Email', target: '/settings/email' },
  { label: 'Supermarket', target: '/settings/supermarket' },
  { label: 'Usage', target: '/settings/usage' },
  { label: 'Members', target: '/settings/people' },
  { label: 'Appearance', target: '/settings/appearance' },
  { label: 'Keyboard', target: '/settings/keyboard' },
  { label: 'Profile', target: '/settings/profile' },
  { label: 'About', target: '/settings/about' },
]

function openSettingsRoute(target?: string): void {
  const window = ensureWindow('chat')
  focusWindow(window)
  if (target?.startsWith('/settings')) {
    dispatchRendererNavigate(window, target)
  }
}

function openMemohSettings(): void {
  openSettingsRoute('/settings')
}

function quitFromTray(): void {
  app.quit()
}

function buildTrayMenu(bots: TrayBot[] = []): Electron.Menu {
  const botItems: MenuItemConstructorOptions[] = bots.length > 0
    ? bots.map((bot) => ({
        label: bot.displayName,
        click: () => openBotWorkspace(bot.id),
      }))
    : [{ label: 'No Bots', enabled: false }]

  return Menu.buildFromTemplate([
    {
      label: `Show ${DESKTOP_PRODUCT_NAME}`,
      click: revealChatWindow,
    },
    { type: 'separator' },
    {
      label: 'Bots',
      enabled: false,
    },
    ...botItems,
    { type: 'separator' },
    {
      label: 'Settings',
      submenu: [
        {
          label: 'All Settings',
          click: () => openSettingsRoute('/settings'),
        },
        { type: 'separator' },
        ...SETTINGS_TRAY_ITEMS.map((item) => ({
          label: item.label,
          click: () => openSettingsRoute(item.target),
        })),
      ],
    },
    { type: 'separator' },
    {
      label: desktopRuntimeTrayLabel(),
      enabled: false,
    },
    { type: 'separator' },
    {
      label: `Quit ${DESKTOP_PRODUCT_NAME}`,
      click: quitFromTray,
    },
  ])
}

function desktopRuntimeTrayLabel(): string {
  const state = remoteRuntimeManager?.runtimeState()
  if (!state?.enabled) return 'Computer access: Off'
  const label = state.status.charAt(0).toUpperCase() + state.status.slice(1)
  return `${state.runtimeName || 'Computer access'}: ${label}`
}

// The main process has no credentials of its own, so it never fetches bots
// from the server. The renderer — which owns the authenticated SDK session —
// pushes its bot list over `desktop:set-tray-bots`; main keeps the last
// pushed list and rebuilds the tray menu from it.
let trayBots: TrayBot[] = []

function normalizeTrayBots(payload: unknown): TrayBot[] {
  if (!Array.isArray(payload)) return []
  return payload.flatMap((item): TrayBot[] => {
    if (!item || typeof item !== 'object') return []
    const record = item as { id?: unknown, displayName?: unknown }
    const id = typeof record.id === 'string' ? record.id.trim() : ''
    if (!id) return []
    const displayName = (typeof record.displayName === 'string' ? record.displayName.trim() : '') || id
    return [{ id, displayName }]
  })
}

function setTrayMenu(): void {
  appTray?.setContextMenu(buildTrayMenu(trayBots))
}

function showTrayMenu(): void {
  if (!appTray) return
  const menu = buildTrayMenu(trayBots)
  appTray.setContextMenu(menu)
  appTray.popUpContextMenu(menu)
}

function createAppTray(): void {
  if (appTray) return

  appTray = new Tray(createTrayIcon())
  appTray.setToolTip(DESKTOP_PRODUCT_NAME)
  setTrayMenu()
  appTray.on('click', () => {
    showTrayMenu()
  })
  appTray.on('right-click', () => {
    showTrayMenu()
  })
}

// `electron-vite` emits the preload bundle as `index.mjs` because the
// package is ESM (`"type": "module"`). Electron silently no-ops if this
// path doesn't exist — keeping the file name in sync with the build
// output is what wires the IPC bridge into the renderer.
const PRELOAD_FILE = '../preload/index.mjs'

function macWindowChromeOptions(tabbingIdentifier: string): Partial<Electron.BrowserWindowConstructorOptions> {
  if (process.platform !== 'darwin') return {}
  return {
    titleBarStyle: 'hidden',
    trafficLightPosition: { x: 14, y: 13 },
    transparent: true,
    backgroundColor: '#00000000',
    tabbingIdentifier,
  }
}

function createChatWindow(): BrowserWindow {
  const window = new BrowserWindow({
    ...rememberedWindowOptions('chat', CHAT_DEFAULTS),
    ...macWindowChromeOptions('memoh-chat'),
    show: false,
    autoHideMenuBar: true,
    title: DESKTOP_PRODUCT_NAME,
    icon: iconPng,
    webPreferences: {
      preload: join(__dirname, PRELOAD_FILE),
      sandbox: false,
      contextIsolation: true,
      nodeIntegration: false,
    },
  })
  attachWindowStatePersistence(window, 'chat', CHAT_DEFAULTS)

  window.once('ready-to-show', () => {
    restoreWindowMaximized(window, 'chat')
    window.show()
  })
  window.on('close', (event) => {
    if (isQuitting) return
    event.preventDefault()
    window.hide()
  })
  window.on('closed', () => {
    chatWindow = null
  })

  applyExternalLinkHandler(window)
  loadRendererEntry(window, 'index')
  return window
}

function ensureWindow(_kind: WindowKind): BrowserWindow {
  if (!chatWindow || chatWindow.isDestroyed()) chatWindow = createChatWindow()
  return chatWindow
}

function focusWindow(window: BrowserWindow): void {
  if (window.isMinimized()) window.restore()
  window.show()
  window.focus()
}

const menuAcceleratorOverrides = new Map<string, string>()

function effectiveMenuAccelerator(command: AppKeyboardCommand): string | undefined {
  return menuAcceleratorOverrides.get(command) ?? acceleratorForCommand(command)
}

function closeWindowMenuItem(): MenuItemConstructorOptions {
  return {
    label: 'Close',
    accelerator: effectiveMenuAccelerator(appKeyboardCommands.closeCurrentWorkspaceTab),
    click: () => {
      dispatchFocusedWindowCommand(
        chatWindow,
        BrowserWindow.getFocusedWindow(),
        appKeyboardCommands.closeCurrentWorkspaceTab,
      )
    },
  }
}

async function rebuildAppMenu(): Promise<void> {
  const template: MenuItemConstructorOptions[] = []
  if (process.platform === 'darwin') {
    template.push({
      label: app.name,
      submenu: [
        { role: 'about' },
        { type: 'separator' },
        {
          label: 'Memoh Settings...',
          click: openMemohSettings,
        },
        { type: 'separator' },
        { role: 'services' },
        { type: 'separator' },
        { role: 'hide' },
        { role: 'hideOthers' },
        { role: 'unhide' },
        { type: 'separator' },
        { role: 'quit' },
      ],
    })
  }
  template.push(
    {
      label: 'Edit',
      submenu: [
        { role: 'undo' },
        { role: 'redo' },
        { type: 'separator' },
        { role: 'cut' },
        { role: 'copy' },
        { role: 'paste' },
        { role: 'selectAll' },
      ],
    },
    {
      label: 'View',
      submenu: [
        { role: 'reload' },
        { role: 'forceReload' },
        { role: 'toggleDevTools' },
        { type: 'separator' },
        { role: 'resetZoom' },
        { role: 'zoomIn' },
        { role: 'zoomOut' },
        { type: 'separator' },
        { role: 'togglefullscreen' },
      ],
    },
    {
      label: 'Window',
      submenu: [
        { role: 'minimize' },
        closeWindowMenuItem(),
      ],
    },
  )

  Menu.setApplicationMenu(Menu.buildFromTemplate(template))
}

app.whenReady().then(async () => {
  if (maybeSelfInstallMacOS()) return

  electronApp.setAppUserModelId(process.env.MEMOH_DESKTOP_APP_ID?.trim() || 'ai.memoh.desktop')

  if (process.platform === 'darwin' && app.dock && is.dev) {
    app.dock.setIcon(iconPng)
  }

  app.on('browser-window-created', (_, window) => {
    optimizer.watchWindowShortcuts(window)
    attachExternalLinkGuards(window.webContents)
  })

  remoteRuntimeManager = new DesktopRemoteRuntimeManager({
    configPath: join(app.getPath('userData'), 'remote-runtime.json'),
    currentServerUrl: getDesktopApiBaseUrl,
    workspaceBase: homedir(),
    deviceName: hostname(),
    encryption: {
      isAvailable: () => safeStorage.isEncryptionAvailable(),
      encrypt: value => safeStorage.encryptString(value),
      decrypt: value => safeStorage.decryptString(value),
    },
    warn: (message, error) => console.warn(message, error),
  })
  remoteRuntimeManager.onStateChanged((state) => {
    setTrayMenu()
    for (const window of BrowserWindow.getAllWindows()) {
      if (!window.isDestroyed()) {
        window.webContents.send('desktop:runtime-state-changed', state)
      }
    }
  })
  await remoteRuntimeManager.restore()

  createAppTray()

  ipcMain.handle('window:close-self', (event) => {
    assertTrustedRenderer(event)
    const sender = BrowserWindow.fromWebContents(event.sender)
    sender?.close()
  })
  ipcMain.handle('desktop:server-status', (event) => {
    assertTrustedRenderer(event)
    return getDesktopServerStatus()
  })
  ipcMain.handle('desktop:api-base-url', (event) => {
    assertTrustedRenderer(event)
    return getDesktopApiBaseUrl()
  })
  ipcMain.handle('desktop:probe-server', (event) => {
    assertTrustedRenderer(event)
    return probeConfiguredServer()
  })
  ipcMain.handle('desktop:connect-server', (event, baseUrl: unknown) => {
    assertTrustedRenderer(event)
    return connectToServer(baseUrl)
  })
  ipcMain.handle('desktop:runtime-state', (event) => {
    assertTrustedRenderer(event)
    return requireRemoteRuntimeManager().runtimeState()
  })
  ipcMain.handle('desktop:configure-runtime', (event, config: unknown) => {
    assertTrustedRenderer(event)
    return requireRemoteRuntimeManager().configure(normalizeDesktopRuntimeConfig(config))
  })
  ipcMain.handle('desktop:set-menu-accelerators', async (event, rawPayload: unknown) => {
    assertTrustedRenderer(event)
    if (!rawPayload || typeof rawPayload !== 'object') return
    const incoming = new Map<string, string>()
    for (const [command, accelerator] of Object.entries(rawPayload as Record<string, unknown>)) {
      if (typeof accelerator !== 'string' || !accelerator) continue
      incoming.set(command, accelerator)
    }
    let changed = incoming.size !== menuAcceleratorOverrides.size
    if (!changed) {
      for (const [command, accelerator] of incoming) {
        if (menuAcceleratorOverrides.get(command) !== accelerator) {
          changed = true
          break
        }
      }
    }
    if (!changed) return
    menuAcceleratorOverrides.clear()
    for (const [command, accelerator] of incoming) {
      menuAcceleratorOverrides.set(command, accelerator)
    }
    await rebuildAppMenu()
  })
  ipcMain.handle('desktop:open-external-url', (event, rawURL: unknown) => {
    assertTrustedRenderer(event)
    return openExternalUrl(rawURL)
  })
  ipcMain.handle('desktop:set-tray-bots', (event, payload: unknown) => {
    assertTrustedRenderer(event)
    trayBots = normalizeTrayBots(payload)
    setTrayMenu()
  })
  ipcMain.handle('desktop:broadcast-invalidate', (event, payload: unknown) => {
    assertTrustedRenderer(event)
    const senderId = event.sender.id
    for (const target of BrowserWindow.getAllWindows()) {
      if (target.isDestroyed()) continue
      if (target.webContents.id === senderId) continue
      target.webContents.send('desktop:invalidate', payload)
    }
  })
  registerDesktopUpdates({
    assertTrustedRenderer,
    markQuitting: () => {
      isQuitting = true
    },
    prepareToInstall: async () => {
      isQuitting = true
      await finishQuitCleanup()
    },
  })

  chatWindow = createChatWindow()

  app.on('activate', () => {
    revealChatWindow()
  })

  await rebuildAppMenu()
})

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit()
})

function requireRemoteRuntimeManager(): DesktopRemoteRuntimeManager {
  if (!remoteRuntimeManager) throw new Error('desktop runtime manager is not ready')
  return remoteRuntimeManager
}

function normalizeDesktopRuntimeConfig(value: unknown): DesktopRuntimeConfig | null {
  if (value === null) return null
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('runtime configuration must contain runtimeId, name, and key')
  }
  const record = value as Record<string, unknown>
  if (typeof record.runtimeId !== 'string' || typeof record.name !== 'string' || typeof record.key !== 'string') {
    throw new Error('runtime configuration must contain runtimeId, name, and key')
  }
  if (record.teamId !== undefined && typeof record.teamId !== 'string') {
    throw new Error('runtime configuration teamId must be a string')
  }
  const name = record.name.trim()
  if (!name) throw new Error('runtime configuration name is required')
  return {
    runtimeId: record.runtimeId,
    name,
    key: record.key,
    teamId: record.teamId,
  }
}
