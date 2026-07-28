import { app, BrowserWindow, ipcMain, type IpcMainInvokeEvent } from 'electron'
import electronUpdater from 'electron-updater'
import type { ProgressInfo, UpdateInfo } from 'electron-updater'
import {
  createInitialDesktopUpdateState,
  reduceDesktopUpdateState,
  type DesktopUpdateInfo,
  type DesktopUpdateState,
  type DesktopUpdateStateEvent,
} from '../shared/updates'

const UPDATE_FEED_BASE_URL = (process.env.MEMOH_DESKTOP_UPDATE_BASE_URL ?? '').trim()
const { autoUpdater } = electronUpdater

export interface DesktopUpdatesOptions {
  assertTrustedRenderer(event: IpcMainInvokeEvent): void
  prepareToInstall(): Promise<void>
  markQuitting(): void
}

let updateState = createInitialDesktopUpdateState(app.getVersion(), Boolean(UPDATE_FEED_BASE_URL))
let updaterConfigured = false
let updaterListenersRegistered = false
let startupCheckScheduled = false

export function registerDesktopUpdates(options: DesktopUpdatesOptions): void {
  configureAutoUpdater()

  ipcMain.handle('desktop:updates:get-info', (event): DesktopUpdateInfo => {
    options.assertTrustedRenderer(event)
    return {
      version: app.getVersion(),
      platform: process.platform,
      enabled: Boolean(UPDATE_FEED_BASE_URL),
    }
  })
  ipcMain.handle('desktop:updates:get-state', (event) => {
    options.assertTrustedRenderer(event)
    return getUpdateState()
  })
  ipcMain.handle('desktop:updates:check', (event) => {
    options.assertTrustedRenderer(event)
    return checkForUpdate()
  })
  ipcMain.handle('desktop:updates:download', (event) => {
    options.assertTrustedRenderer(event)
    return downloadUpdate()
  })
  ipcMain.handle('desktop:updates:install', async (event) => {
    options.assertTrustedRenderer(event)
    return installUpdate(options)
  })

  scheduleStartupUpdateCheck()
}

function getUpdateState(): DesktopUpdateState {
  return updateState
}

async function checkForUpdate(): Promise<DesktopUpdateState> {
  if (!UPDATE_FEED_BASE_URL) {
    return setUpdateState({
      type: 'unavailable',
      error: 'No update feed URL is configured.',
    })
  }
  if (['checking', 'downloading', 'downloaded'].includes(updateState.status)) return updateState

  configureAutoUpdater()
  if (!autoUpdater.isUpdaterActive()) {
    return setUpdateState({
      type: 'unavailable',
      error: 'Updater is not available for this build.',
    })
  }

  setUpdateState({ type: 'checking' })
  try {
    const result = await autoUpdater.checkForUpdates()
    if (!result) {
      return setUpdateState({
        type: 'unavailable',
        error: 'Updater is not available for this build.',
      })
    }
    if (updateState.status === 'checking') {
      setUpdateState(
        result.isUpdateAvailable
          ? { type: 'available', latestVersion: result.updateInfo.version }
          : { type: 'not-available', latestVersion: result.updateInfo.version },
      )
    }
  } catch (error) {
    setUpdateState({ type: 'error', error })
  }
  return updateState
}

async function downloadUpdate(): Promise<DesktopUpdateState> {
  configureAutoUpdater()
  if (!autoUpdater.isUpdaterActive()) {
    return setUpdateState({
      type: 'unavailable',
      error: 'Updater is not available for this build.',
    })
  }
  if (updateState.status === 'downloaded') return updateState
  if (updateState.status !== 'available') return updateState

  setUpdateState({ type: 'download-progress', percent: 0 })
  try {
    await autoUpdater.downloadUpdate()
  } catch (error) {
    setUpdateState({ type: 'error', error })
  }
  return updateState
}

async function installUpdate(options: DesktopUpdatesOptions): Promise<DesktopUpdateState> {
  if (updateState.status !== 'downloaded') return updateState

  options.markQuitting()
  await options.prepareToInstall()
  setImmediate(() => autoUpdater.quitAndInstall(false, true))
  return updateState
}

function configureAutoUpdater(): void {
  if (!UPDATE_FEED_BASE_URL) return

  if (!updaterConfigured) {
    autoUpdater.autoDownload = false
    autoUpdater.autoInstallOnAppQuit = false
    autoUpdater.forceDevUpdateConfig = !app.isPackaged
    try {
      autoUpdater.setFeedURL({
        provider: 'generic',
        url: ensureTrailingSlash(UPDATE_FEED_BASE_URL),
      })
      updaterConfigured = true
    } catch (error) {
      setUpdateState({ type: 'unavailable', error: formatConfigurationError(error) })
      return
    }
  }

  if (updaterListenersRegistered) return
  autoUpdater.on('checking-for-update', () => setUpdateState({ type: 'checking' }))
  autoUpdater.on('update-available', info => (
    setUpdateState({ type: 'available', latestVersion: updateVersion(info) })
  ))
  autoUpdater.on('update-not-available', info => (
    setUpdateState({ type: 'not-available', latestVersion: updateVersion(info) })
  ))
  autoUpdater.on('download-progress', progress => (
    setUpdateState({ type: 'download-progress', percent: progressPercent(progress) })
  ))
  autoUpdater.on('update-downloaded', info => (
    setUpdateState({ type: 'downloaded', latestVersion: updateVersion(info) })
  ))
  autoUpdater.on('error', error => setUpdateState({ type: 'error', error }))
  updaterListenersRegistered = true
}

function scheduleStartupUpdateCheck(): void {
  if (!UPDATE_FEED_BASE_URL || startupCheckScheduled) return
  startupCheckScheduled = true
  setTimeout(() => {
    void checkForUpdate()
  }, 3_000)
}

function setUpdateState(event: DesktopUpdateStateEvent): DesktopUpdateState {
  updateState = reduceDesktopUpdateState(updateState, event)
  for (const window of BrowserWindow.getAllWindows()) {
    if (!window.isDestroyed()) {
      window.webContents.send('desktop:updates:state-changed', updateState)
    }
  }
  return updateState
}

function updateVersion(info: UpdateInfo): string {
  return info.version
}

function progressPercent(progress: ProgressInfo): number {
  return progress.percent
}

function ensureTrailingSlash(value: string): string {
  return value.endsWith('/') ? value : `${value}/`
}

function formatConfigurationError(error: unknown): string {
  const message = error instanceof Error ? error.message : String(error)
  return `Failed to configure updater: ${message}`
}
