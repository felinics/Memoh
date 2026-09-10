import {
  deleteBotsByBotIdPackagesByInstallationId,
  postBotsByBotIdPackages,
  postBotsByBotIdPackagesByInstallationIdResume,
  postBotsByBotIdPackagesByInstallationIdUpdate,
} from '@memohai/sdk'
import {
  fetchSSEProblem,
  isSSEErrorEvent,
  localizeSSEErrorEvent,
  normalizeSSEFailure,
  type SSEErrorEvent,
} from './sse-error'

// codesync(package-stream): keep these manual SSE payload types in sync with
// internal/handlers/packages.go (PackageStreamEvent). The generated
// HandlersPackageStreamEvent flattens every frame into one all-optional bag,
// which is why the union is spelled out here.
export type PackageStepKind = 'package' | 'dependency' | 'skills' | 'connector'

export type PackageStreamEvent =
  | { type: 'started'; kind?: PackageStepKind; id?: string; version?: string }
  | { type: 'step'; kind: PackageStepKind; id: string }
  | { type: 'log'; kind?: PackageStepKind; id?: string; stream: 'stdout' | 'stderr'; data: string }
  | { type: 'step_done'; kind: PackageStepKind; id: string; status: string; version?: string; message?: string }
  | { type: 'done'; kind?: PackageStepKind; id?: string; status?: string; version?: string }
  | SSEErrorEvent

export type PackageOperationAction = 'install' | 'update' | 'resume' | 'remove'

export interface PackageInstallTarget {
  registryId: string
  packageId: string
  revision: string
  /** Omitted → the Server uses the bot's current target. */
  workspaceTargetId?: string
}

export interface PackageStreamOptions {
  botId: string
  action: PackageOperationAction
  /** Required by update, resume and remove. */
  installationId?: string
  /** Required by install. */
  install?: PackageInstallTarget
  /** Remove: also drop auto-installed Packages that lose their last reference. */
  removeUnreferencedRequired?: boolean
  /**
   * Aborts the HTTP stream only. There is no cancel API: the operation keeps
   * running on the Server until it records the outcome.
   */
  signal?: AbortSignal
}

const STEP_KINDS = new Set<string>(['package', 'dependency', 'skills', 'connector'])

function optionalString(value: unknown): boolean {
  return value === undefined || typeof value === 'string'
}

export function isPackageStreamEvent(value: unknown): value is PackageStreamEvent {
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

const INVALID_EVENT = 'Invalid package stream event'

/**
 * Streams one Package operation as parsed events. Connection failures
 * (Problem Details on a non-2xx) reject on the first `next()`; a mid-stream
 * failure throws after the last event.
 */
export async function* streamPackageOperation(
  options: PackageStreamOptions,
): AsyncGenerator<PackageStreamEvent, void, unknown> {
  let streamError: unknown
  const common = {
    headers: { Accept: 'text/event-stream' },
    signal: options.signal,
    fetch: fetchSSEProblem,
    onSseError: (error: unknown) => {
      streamError = error
    },
    responseValidator: async (data: unknown) => {
      if (!isPackageStreamEvent(data)) throw new Error(INVALID_EVENT)
    },
    sseMaxRetryAttempts: 1,
  }

  let result: { stream: unknown }
  switch (options.action) {
    case 'install': {
      const install = options.install
      if (!install) throw new Error('install target is required')
      result = await postBotsByBotIdPackages({
        ...common,
        path: { bot_id: options.botId },
        body: {
          registry_id: install.registryId,
          package_id: install.packageId,
          revision: install.revision,
          workspace_target_id: install.workspaceTargetId || undefined,
        },
      })
      break
    }
    case 'update':
      result = await postBotsByBotIdPackagesByInstallationIdUpdate({
        ...common,
        path: { bot_id: options.botId, installation_id: requireInstallation(options) },
      })
      break
    case 'resume':
      result = await postBotsByBotIdPackagesByInstallationIdResume({
        ...common,
        path: { bot_id: options.botId, installation_id: requireInstallation(options) },
      })
      break
    default:
      result = await deleteBotsByBotIdPackagesByInstallationId({
        ...common,
        path: { bot_id: options.botId, installation_id: requireInstallation(options) },
        query: options.removeUnreferencedRequired ? { remove_unreferenced_required: true } : undefined,
      })
  }

  for await (const event of result.stream as AsyncGenerator<unknown, void, unknown>) {
    if (!isPackageStreamEvent(event)) throw new Error(INVALID_EVENT)
    yield event.type === 'error' ? localizeSSEErrorEvent(event) : event
  }

  if (streamError) {
    throw normalizeSSEFailure(streamError, 'Package stream failed')
  }
}

function requireInstallation(options: PackageStreamOptions): string {
  const id = options.installationId?.trim() ?? ''
  if (!id) throw new Error('installation id is required')
  return id
}
