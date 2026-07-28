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
  platform: NodeJS.Platform
  enabled: boolean
}

export interface DesktopUpdateState {
  status: DesktopUpdateStatus
  currentVersion: string
  latestVersion: string | null
  progress: number | null
  error: string | null
}

export type DesktopUpdateStateEvent =
  | { type: 'checking' }
  | { type: 'not-available', latestVersion?: string | null }
  | { type: 'available', latestVersion: string }
  | { type: 'download-progress', percent: number }
  | { type: 'downloaded', latestVersion?: string | null }
  | { type: 'error', error: unknown }
  | { type: 'unavailable', error: string }

export function createInitialDesktopUpdateState(
  currentVersion: string,
  enabled = true,
): DesktopUpdateState {
  return {
    status: enabled ? 'idle' : 'unavailable',
    currentVersion,
    latestVersion: null,
    progress: null,
    error: enabled ? null : 'No update feed URL is configured.',
  }
}

export function reduceDesktopUpdateState(
  state: DesktopUpdateState,
  event: DesktopUpdateStateEvent,
): DesktopUpdateState {
  switch (event.type) {
    case 'checking':
      return {
        ...state,
        status: 'checking',
        progress: null,
        error: null,
      }
    case 'not-available':
      return {
        ...state,
        status: 'up-to-date',
        latestVersion: event.latestVersion ?? state.currentVersion,
        progress: null,
        error: null,
      }
    case 'available':
      return {
        ...state,
        status: 'available',
        latestVersion: event.latestVersion,
        progress: null,
        error: null,
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
