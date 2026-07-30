import type { InjectionKey } from 'vue'

// Provided by the Electron desktop shell to enable a macOS-style top inset
// (traffic-light reserve + custom TopBar) inside reusable web sidebars.
// Web (browser) does not provide this key, so consumers fall back to false.
export const DesktopShellKey: InjectionKey<boolean> = Symbol('memohai:desktop-shell')

export type DesktopRuntimeStatus =
  | 'disabled'
  | 'connecting'
  | 'connected'
  | 'disconnected'
  | 'stopped'
  | 'error'

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

export const DesktopRuntimeKey: InjectionKey<DesktopRuntimeBridge | undefined> = Symbol('memohai:desktop-runtime')

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

export const DesktopUpdatesKey: InjectionKey<DesktopUpdateBridge | undefined> = Symbol('memohai:desktop-updates')
