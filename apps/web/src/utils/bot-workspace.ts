import type { BotWorkspaceBackend } from './bot-detail-tabs'

export function resolveBotWorkspaceBackend(workspaceBackend?: string | null): BotWorkspaceBackend {
  const live = (workspaceBackend ?? '').trim().toLowerCase()
  if (live === 'remote') return 'remote'
  return 'container'
}

export type RuntimeStatusKey = 'running' | 'paused' | 'created' | 'stopped' | 'exited' | 'unknown'

const runtimeStatusKeys: readonly RuntimeStatusKey[] = ['running', 'paused', 'created', 'stopped', 'exited']

// Shared by the overview and container pages so a status the backend adds
// (e.g. paused) is labeled the same everywhere instead of falling back to
// "unknown" on whichever page was not updated.
export function resolveRuntimeStatusKey(status?: string | null): RuntimeStatusKey {
  const normalized = (status ?? '').trim().toLowerCase()
  return runtimeStatusKeys.find(key => key === normalized) ?? 'unknown'
}

const runtimeStatusLabelKeys: Record<RuntimeStatusKey, string> = {
  running: 'bots.container.statusRunning',
  paused: 'bots.container.statusPaused',
  created: 'bots.container.statusCreated',
  stopped: 'bots.container.statusStopped',
  exited: 'bots.container.statusExited',
  unknown: 'bots.container.statusUnknown',
}

export function runtimeStatusLabelKey(key: RuntimeStatusKey): string {
  return runtimeStatusLabelKeys[key]
}
