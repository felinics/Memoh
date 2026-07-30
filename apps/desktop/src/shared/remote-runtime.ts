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

export interface DesktopRuntimeConfig {
  runtimeId: string
  name: string
  key: string
  teamId?: string
}
