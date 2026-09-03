import { defineStore } from 'pinia'
import { reactive } from 'vue'
import { useQueryCache } from '@pinia/colada'
import { toast } from '@felinic/ui'
import i18n from '@/i18n'
import router from '@/router'
import {
  botDependenciesQueryKey,
  invalidateBotDependencies,
  type DependencyItem,
  type DependencyListResponse,
  type DependencyOperationAction,
  type DependencyStatus,
} from '@/composables/api/useWorkspaceDependencies'
import { streamDependencyOperation } from '@/composables/api/useWorkspaceDependencyStream'
import { onAuthSessionCleared } from '@/lib/auth-session'
import { resolveApiErrorMessage } from '@/utils/api-error'
import {
  dependencyDisplayName,
  formatDependencyVersion,
  type DependencyLogLine,
  type DependencyProgressStatus,
} from '@/utils/workspace-dependency'

// Streamed dependency operations that outlive the dialog that started them.
// Every surface that can start one (the bot's Dependencies tab, the
// Supermarket's install dialog, the enable-agent preflight) hands the stream
// to this store, keyed by bot + dependency, so closing the progress dialog
// only stops *showing* the operation: the SSE stream keeps being consumed
// here until the Server reports `done` / `error`, the row keeps its
// in-progress badge, and whichever surface is around can reopen the same log.
//
// There is deliberately no cancel: aborting the HTTP stream would not stop the
// script inside the workspace, and the Server has no cancel API. The one
// abort is an auth session change, when the user the stream belongs to is
// gone.
//
// Visibility is tracked as a set of viewers per operation. While at least one
// dialog shows an operation, its verdict lands in that dialog; when it settles
// with nobody watching, the verdict lands as a toast instead, and the record
// is dropped. A settled operation that is still being shown is dropped when
// its last viewer closes.

export interface DependencyOperation {
  /** `operationKey(botId, depId)`. */
  key: string
  botId: string
  targetId: string
  item: DependencyItem
  action: DependencyOperationAction
  /** Version the user asked for; empty means the latest. Replayed by retry. */
  version: string
  status: DependencyProgressStatus
  lines: DependencyLogLine[]
  /** Localized failure summary; empty while running or after success. */
  error: string
  resultVersion: string
  entrypoint: string
}

export interface StartDependencyOperationInput {
  botId: string
  /** '' → the bot's current workspace target. */
  targetId: string
  item: DependencyItem
  action: DependencyOperationAction
  version?: string
  /**
   * Replaces the default success toast when the operation finishes with no
   * dialog watching it (the enable flow says "you can enable it now" rather
   * than "view dependencies"). Failures always use the default error toast.
   */
  onBackgroundDone?: (operation: DependencyOperation) => void
}

export type StartDependencyOperationResult =
  /** A new stream was opened. */
  | { kind: 'started'; operation: DependencyOperation }
  /** The same dependency is already being operated on: show that, send nothing. */
  | { kind: 'running'; operation: DependencyOperation }
  /** Another dependency of this bot is streaming (the Server would answer busy). */
  | { kind: 'busy'; operation: DependencyOperation }
  | { kind: 'invalid' }

// Keeps a runaway script (npm's progress spew) from growing the reactive log
// without bound; the head is dropped, the tail is what the user reads anyway.
const MAX_LOG_LINES = 2000

/** Toasts carrying an action stay a little longer than a plain confirmation. */
const ACTIONABLE_TOAST_MS = 8000

export function operationKey(botId: string, depId: string): string {
  return `${botId}/${depId}`
}

function optimisticStatus(action: DependencyOperationAction): DependencyStatus {
  switch (action) {
    case 'remove':
      return 'removing'
    case 'update':
      return 'updating'
    default:
      return 'installing'
  }
}

export const useDependencyOperationsStore = defineStore('dependency-operations', () => {
  const t = i18n.global.t

  const operations = reactive(new Map<string, DependencyOperation>())
  // Non-reactive bookkeeping: who is showing an operation, how to abort its
  // stream, and the caller's success toast override.
  const viewers = new Map<string, Set<string>>()
  const controllers = new Map<string, AbortController>()
  const doneHandlers = new Map<string, (operation: DependencyOperation) => void>()
  let lineSequence = 0

  function get(botId: string, depId: string | undefined): DependencyOperation | undefined {
    if (!botId || !depId) return undefined
    return operations.get(operationKey(botId, depId))
  }

  /** The operation streaming for this bot, if any — one script per workspace at a time. */
  function runningFor(botId: string): DependencyOperation | undefined {
    for (const operation of operations.values()) {
      if (operation.botId === botId && operation.status === 'running') return operation
    }
    return undefined
  }

  function isViewed(key: string): boolean {
    return (viewers.get(key)?.size ?? 0) > 0
  }

  function forget(key: string) {
    operations.delete(key)
    viewers.delete(key)
    doneHandlers.delete(key)
    controllers.get(key)?.abort()
    controllers.delete(key)
  }

  /** A dialog started showing the operation; its verdict will land there. */
  function view(key: string, viewerId: string) {
    if (!operations.has(key)) return
    let set = viewers.get(key)
    if (!set) {
      set = new Set()
      viewers.set(key, set)
    }
    set.add(viewerId)
  }

  /**
   * A dialog stopped showing the operation. A running one keeps streaming in
   * the background; a settled one is forgotten once nobody shows it.
   */
  function unview(key: string, viewerId: string) {
    const set = viewers.get(key)
    if (!set) return
    set.delete(viewerId)
    if (set.size > 0) return
    viewers.delete(key)
    const operation = operations.get(key)
    if (operation && operation.status !== 'running') forget(key)
  }

  // The list refetches only when the stream ends, so the row would keep saying
  // "installed" for the whole download; patching the cached status makes the
  // badge spin immediately, and every target of the bot is invalidated after.
  function patchCachedStatus(operation: DependencyOperation, status: DependencyStatus) {
    const queryCache = useQueryCache()
    const key = botDependenciesQueryKey(operation.botId, operation.targetId)
    const current = queryCache.getQueryData<DependencyListResponse>(key)
    if (!current?.items) return
    queryCache.setQueryData<DependencyListResponse>(key, {
      ...current,
      items: current.items.map(entry => (entry.id === operation.item.id ? { ...entry, status } : entry)),
    })
  }

  function pushLine(operation: DependencyOperation, stream: DependencyLogLine['stream'], data: string) {
    operation.lines.push({ id: ++lineSequence, stream, data })
    if (operation.lines.length > MAX_LOG_LINES) {
      operation.lines.splice(0, operation.lines.length - MAX_LOG_LINES)
    }
  }

  function viewDependencies(botId: string) {
    void router.push({
      name: 'bot-detail',
      params: { botName: botId },
      query: { tab: 'dependencies' },
    }).catch(() => {})
  }

  function doneMessage(operation: DependencyOperation): string {
    const args = { name: dependencyDisplayName(operation.item) }
    switch (operation.action) {
      case 'remove':
        return t('bots.dependencies.background.removed', args)
      case 'update':
        return t('bots.dependencies.background.updated', args)
      case 'reinstall':
        return t('bots.dependencies.background.reinstalled', args)
      default:
        return t('bots.dependencies.background.installed', args)
    }
  }

  // Sent to the background, no dialog is there to show the verdict; a toast
  // is the one place the outcome can still land.
  function notifyBackground(operation: DependencyOperation) {
    if (operation.status === 'done') {
      const handler = doneHandlers.get(operation.key)
      if (handler) {
        handler(operation)
        return
      }
      toast.success(doneMessage(operation), {
        duration: ACTIONABLE_TOAST_MS,
        action: {
          label: t('supermarket.viewBotDependencies'),
          onClick: () => viewDependencies(operation.botId),
        },
      })
      return
    }
    toast.error(
      t('bots.dependencies.background.failed', { name: dependencyDisplayName(operation.item) }),
      { description: operation.error, duration: ACTIONABLE_TOAST_MS },
    )
  }

  function settle(operation: DependencyOperation) {
    const queryCache = useQueryCache()
    void invalidateBotDependencies(queryCache, operation.botId)
    if (operation.item.category === 'agent') {
      void queryCache.invalidateQueries({ key: ['bot-agents', operation.botId] })
    }
    if (!isViewed(operation.key)) {
      notifyBackground(operation)
      forget(operation.key)
    }
  }

  async function consume(operation: DependencyOperation, signal: AbortSignal) {
    try {
      const stream = streamDependencyOperation(
        operation.botId,
        operation.item.id ?? '',
        operation.action,
        operation.targetId || undefined,
        { version: operation.version, signal },
      )
      for await (const event of stream) {
        if (signal.aborted) return
        switch (event.type) {
          case 'log':
            pushLine(operation, event.stream, event.data)
            break
          case 'done':
            operation.status = 'done'
            operation.resultVersion = formatDependencyVersion(event.version)
            operation.entrypoint = Object.values(event.entrypoints ?? {})[0] ?? ''
            break
          case 'error':
            operation.status = 'error'
            operation.error = event.message
            break
          default:
            // `started` carries nothing the log needs; the first script line
            // replaces the "Preparing…" placeholder on its own.
            break
        }
      }
      // A stream that closes without a verdict is a failure the Server did
      // not get to report (connection dropped); the list refetch below shows
      // whatever status it recorded.
      if (operation.status === 'running') {
        operation.status = 'error'
        operation.error = t('bots.dependencies.progress.failedTitle')
      }
    } catch (error) {
      if (signal.aborted) return
      operation.status = 'error'
      operation.error = resolveApiErrorMessage(error, t('bots.dependencies.progress.failedTitle'))
    } finally {
      if (!signal.aborted) settle(operation)
    }
  }

  function run(operation: DependencyOperation) {
    controllers.get(operation.key)?.abort()
    const controller = new AbortController()
    controllers.set(operation.key, controller)
    operation.status = 'running'
    operation.lines = []
    operation.error = ''
    operation.resultVersion = ''
    operation.entrypoint = ''
    patchCachedStatus(operation, optimisticStatus(operation.action))
    void consume(operation, controller.signal)
  }

  /**
   * Starts one operation, or reports why it did not. The same dependency
   * already streaming is returned as `running` so the caller reopens its log
   * instead of sending a second request (the Server would answer busy).
   */
  function start(input: StartDependencyOperationInput): StartDependencyOperationResult {
    const depId = input.item.id ?? ''
    if (!input.botId || !depId) return { kind: 'invalid' }
    const key = operationKey(input.botId, depId)
    const existing = operations.get(key)
    if (existing?.status === 'running') return { kind: 'running', operation: existing }
    const other = runningFor(input.botId)
    if (other) return { kind: 'busy', operation: other }

    // A settled record still shown somewhere is replaced; its viewers now
    // watch the new run, which is what a "retry from another surface" means.
    const previousViewers = viewers.get(key)
    forget(key)
    if (previousViewers?.size) viewers.set(key, previousViewers)
    if (input.onBackgroundDone) doneHandlers.set(key, input.onBackgroundDone)

    const operation = reactive<DependencyOperation>({
      key,
      botId: input.botId,
      targetId: input.targetId,
      item: input.item,
      action: input.action,
      version: input.version?.trim() ?? '',
      status: 'running',
      lines: [],
      error: '',
      resultVersion: '',
      entrypoint: '',
    })
    operations.set(key, operation)
    run(operation)
    return { kind: 'started', operation }
  }

  /** Replays a failed operation in place (the progress dialog's Retry). */
  function retry(key: string): boolean {
    const operation = operations.get(key)
    if (!operation || operation.status !== 'error') return false
    if (runningFor(operation.botId)) return false
    run(operation)
    return true
  }

  /** Drops every operation without a verdict: the user they belong to is gone. */
  function reset() {
    for (const key of [...operations.keys()]) forget(key)
  }

  onAuthSessionCleared(reset)

  return {
    operations,
    get,
    runningFor,
    isViewed,
    view,
    unview,
    start,
    retry,
    reset,
  }
})
