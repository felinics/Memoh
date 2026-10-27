import { readFileSync, writeFileSync, renameSync } from 'node:fs'
import { join } from 'node:path'
import { GenericProvider } from 'electron-updater/out/providers/GenericProvider.js'
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
// The app is tray-resident and can run for weeks without a quit. Restart always
// installs the cached download without a last-moment check, so a release newer
// than the last check is only picked up on the following launch. A check fetches
// only the small feed manifest, so a short interval keeps that window small cheaply.
// Failures retry separately so one outage does not disable updates.
const PERIODIC_CHECK_INTERVAL_MS = 60 * 60 * 1000
const RETRY_DELAYS_MS = [60_000, 5 * 60_000, 15 * 60_000, 60 * 60_000]
const DOWNLOAD_STALL_TIMEOUT_MS = 2 * 60_000
const { autoUpdater } = electronUpdater
type UpdateCheckResult = NonNullable<Awaited<ReturnType<typeof autoUpdater.checkForUpdates>>>
type DownloadCancellationToken = NonNullable<UpdateCheckResult['cancellationToken']>

export interface DesktopUpdatesOptions {
  assertTrustedRenderer(event: IpcMainInvokeEvent): void
  prepareToInstall(): Promise<void>
  markQuitting(): void
  installFailed(): void
  onStateChanged(state: DesktopUpdateState): void
}

interface SavedUpdates {
  autoUpdate: boolean
  pending: UpdateInfo | null
  attemptedVersion: string | null
}
let saved: SavedUpdates = { autoUpdate: true, pending: null, attemptedVersion: null }
let updateOptions: DesktopUpdatesOptions
let updateState = createInitialDesktopUpdateState(app.getVersion(), Boolean(UPDATE_FEED_BASE_URL))
let updaterConfigured = false
let updaterListenersRegistered = false
let checkTimer: ReturnType<typeof setTimeout> | null = null
let downloadStallTimer: ReturnType<typeof setTimeout> | null = null
let downloadCancellationToken: DownloadCancellationToken | null = null
let downloadCompletionSequence = 0
let installRecoveryRequired = false
let consecutiveFailures = 0
let handledFailure: unknown = null

export function registerDesktopUpdates(options: DesktopUpdatesOptions): void {
  updateOptions = options
  try {
    const value = JSON.parse(readFileSync(settingsPath(), 'utf8')) as SavedUpdates
    if (typeof value.autoUpdate !== 'boolean') throw new Error('Invalid update settings')
    saved = { autoUpdate: value.autoUpdate, pending: value.pending?.version ? value.pending : null, attemptedVersion: value.attemptedVersion ?? null }
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT') setUpdateState({ type: 'error', error })
  }
  setUpdateState({ type: 'preference', autoUpdate: saved.autoUpdate })
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
  ipcMain.handle('desktop:updates:set-auto-update', (event, enabled: unknown) => {
    options.assertTrustedRenderer(event)
    if (typeof enabled !== 'boolean') throw new TypeError('autoUpdate must be boolean')
    if (updateState.status === 'installing') return updateState
    persist({ ...saved, autoUpdate: enabled })
    setUpdateState({ type: 'preference', autoUpdate: enabled })
    if (enabled) void checkForUpdate()
    else cancelScheduledCheck()
    return updateState
  })
  ipcMain.handle('desktop:updates:install', async (event) => {
    options.assertTrustedRenderer(event)
    return installDesktopUpdate()
  })

  scheduleCheck(3_000)
}

function getUpdateState(): DesktopUpdateState {
  return updateState
}

async function checkForUpdate(automatic = false): Promise<DesktopUpdateState> {
  if (automatic && installRecoveryRequired) return updateState
  if (!UPDATE_FEED_BASE_URL) {
    return setUpdateState({
      type: 'unavailable',
      error: 'No update feed URL is configured.',
    })
  }
  if (['checking', 'downloading', 'installing'].includes(updateState.status)) return updateState

  installRecoveryRequired = false
  cancelScheduledCheck()
  configureAutoUpdater()
  if (!autoUpdater.isUpdaterActive()) {
    return setUpdateState({
      type: 'unavailable',
      error: 'Updater is not available for this build.',
    })
  }

  handledFailure = null
  setUpdateState({ type: 'checking' })
  try {
    const completionAtStart = downloadCompletionSequence
    const result = await autoUpdater.checkForUpdates()
    if (!result) {
      return setUpdateState({
        type: 'unavailable',
        error: 'Updater is not available for this build.',
      })
    }
    if (result.downloadPromise) {
      const token = result.cancellationToken
      // A cached download may finish before checkForUpdates() resumes. Do not
      // arm a stall timer for work that has already emitted `update-downloaded`.
      if (token && downloadCompletionSequence === completionAtStart) armDownloadWatchdog(token)
      void result.downloadPromise.then(
        () => clearDownloadWatchdog(token),
        failDownload,
      )
    }
    if (updateState.status === 'checking') {
      setUpdateState(
        result.isUpdateAvailable
          ? { type: 'available', latestVersion: result.updateInfo.version, releaseNotes: updateNotes(result.updateInfo) }
          : { type: 'not-available', latestVersion: result.updateInfo.version },
      )
    }
    if (!result.isUpdateAvailable) markUpdateSucceeded()
  } catch (error) {
    failUpdate(error)
  }
  return updateState
}

function markUpdateSucceeded(): void {
  consecutiveFailures = 0
  handledFailure = null
  scheduleCheck(PERIODIC_CHECK_INTERVAL_MS)
}

function failUpdate(error: unknown): void {
  clearDownloadWatchdog()
  if (handledFailure === error || (updateState.status === 'error' && checkTimer)) return
  handledFailure = error
  // A feed error must not hide an already usable download. New-version download
  // failures are no longer in `downloaded`, so they remain visible and actionable.
  if (updateState.status !== 'downloaded') setUpdateState({ type: 'error', error })
  const delay = RETRY_DELAYS_MS[Math.min(consecutiveFailures, RETRY_DELAYS_MS.length - 1)]
  consecutiveFailures += 1
  scheduleCheck(delay)
}

function failDownload(error: unknown): void {
  clearDownloadWatchdog()
  // electron-updater clears its pending cache when a replacement download fails.
  // Do not carry a stale manifest into the next launch's offline recovery.
  if (saved.pending) {
    try {
      persist({ ...saved, pending: null })
    } catch (persistError) {
      failUpdate(persistError)
      return
    }
  }
  failUpdate(error)
}

function armDownloadWatchdog(token: DownloadCancellationToken): void {
  clearDownloadWatchdog()
  downloadCancellationToken = token
  downloadStallTimer = setTimeout(() => {
    const error = new Error('Update download stopped making progress. Retry will follow automatically.')
    token.cancel()
    failDownload(error)
  }, DOWNLOAD_STALL_TIMEOUT_MS)
}

function clearDownloadWatchdog(token?: DownloadCancellationToken): void {
  if (token && downloadCancellationToken !== token) return
  if (downloadStallTimer) clearTimeout(downloadStallTimer)
  downloadStallTimer = null
  downloadCancellationToken = null
}

function scheduleCheck(delay: number): void {
  cancelScheduledCheck()
  if (!UPDATE_FEED_BASE_URL || !saved.autoUpdate || updateState.status === 'installing' || installRecoveryRequired) return
  checkTimer = setTimeout(() => {
    checkTimer = null
    void checkForUpdate(true)
  }, delay)
}

function cancelScheduledCheck(): void {
  if (checkTimer) clearTimeout(checkTimer)
  checkTimer = null
}

function settingsPath(): string {
  return join(app.getPath('userData'), 'desktop-updates.json')
}

function persist(next: SavedUpdates): void {
  const path = settingsPath()
  writeFileSync(`${path}.tmp`, JSON.stringify(next), { mode: 0o600 })
  renameSync(`${path}.tmp`, path)
  saved = next
}

function failInstall(error: unknown): void {
  installRecoveryRequired = true
  cancelScheduledCheck()
  clearDownloadWatchdog()
  updateOptions.installFailed()
  setUpdateState({ type: 'error', error })
}

export async function installDesktopUpdate(): Promise<DesktopUpdateState> {
  return installUpdate(updateOptions)
}

async function installUpdate(options: DesktopUpdatesOptions, restart = true): Promise<DesktopUpdateState> {
  if (updateState.status !== 'downloaded') return updateState
  cancelScheduledCheck()
  setUpdateState({ type: 'installing' }) // Lock before the first await, including IPC double-clicks.
  try {
    persist({ ...saved, attemptedVersion: updateState.latestVersion })
    await options.prepareToInstall()
    options.markQuitting()
    autoUpdater.autoRunAppAfterInstall = restart
    // MacUpdater may still be preparing the ZIP in Squirrel. Its eventual quit
    // callback cannot be cancelled through the public API, so elapsed time alone
    // must not restore the session while that callback remains armed. Stay locked
    // until installation exits the process or the updater reports a real error.
    autoUpdater.quitAndInstall(!restart, restart)
  } catch (error) {
    failInstall(error)
  }
  return updateState
}

// Own the quit boundary instead of native auto-install: on macOS handing the
// package to Squirrel early cannot reliably be undone when the toggle changes.
export async function installDesktopUpdateOnQuit(): Promise<boolean> {
  if (!saved.autoUpdate || updateState.status !== 'downloaded') return false
  await installUpdate(updateOptions, false)
  return getUpdateState().status === 'installing'
}

/** Recover after shutdown/crash without depending on an online feed. The standard
 * updater resolves and SHA-validates its own cache using the saved manifest; no
 * cached executable path is trusted or launched by this application. A bounded
 * startup wait keeps the old version usable on recovery failure. An attempted
 * version still running old code becomes an error, never another restart loop.
 */
export async function recoverDesktopUpdate(): Promise<boolean> {
  if (!saved.pending || !UPDATE_FEED_BASE_URL) return false
  try {
    if (autoUpdater.currentVersion.compare(saved.pending.version) >= 0) {
      persist({ ...saved, pending: null, attemptedVersion: null })
      return false
    }
    if (saved.attemptedVersion === saved.pending.version) {
      installRecoveryRequired = true
      cancelScheduledCheck()
      setUpdateState({ type: 'error', error: 'The previous update was not applied. Retry from About.' })
      return false
    }
    if (!saved.autoUpdate) return false
    const manifest = saved.pending
    class CachedManifestProvider extends GenericProvider {
      constructor(_options: unknown, updater: ConstructorParameters<typeof GenericProvider>[1], runtime: ConstructorParameters<typeof GenericProvider>[2]) {
        super({ provider: 'generic', url: ensureTrailingSlash(UPDATE_FEED_BASE_URL) }, updater, runtime)
      }
      override async getLatestVersion(): Promise<UpdateInfo> { return manifest }
    }
    autoUpdater.setFeedURL({ provider: 'custom', updateProvider: CachedManifestProvider, url: ensureTrailingSlash(UPDATE_FEED_BASE_URL) })
    let expired = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const recovery = (async () => {
      const result = await autoUpdater.checkForUpdates()
      await result?.downloadPromise
      if (!expired && updateState.status === 'downloaded') await installUpdate(updateOptions)
    })()
    try {
      await Promise.race([
        recovery,
        new Promise<void>(resolve => { timer = setTimeout(() => { expired = true; resolve() }, 8_000) }),
      ])
    } finally {
      if (timer) clearTimeout(timer)
      autoUpdater.setFeedURL({ provider: 'generic', url: ensureTrailingSlash(UPDATE_FEED_BASE_URL) })
    }
    // After timeout, recovery may finish downloading but must not restart an
    // already visible session. Its errors are still consumed and exposed.
    void recovery.catch(failUpdate)
    return getUpdateState().status === 'installing'
  } catch (error) {
    failUpdate(error)
    return false
  }
}

function configureAutoUpdater(): void {
  if (!UPDATE_FEED_BASE_URL) return

  if (!updaterConfigured) {
    // Silent-first flow: download in the background and apply on the next
    // natural quit. The footer chip / About row remain the explicit path
    // (restart-now), not the only way an update ever lands.
    autoUpdater.autoDownload = true
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
    setUpdateState({ type: 'available', latestVersion: updateVersion(info), releaseNotes: updateNotes(info) })
  ))
  autoUpdater.on('update-not-available', info => (
    setUpdateState({ type: 'not-available', latestVersion: updateVersion(info) })
  ))
  autoUpdater.on('download-progress', progress => {
    if (downloadCancellationToken) armDownloadWatchdog(downloadCancellationToken)
    setUpdateState({ type: 'download-progress', percent: progressPercent(progress) })
  })
  autoUpdater.on('update-downloaded', info => {
    downloadCompletionSequence += 1
    clearDownloadWatchdog()
    try {
      persist({ ...saved, pending: info, attemptedVersion: null })
      setUpdateState({ type: 'downloaded', latestVersion: updateVersion(info), releaseNotes: updateNotes(info) })
      markUpdateSucceeded()
    } catch (error) {
      failDownload(error)
    }
  })
  autoUpdater.on('error', error => {
    if (updateState.status === 'installing') failInstall(error)
    else failUpdate(error)
  })
  updaterListenersRegistered = true
}

function setUpdateState(event: DesktopUpdateStateEvent): DesktopUpdateState {
  updateState = reduceDesktopUpdateState(updateState, event)
  for (const window of BrowserWindow.getAllWindows()) {
    if (!window.isDestroyed()) {
      window.webContents.send('desktop:updates:state-changed', updateState)
    }
  }
  updateOptions.onStateChanged(updateState)
  return updateState
}

function updateVersion(info: UpdateInfo): string {
  return info.version
}

// electron-updater shapes releaseNotes as string | Array<{ note }> | null
// depending on the feed; collapse to a single markdown string (or null so the
// renderer can hide the notes affordance entirely).
function updateNotes(info: UpdateInfo): string | null {
  const notes = info.releaseNotes
  if (typeof notes === 'string') return notes.trim() || null
  if (Array.isArray(notes)) {
    const joined = notes
      .map(entry => (typeof entry?.note === 'string' ? entry.note.trim() : ''))
      .filter(Boolean)
      .join('\n\n')
    return joined || null
  }
  return null
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
