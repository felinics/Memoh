import {
  deleteBotsByBotIdAppsByInstallationId,
  postBotsByBotIdApps,
  postBotsByBotIdAppsByInstallationIdResume,
  postBotsByBotIdAppsUpdate,
} from '@memohai/sdk'
import {
  fetchSSEProblem,
  isSSEErrorEvent,
  localizeSSEErrorEvent,
  normalizeSSEFailure,
  type SSEErrorEvent,
} from './sse-error'

// codesync(app-stream): keep these manual SSE payload types in sync with
// internal/handlers/apps.go (AppStreamEvent). The generated
// HandlersAppStreamEvent flattens every frame into one all-optional bag,
// which is why the union is spelled out here.
export type AppStepKind = 'app' | 'dependency' | 'skills' | 'connector'

export type AppStreamEvent =
  | { type: 'started'; kind?: AppStepKind; id?: string; version?: string }
  | { type: 'step'; kind: AppStepKind; id: string }
  | { type: 'log'; kind?: AppStepKind; id?: string; stream: 'stdout' | 'stderr'; data: string }
  | { type: 'step_done'; kind: AppStepKind; id: string; status: string; version?: string; message?: string }
  | { type: 'done'; kind?: AppStepKind; id?: string; status?: string; version?: string }
  | SSEErrorEvent

export type AppOperationAction = 'install' | 'update' | 'resume' | 'remove'

export interface AppInstallTarget {
  registryId: string
  appId: string
  revision: string
  /** Omitted → the Server uses the bot's current target. */
  workspaceTargetId?: string
}

/** What an update touches: the release, the dependencies, or both. */
export interface AppUpdateSelection {
  release: boolean
  dependencies: string[]
  /** Omitted → the Server uses the bot's current target. */
  workspaceTargetId?: string
}

export interface AppStreamOptions {
  botId: string
  action: AppOperationAction
  /** Required by resume and remove. */
  installationId?: string
  /** Required by install. */
  install?: AppInstallTarget
  /** Required by update, together with registryId and appId. */
  update?: AppUpdateSelection
  registryId?: string
  appId?: string
  /** Remove: also drop auto-installed Apps that lose their last reference. */
  removeUnreferencedRequired?: boolean
  /**
   * Aborts the HTTP stream only. There is no cancel API: the operation keeps
   * running on the Server until it records the outcome.
   */
  signal?: AbortSignal
}

const STEP_KINDS = new Set<string>(['app', 'dependency', 'skills', 'connector'])

function optionalString(value: unknown): boolean {
  return value === undefined || typeof value === 'string'
}

export function isAppStreamEvent(value: unknown): value is AppStreamEvent {
  if (!value || typeof value !== 'object') return false
  const event = value as Record<string, unknown>
  if (event.kind !== undefined && (typeof event.kind !== 'string' || !STEP_KINDS.has(event.kind))) return false
  switch (event.type) {
    case 'started':
    case 'done':
      return optionalString(event.id) && optionalString(event.version) && optionalString(event.status)
    case 'step':
      return typeof event.kind === 'string' && typeof event.id === 'string'
    case 'log':
      return (event.stream === 'stdout' || event.stream === 'stderr') && typeof event.data === 'string'
    case 'step_done':
      return typeof event.kind === 'string' && typeof event.id === 'string' && typeof event.status === 'string'
        && optionalString(event.version) && optionalString(event.message)
    case 'error':
      return isSSEErrorEvent(event)
    default:
      return false
  }
}

const INVALID_EVENT = 'Invalid app stream event'

/**
 * Streams one App operation as parsed events. Connection failures
 * (Problem Details on a non-2xx) reject on the first `next()`; a mid-stream
 * failure throws after the last event.
 */
export async function* streamAppOperation(
  options: AppStreamOptions,
): AsyncGenerator<AppStreamEvent, void, unknown> {
  let streamError: unknown
  const common = {
    headers: { Accept: 'text/event-stream' },
    signal: options.signal,
    fetch: fetchSSEProblem,
    onSseError: (error: unknown) => {
      streamError = error
    },
    responseValidator: async (data: unknown) => {
      if (!isAppStreamEvent(data)) throw new Error(INVALID_EVENT)
    },
    sseMaxRetryAttempts: 1,
  }

  let result: { stream: unknown }
  switch (options.action) {
    case 'install': {
      const install = options.install
      if (!install) throw new Error('install target is required')
      result = await postBotsByBotIdApps({
        ...common,
        path: { bot_id: options.botId },
        body: {
          registry_id: install.registryId,
          app_id: install.appId,
          revision: install.revision,
          workspace_target_id: install.workspaceTargetId || undefined,
        },
      })
      break
    }
    case 'update': {
      const update = options.update
      if (!update || !options.registryId || !options.appId) throw new Error('update selection is required')
      result = await postBotsByBotIdAppsUpdate({
        ...common,
        path: { bot_id: options.botId },
        body: {
          registry_id: options.registryId,
          app_id: options.appId,
          workspace_target_id: update.workspaceTargetId || undefined,
          release: update.release,
          dependencies: update.dependencies,
        },
      })
      break
    }
    case 'resume':
      result = await postBotsByBotIdAppsByInstallationIdResume({
        ...common,
        path: { bot_id: options.botId, installation_id: requireInstallation(options) },
      })
      break
    default:
      result = await deleteBotsByBotIdAppsByInstallationId({
        ...common,
        path: { bot_id: options.botId, installation_id: requireInstallation(options) },
        query: options.removeUnreferencedRequired ? { remove_unreferenced_required: true } : undefined,
      })
  }

  for await (const event of result.stream as AsyncGenerator<unknown, void, unknown>) {
    if (!isAppStreamEvent(event)) throw new Error(INVALID_EVENT)
    yield event.type === 'error' ? localizeSSEErrorEvent(event) : event
  }

  if (streamError) {
    throw normalizeSSEFailure(streamError, 'App stream failed')
  }
}

function requireInstallation(options: AppStreamOptions): string {
  const id = options.installationId?.trim() ?? ''
  if (!id) throw new Error('installation id is required')
  return id
}
