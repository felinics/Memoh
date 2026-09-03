import {
  deleteBotsByBotIdDependenciesByDepId,
  postBotsByBotIdDependenciesByDepIdInstall,
  postBotsByBotIdDependenciesByDepIdReinstall,
  postBotsByBotIdDependenciesByDepIdUpdate,
} from '@memohai/sdk'
import {
  fetchSSEProblem,
  isSSEErrorEvent,
  localizeSSEErrorEvent,
  normalizeSSEFailure,
  type SSEErrorEvent,
} from './sse-error'
import type { DependencyOperationAction } from './useWorkspaceDependencies'

// codesync(workspace-dependency-stream): keep these manual SSE payload types in
// sync with internal/handlers/workspace_dependencies.go (the started / log /
// done / error event structs). The generated
// HandlersWorkspaceDependencyStreamEvent flattens them into one all-optional
// bag, which is why the union is spelled out here.
export type WorkspaceDependencyStreamEvent =
  | { type: 'started'; dependency_id: string; version?: string }
  | { type: 'log'; stream: 'stdout' | 'stderr'; data: string }
  | { type: 'done'; version?: string; entrypoints?: Record<string, string> }
  | SSEErrorEvent

export interface WorkspaceDependencyStreamRequestOptions {
  /**
   * Version to install / update / reinstall to. Empty means the latest the
   * catalog script resolves (or the manifest pin). Ignored by remove.
   */
  version?: string
  /**
   * Aborts the HTTP stream only. There is no cancel API: the script keeps
   * running inside the workspace and the row keeps its in-progress status
   * until the Server records the outcome.
   */
  signal?: AbortSignal
}

export interface WorkspaceDependencyStreamOptions extends WorkspaceDependencyStreamRequestOptions {
  botId: string
  depId: string
  action: DependencyOperationAction
  /** Omitted → the Server uses the bot's current target. */
  workspaceTargetId?: string
}

function isStringRecord(value: unknown): value is Record<string, string> {
  return !!value
    && typeof value === 'object'
    && !Array.isArray(value)
    && Object.values(value as Record<string, unknown>).every(entry => typeof entry === 'string')
}

export function isWorkspaceDependencyStreamEvent(value: unknown): value is WorkspaceDependencyStreamEvent {
  if (!value || typeof value !== 'object') return false
  const event = value as Record<string, unknown>
  switch (event.type) {
    case 'started':
      return typeof event.dependency_id === 'string'
        && (event.version === undefined || typeof event.version === 'string')
    case 'log':
      return (event.stream === 'stdout' || event.stream === 'stderr')
        && typeof event.data === 'string'
    case 'done':
      return (event.version === undefined || typeof event.version === 'string')
        && (event.entrypoints === undefined || isStringRecord(event.entrypoints))
    case 'error':
      return isSSEErrorEvent(event)
    default:
      return false
  }
}

const INVALID_EVENT = 'Invalid workspace dependency stream event'

/**
 * Streams one dependency operation as parsed events. Connection failures
 * (Problem Details on a non-2xx) reject on the first `next()`; a mid-stream
 * failure throws after the last event. Comment heartbeat lines (`: ping`) are
 * dropped by the SDK parser, which only yields chunks carrying a `data:` line.
 */
export async function* streamDependencyOperation(
  botId: string,
  depId: string,
  action: DependencyOperationAction,
  workspaceTargetId?: string,
  options: WorkspaceDependencyStreamRequestOptions = {},
): AsyncGenerator<WorkspaceDependencyStreamEvent, void, unknown> {
  let streamError: unknown

  // One options object for the four generated SSE functions: their *Data
  // shapes are identical (path bot_id/dep_id, optional workspace_target_id),
  // and the generated functions keep each route's URL single-sourced.
  const request = {
    path: { bot_id: botId, dep_id: depId },
    query: workspaceTargetId ? { workspace_target_id: workspaceTargetId } : undefined,
    headers: { Accept: 'text/event-stream' },
    signal: options.signal,
    fetch: fetchSSEProblem,
    onSseError: (error: unknown) => {
      streamError = error
    },
    responseValidator: async (data: unknown) => {
      if (!isWorkspaceDependencyStreamEvent(data)) {
        throw new Error(INVALID_EVENT)
      }
    },
    sseMaxRetryAttempts: 1,
  }

  // An empty version sends no body: the Server then resolves the latest (or
  // the manifest pin), which is exactly what "leave blank" promises.
  const version = options.version?.trim() ?? ''
  const versioned = { ...request, body: version ? { version } : undefined }

  const result = action === 'remove'
    ? await deleteBotsByBotIdDependenciesByDepId(request)
    : action === 'update'
      ? await postBotsByBotIdDependenciesByDepIdUpdate(versioned)
      : action === 'reinstall'
        ? await postBotsByBotIdDependenciesByDepIdReinstall(versioned)
        : await postBotsByBotIdDependenciesByDepIdInstall(versioned)

  for await (const event of result.stream as AsyncGenerator<unknown, void, unknown>) {
    if (!isWorkspaceDependencyStreamEvent(event)) {
      throw new Error(INVALID_EVENT)
    }
    yield event.type === 'error'
      ? localizeSSEErrorEvent(event)
      : event
  }

  if (streamError) {
    throw normalizeSSEFailure(streamError, 'Workspace dependency stream failed')
  }
}

/** Options-object form of `streamDependencyOperation`, same generator. */
export function openWorkspaceDependencyStream(
  options: WorkspaceDependencyStreamOptions,
): { stream: AsyncGenerator<WorkspaceDependencyStreamEvent, void, unknown> } {
  return {
    stream: streamDependencyOperation(
      options.botId,
      options.depId,
      options.action,
      options.workspaceTargetId,
      { version: options.version, signal: options.signal },
    ),
  }
}
