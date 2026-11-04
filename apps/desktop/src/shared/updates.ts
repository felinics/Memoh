/**
 * Automatic updates default on: checking → downloading → downloaded → installing.
 * `available` is only an upstream event, never a user decision or public status.
 * Downloads report null progress until bytes are known. Restart is optional:
 * main applies prepared updates on a real quit, or recovers them next launch.
 * Hiding a window does not install. Turning automation off prevents automatic
 * checks and installation; About can still explicitly check-and-update/restart.
 * Failures remain actionable in About; install attempts must not loop at startup.
 */
export type DesktopUpdateStatus =
  | 'idle'
  | 'checking'
  | 'up-to-date'
  | 'installing'
  | 'downloading'
  | 'downloaded'
  | 'error'
  | 'unavailable'

export interface DesktopUpdateInfo {
  version: string
  platform: NodeJS.Platform
  enabled: boolean
}

export interface DesktopUpdateState {
  status: DesktopUpdateStatus
  autoUpdate: boolean
  currentVersion: string
  latestVersion: string | null
  progress: number | null
  error: string | null
  // Release notes carried by the update feed (electron-builder `releaseInfo`).
  // null both when no update is pending and when the feed ships no notes —
  // the renderer hides the notes affordance on null.
  releaseNotes: string | null
}

export type DesktopUpdateStateEvent =
  | { type: 'checking' }
  | { type: 'installing' }
  | { type: 'preference', autoUpdate: boolean }
  | { type: 'not-available', latestVersion?: string | null }
  | { type: 'available', latestVersion: string, releaseNotes?: string | null }
  | { type: 'download-progress', percent: number }
  | { type: 'downloaded', latestVersion?: string | null, releaseNotes?: string | null }
  | { type: 'error', error: unknown }
  | { type: 'unavailable', error: string }

export function createInitialDesktopUpdateState(
  currentVersion: string,
  enabled = true,
): DesktopUpdateState {
  return {
    status: enabled ? 'idle' : 'unavailable',
    autoUpdate: true,
    currentVersion,
    latestVersion: null,
    progress: null,
    error: enabled ? null : 'No update feed URL is configured.',
    releaseNotes: null,
  }
}

export function reduceDesktopUpdateState(
  state: DesktopUpdateState,
  event: DesktopUpdateStateEvent,
): DesktopUpdateState {
  // Once a restart is under way, only its own outcome may change the state;
  // events from a background check that was already running are stale.
  if (state.status === 'installing' && event.type !== 'error' && event.type !== 'preference') return state
  switch (event.type) {
    case 'preference':
      return { ...state, autoUpdate: event.autoUpdate }
    case 'installing':
      return { ...state, status: 'installing', error: null }
    case 'checking':
      if (state.status === 'downloaded') return state
      return {
        ...state,
        status: 'checking',
        progress: null,
        error: null,
      }
    case 'not-available':
      if (state.status === 'downloaded') return state
      return {
        ...state,
        status: 'up-to-date',
        latestVersion: event.latestVersion ?? state.currentVersion,
        progress: null,
        error: null,
        releaseNotes: null,
      }
    case 'available':
      if (state.status === 'downloaded' && state.latestVersion === event.latestVersion) return state
      return {
        ...state,
        status: 'downloading',
        latestVersion: event.latestVersion,
        progress: null,
        error: null,
        releaseNotes: event.releaseNotes ?? null,
      }
    case 'download-progress':
      return {
        ...state,
        status: 'downloading',
        progress: clampProgress(event.percent),
        error: null,
      }
    case 'downloaded':
      return {
        ...state,
        status: 'downloaded',
        latestVersion: event.latestVersion ?? state.latestVersion,
        progress: 100,
        error: null,
        releaseNotes: event.releaseNotes ?? state.releaseNotes,
      }
    case 'error':
      return {
        ...state,
        status: 'error',
        progress: null,
        error: normalizeErrorMessage(event.error),
      }
    case 'unavailable':
      return {
        ...state,
        status: 'unavailable',
        latestVersion: null,
        progress: null,
        error: event.error,
        releaseNotes: null,
      }
  }
}

function clampProgress(value: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.max(0, Math.min(100, Math.round(value)))
}

function normalizeErrorMessage(error: unknown): string {
  if (error instanceof Error && error.message) return error.message
  if (typeof error === 'string' && error.trim()) return error.trim()
  return 'Update failed'
}
