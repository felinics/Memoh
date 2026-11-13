import {
  postBotsByBotIdDependenciesByDepIdInstall,
  postBotsByBotIdDependenciesByDepIdReinstall,
  postBotsByBotIdDependenciesByDepIdUpdate,
} from '@memohai/sdk'
import {
  isSSEErrorEvent,
  type SSEErrorEvent,
} from './sse-error'
import { streamJsonSse } from './stream-json-sse'
import type { DependencyOperationAction } from './useWorkspaceDependencies'

// codesync(workspace-dependency-stream): keep these manual SSE payload types in
// sync with internal/handlers/workspace_dependencies.go (the started / log /
// done / error event structs). The generated
// HandlersWorkspaceDependencyStreamEvent flattens them into one all-optional
// bag, which is why the union is spelled out here.
export type WorkspaceDependencyStreamEvent =
  | { type: 'started'; dependency_id: string; version?: string; definition_revision?: string }
  | { type: 'log'; stream: 'stdout' | 'stderr'; data: string }
  | { type: 'done'; version?: string; entrypoints?: Record<string, string>; definition_revision?: string }
  | SSEErrorEvent

export interface WorkspaceDependencyStreamRequestOptions {
  /** Conversation that requested this manager-confirmed operation. */
  sessionId?: string
  /** Revision returned by this operation's script preview. Omit to resolve latest. */
  definitionRevision?: string
  /**
   * Prerequisite revisions the confirmation showed, by dependency id. Missing
   * prerequisites then install only from these; omit to resolve them when
   * the operation starts.
   */
  prerequisiteRevisions?: Record<string, string>
  /**
   * Version to install / update / reinstall to. Empty means the latest the
   * catalog script resolves (or the manifest pin).
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
  if (event.definition_revision !== undefined && typeof event.definition_revision !== 'string') return false
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
  options: WorkspaceDependencyStreamRequestOptions = {},
): AsyncGenerator<WorkspaceDependencyStreamEvent, void, unknown> {
  // An empty version sends no body: the Server then resolves the latest (or
  // the manifest pin), which is exactly what "leave blank" promises.
  const version = options.version?.trim() ?? ''
  const definitionRevision = options.definitionRevision?.trim() ?? ''
  const sessionId = options.sessionId?.trim() ?? ''
  const body = version || definitionRevision || sessionId || options.prerequisiteRevisions
    ? {
        version: version || undefined,
        definition_revision: definitionRevision || undefined,
        session_id: sessionId || undefined,
        prerequisite_revisions: options.prerequisiteRevisions,
      }
    : undefined

  yield* streamJsonSse<WorkspaceDependencyStreamEvent>({
    signal: options.signal,
    invalidEventMessage: INVALID_EVENT,
    failureMessage: 'Workspace dependency stream failed',
    isEvent: isWorkspaceDependencyStreamEvent,
    open: (common) => {
      const request = { ...common, path: { bot_id: botId, dep_id: depId }, body }
      if (action === 'update') return postBotsByBotIdDependenciesByDepIdUpdate(request)
      if (action === 'reinstall') return postBotsByBotIdDependenciesByDepIdReinstall(request)
      return postBotsByBotIdDependenciesByDepIdInstall(request)
    },
  })
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
      { version: options.version, definitionRevision: options.definitionRevision, prerequisiteRevisions: options.prerequisiteRevisions, sessionId: options.sessionId, signal: options.signal },
    ),
  }
}
