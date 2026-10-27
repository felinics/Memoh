import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { EventEmitter } from 'node:events'
import { createRequire } from 'node:module'
import type { DesktopUpdateState } from '../shared/updates'

const { MacUpdater } = createRequire(import.meta.url)('electron-updater/out/MacUpdater.js') as typeof import('electron-updater')

const f = vi.hoisted(() => ({
  handlers: new Map<string, (...args: unknown[]) => unknown>(),
  events: new Map<string, (...args: unknown[]) => void>(),
  persisted: '',
  options: {
    assertTrustedRenderer: vi.fn(),
    prepareToInstall: vi.fn(),
    markQuitting: vi.fn(),
    installFailed: vi.fn(),
    onStateChanged: vi.fn(),
  },
  updater: {
    on: vi.fn(), setFeedURL: vi.fn(), isUpdaterActive: () => true,
    checkForUpdates: vi.fn(), quitAndInstall: vi.fn(),
    currentVersion: { compare: vi.fn() },
    autoDownload: false, autoInstallOnAppQuit: true, autoRunAppAfterInstall: true,
  },
}))
vi.mock('electron', () => ({
  app: { getVersion: () => '1.0.0', getPath: () => '/test' },
  BrowserWindow: { getAllWindows: () => [] },
  ipcMain: { handle: (key: string, handler: (...args: unknown[]) => unknown) => f.handlers.set(key, handler) },
}))
vi.mock('node:fs', () => ({
  readFileSync: () => f.persisted || '{"autoUpdate":true,"pending":null,"attemptedVersion":null}',
  writeFileSync: (_path: string, value: string) => { f.persisted = value },
  renameSync: vi.fn(),
}))
vi.mock('electron-updater', () => ({ default: { autoUpdater: f.updater } }))
vi.mock('electron-updater/out/providers/GenericProvider.js', () => ({ GenericProvider: class {} }))

beforeEach(() => {
  vi.resetModules()
  vi.resetAllMocks()
  vi.useFakeTimers()
  vi.stubEnv('MEMOH_DESKTOP_UPDATE_BASE_URL', 'https://updates.example.test/')
  f.persisted = ''
  f.handlers.clear()
  f.events.clear()
  f.updater.on.mockImplementation((name, fn) => { f.events.set(name, fn) })
  f.updater.currentVersion.compare.mockReturnValue(-1)
  f.options.prepareToInstall.mockResolvedValue(undefined)
  f.updater.checkForUpdates.mockResolvedValue(null)
})
afterEach(() => {
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.unstubAllEnvs()
})
async function start() {
  const api = await import('./updates')
  api.registerDesktopUpdates(f.options)
  return api
}
function state() { return f.handlers.get('desktop:updates:get-state')!({}) as DesktopUpdateState }

function finishAvailableUpdate(version: string) {
  f.events.get('checking-for-update')!()
  f.events.get('update-available')!({ version })
  const downloadPromise = Promise.resolve().then(() => {
    f.events.get('update-downloaded')!({ version })
    return []
  })
  return {
    isUpdateAvailable: true,
    updateInfo: { version },
    downloadPromise,
    cancellationToken: { cancel: vi.fn() },
  }
}

it('auto-downloads without exposing a manual download IPC', async () => {
  await start()
  expect(f.updater.autoDownload).toBe(true)
  expect(f.updater.autoInstallOnAppQuit).toBe(false)
  expect(f.handlers.has('desktop:updates:download')).toBe(false)
  f.events.get('update-available')!({ version: '2.0.0' })
  expect(state().status).toBe('downloading')
})
it('persists opt-out and skips automatic checks and quit installation', async () => {
  const api = await start()
  f.handlers.get('desktop:updates:set-auto-update')!({}, false)
  f.events.get('update-downloaded')!({ version: '2.0.0' })
  await vi.advanceTimersByTimeAsync(60 * 60 * 1000)
  expect(f.updater.checkForUpdates).not.toHaveBeenCalled()
  expect(await api.installDesktopUpdateOnQuit()).toBe(false)
  expect(JSON.parse(f.persisted).autoUpdate).toBe(false)
})
it('locks duplicate restart requests before cleanup and recovers cleanup failures', async () => {
  await start()
  f.events.get('update-downloaded')!({ version: '2.0.0' })
  let reject!: (reason: Error) => void
  f.options.prepareToInstall.mockImplementation(() => new Promise((_resolve, fail) => { reject = fail }))
  const install = f.handlers.get('desktop:updates:install')!
  const first = install({})
  await install({})
  expect(f.options.prepareToInstall).toHaveBeenCalledOnce()
  expect(state().status).toBe('installing')
  reject(new Error('cleanup failed'))
  await first
  expect(state().status).toBe('error')
  expect(f.options.installFailed).toHaveBeenCalledOnce()
})
it('consumes asynchronous automatic-download failures', async () => {
  await start()
  f.updater.checkForUpdates.mockImplementation(async () => ({
    isUpdateAvailable: true, updateInfo: { version: '2.0.0' },
    downloadPromise: Promise.reject(new Error('offline')),
  }))
  await f.handlers.get('desktop:updates:check')!({})
  expect(state().status).toBe('error')
})
it('reports an unapplied previous installation instead of restarting again', async () => {
  f.persisted = JSON.stringify({ autoUpdate: true, pending: { version: '2.0.0' }, attemptedVersion: '2.0.0' })
  const api = await start()
  expect(await api.recoverDesktopUpdate()).toBe(false)
  expect(state().status).toBe('error')
  expect(f.updater.quitAndInstall).not.toHaveBeenCalled()
})
it('clears pending metadata only after observing the installed version', async () => {
  f.persisted = JSON.stringify({ autoUpdate: true, pending: { version: '2.0.0' }, attemptedVersion: '2.0.0' })
  f.updater.currentVersion.compare.mockReturnValue(0)
  const api = await start()
  expect(await api.recoverDesktopUpdate()).toBe(false)
  expect(JSON.parse(f.persisted).pending).toBeNull()
})
it('applies a ready update on real quit without requesting relaunch', async () => {
  const api = await start()
  f.events.get('update-downloaded')!({ version: '2.0.0' })
  expect(await api.installDesktopUpdateOnQuit()).toBe(true)
  expect(f.updater.quitAndInstall).toHaveBeenCalledWith(true, false)
})
it.each([true, false])('keeps a slow native installation locked until completion (restart=%s)', async (restart) => {
  const native = Object.assign(new EventEmitter(), { checkForUpdates: vi.fn(), quitAndInstall: vi.fn() })
  const quit = vi.fn()
  // Exercise the installed dependency's callbacks without constructing Electron
  // or starting its local ZIP server. Only the native boundary is simulated.
  const mac = Object.create(MacUpdater.prototype) as InstanceType<typeof MacUpdater>
  Object.assign(mac, { nativeUpdater: native, app: { quit }, autoInstallOnAppQuit: false })
  f.updater.quitAndInstall.mockImplementation(() => {
    mac.autoRunAppAfterInstall = f.updater.autoRunAppAfterInstall
    mac.quitAndInstall()
  })
  const api = await start()
  f.events.get('update-downloaded')!({ version: '2.0.0' })
  if (restart) await f.handlers.get('desktop:updates:install')!({})
  else expect(await api.installDesktopUpdateOnQuit()).toBe(true)

  await vi.advanceTimersByTimeAsync(31_000)
  expect(state().status).toBe('installing')
  expect(f.options.installFailed).not.toHaveBeenCalled()
  await f.handlers.get('desktop:updates:install')!({})
  expect(f.updater.quitAndInstall).toHaveBeenCalledOnce()
  expect(native.checkForUpdates).toHaveBeenCalledOnce()
  expect(native.quitAndInstall).not.toHaveBeenCalled()
  expect(quit).not.toHaveBeenCalled()

  native.emit('update-downloaded')
  expect(native.quitAndInstall).toHaveBeenCalledTimes(restart ? 1 : 0)
  expect(quit).toHaveBeenCalledTimes(restart ? 0 : 1)
})
it.each(['throw', 'event'])('restores the session on an actual installer failure (%s)', async (failure) => {
  await start()
  f.events.get('update-downloaded')!({ version: '2.0.0' })
  const error = new Error('native installation failed')
  if (failure === 'throw') f.updater.quitAndInstall.mockImplementation(() => { throw error })
  await f.handlers.get('desktop:updates:install')!({})
  if (failure === 'event') f.events.get('error')!(error)
  expect(state().status).toBe('error')
  expect(f.options.installFailed).toHaveBeenCalledOnce()
})
it('recovers a prepared update before requesting startup restart', async () => {
  f.persisted = JSON.stringify({ autoUpdate: true, pending: { version: '2.0.0' }, attemptedVersion: null })
  const api = await start()
  f.updater.checkForUpdates.mockImplementation(async () => {
    f.events.get('update-downloaded')!({ version: '2.0.0' })
    return { downloadPromise: Promise.resolve() }
  })
  expect(await api.recoverDesktopUpdate()).toBe(true)
  expect(f.updater.quitAndInstall).toHaveBeenCalledWith(false, true)
  expect(JSON.parse(f.persisted).attemptedVersion).toBe('2.0.0')
})
it('does not restart a visible session when startup recovery finishes late', async () => {
  f.persisted = JSON.stringify({ autoUpdate: true, pending: { version: '2.0.0' }, attemptedVersion: null })
  const api = await start()
  let finish!: () => void
  f.updater.checkForUpdates.mockImplementation(async () => {
    f.events.get('update-available')!({ version: '2.0.0' })
    return { downloadPromise: new Promise<void>(resolve => { finish = resolve }) }
  })
  const recovery = api.recoverDesktopUpdate()
  await vi.advanceTimersByTimeAsync(8_000)
  expect(await recovery).toBe(false)
  f.events.get('update-downloaded')!({ version: '2.0.0' })
  finish()
  await Promise.resolve()
  await Promise.resolve()
  expect(state().status).toBe('downloaded')
  expect(f.updater.quitAndInstall).not.toHaveBeenCalled()
})
it('honors a saved opt-out during startup recovery', async () => {
  f.persisted = JSON.stringify({ autoUpdate: false, pending: { version: '2.0.0' }, attemptedVersion: null })
  const api = await start()
  expect(await api.recoverDesktopUpdate()).toBe(false)
  expect(f.updater.checkForUpdates).not.toHaveBeenCalled()
})

it('retries automatically after a transient check failure', async () => {
  await start()
  f.updater.checkForUpdates.mockRejectedValue(new Error('network offline'))
  await vi.advanceTimersByTimeAsync(3_000)
  expect(f.updater.checkForUpdates).toHaveBeenCalledTimes(1)
  expect(state().status).toBe('error')

  await vi.advanceTimersByTimeAsync(60_000)
  expect(f.updater.checkForUpdates).toHaveBeenCalledTimes(2)
})

it('rechecks while ready and keeps the same downloaded version visible', async () => {
  await start()
  f.updater.checkForUpdates.mockImplementation(async () => finishAvailableUpdate('1.1.0'))
  await vi.advanceTimersByTimeAsync(3_000)
  expect(state().status).toBe('downloaded')

  await vi.advanceTimersByTimeAsync(60 * 60 * 1000)
  expect(f.updater.checkForUpdates).toHaveBeenCalledTimes(2)
  expect(state()).toMatchObject({ status: 'downloaded', latestVersion: '1.1.0', progress: 100 })

  f.updater.checkForUpdates.mockImplementation(async () => finishAvailableUpdate('1.2.0'))
  await vi.advanceTimersByTimeAsync(60 * 60 * 1000)
  expect(f.updater.checkForUpdates).toHaveBeenCalledTimes(3)
  expect(state()).toMatchObject({ status: 'downloaded', latestVersion: '1.2.0' })
})

it('cancels a download that stops making progress', async () => {
  await start()
  const cancel = vi.fn()
  f.updater.checkForUpdates.mockImplementation(async () => {
    f.events.get('checking-for-update')!()
    f.events.get('update-available')!({ version: '1.1.0' })
    let rejectDownload!: (error: Error) => void
    const downloadPromise = new Promise<string[]>((_resolve, reject) => { rejectDownload = reject })
    cancel.mockImplementation(() => rejectDownload(new Error('download cancelled')))
    return {
      isUpdateAvailable: true,
      updateInfo: { version: '1.1.0' },
      downloadPromise,
      cancellationToken: { cancel },
    }
  })
  await vi.advanceTimersByTimeAsync(3_000)
  expect(state().status).toBe('downloading')

  await vi.advanceTimersByTimeAsync(2 * 60 * 1000)
  expect(cancel).toHaveBeenCalledOnce()
  expect(state().status).toBe('error')
})
