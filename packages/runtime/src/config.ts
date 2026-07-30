import { isAbsolute } from 'node:path'

import { validateRuntimeKey } from './key'

const runtimeTeamIdPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

export interface RuntimeClientConfig {
  serverUrl: string
  key: string
  // Optional for direct connections to the upstream single-team server.
  teamId?: string
  workspaceBase: string
  insecureLocalhost?: boolean
}

export function normalizeRuntimeTeamId(teamId: string): string {
  const normalized = teamId.trim().toLowerCase()
  if (!runtimeTeamIdPattern.test(normalized)) {
    throw new Error('runtime team ID must be a UUID')
  }
  return normalized
}

export function validateConfig(config: RuntimeClientConfig): void {
  if (!config.serverUrl?.trim()) {
    throw new Error('serverUrl is required')
  }
  if (!config.workspaceBase?.trim() || !isAbsolute(config.workspaceBase)) {
    throw new Error('workspaceBase must be an absolute path')
  }
  let serverUrl: URL
  try {
    serverUrl = new URL(config.serverUrl)
  } catch {
    throw new Error('serverUrl must be a valid absolute URL')
  }
  if (!['http:', 'https:', 'ws:', 'wss:'].includes(serverUrl.protocol)) {
    throw new Error('serverUrl must use http, https, ws, or wss')
  }
  if (serverUrl.username || serverUrl.password) {
    throw new Error('serverUrl must not contain credentials')
  }
  if (serverUrl.search || serverUrl.hash) {
    throw new Error('serverUrl must not contain a query string or fragment')
  }
  if (config.insecureLocalhost !== undefined && typeof config.insecureLocalhost !== 'boolean') {
    throw new Error('insecureLocalhost must be a boolean')
  }
  validateRuntimeKey(config.key)
  if (config.teamId !== undefined) {
    normalizeRuntimeTeamId(config.teamId)
  }
}
