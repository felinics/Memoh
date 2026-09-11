import { defineStore } from 'pinia'
import { reactive } from 'vue'
import { useQueryCache } from '@pinia/colada'
import { toast } from '@felinic/ui'
import i18n from '@/i18n'
import { useRouter } from 'vue-router'
import { getBotsByBotIdApps } from '@memohai/sdk'
import { invalidateBotApps, appInProgress, type AppItem } from '@/composables/api/useApps'
import { invalidateBotDependencies } from '@/composables/api/useWorkspaceDependencies'
import {
  streamAppOperation,
  type AppInstallTarget,
  type AppOperationAction,
  type AppStepKind,
  type AppUpdateSelection,
} from '@/composables/api/useAppStream'
import { onAuthSessionCleared } from '@/lib/auth-session'
import { apiErrorStatus, resolveApiErrorMessage } from '@/utils/api-error'
import type { DependencyLogLine, DependencyProgressStatus } from '@/utils/workspace-dependency'

// Streamed App operations that outlive the dialog that started them,
// keyed by bot + registry + app. The design mirrors the dependency
// operation store: closing the progress dialog only stops *showing* the
// operation, the SSE stream keeps being consumed here until the Server
// reports done / error, and a verdict nobody is watching lands as a toast.
// There is no cancel: the Server keeps running the operation regardless.

export interface AppOperationStep {
  kind: AppStepKind
  id: string
  /** running while the step streams; afterwards the Server's step_done status. */
  status: string
  version: string
  message: string
}

export interface AppOperation {
  /** `appOperationKey(botId, registryId, appId)`. */
  key: string
  botId: string
  targetId: string
  registryId: string
  appId: string
  installationId: string
  /** Localized App name for headings and toasts. */
  name: string
  action: AppOperationAction
  install?: AppInstallTarget
  update?: AppUpdateSelection
  removeUnreferencedRequired: boolean
  status: DependencyProgressStatus
  /** App status the Server reported in `done` (installed, partial, removed…). */
  result: string
  version: string
  steps: AppOperationStep[]
  lines: DependencyLogLine[]
  /** Localized failure summary; empty while running or after success. */
  error: string
}

export interface StartAppOperationInput {
  botId: string
  /** '' → the bot's current workspace target. */
  targetId: string
  registryId: string
  appId: string
  installationId?: string
  name: string
  action: AppOperationAction
  install?: AppInstallTarget
  update?: AppUpdateSelection
  removeUnreferencedRequired?: boolean
  /** Replaces the default success toast when the operation finishes unwatched. */
  onBackgroundDone?: (operation: AppOperation) => void
}

export type StartAppOperationResult =
  | { kind: 'started'; operation: AppOperation }
  | { kind: 'running'; operation: AppOperation }
  | { kind: 'busy'; operation: AppOperation }
  | { kind: 'invalid' }

const MAX_LOG_LINES = 2000
const ACTIONABLE_TOAST_MS = 8000
// A stream that ends without a terminal event (proxy hiccup, write deadline
// on a stalled connection) says nothing about the operation: the Server keeps
// running it. The outcome is read back from the App list instead.
const RECONCILE_POLL_MS = 3000
const RECONCILE_MAX_MS = 10 * 60_000

export function appOperationKey(botId: string, registryId: string, appId: string): string {
  return `${botId}/${registryId}/${appId}`
}

export const useAppOperationsStore = defineStore('app-operations', () => {
  const t = i18n.global.t
  const router = useRouter()

  const operations = reactive(new Map<string, AppOperation>())
  const viewers = new Map<string, Set<string>>()
  const controllers = new Map<string, AbortController>()
  const doneHandlers = new Map<string, (operation: AppOperation) => void>()
  let lineSequence = 0

  function get(botId: string, registryId: string | undefined, appId: string | undefined): AppOperation | undefined {
    if (!botId || !registryId || !appId) return undefined
    return operations.get(appOperationKey(botId, registryId, appId))
  }

  /** The operation streaming for this bot, if any: one workspace script at a time. */
  function runningFor(botId: string): AppOperation | undefined {
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

  function view(key: string, viewerId: string) {
    if (!operations.has(key)) return
    let set = viewers.get(key)
    if (!set) {
      set = new Set()
      viewers.set(key, set)
    }
    set.add(viewerId)
  }

  function unview(key: string, viewerId: string) {
    const set = viewers.get(key)
    if (!set) return
    set.delete(viewerId)
    if (set.size > 0) return
    viewers.delete(key)
    const operation = operations.get(key)
    if (operation && operation.status !== 'running') forget(key)
  }

  function pushLine(operation: AppOperation, stream: DependencyLogLine['stream'], data: string) {
    operation.lines.push({ id: ++lineSequence, stream, data })
    if (operation.lines.length > MAX_LOG_LINES) {
      operation.lines.splice(0, operation.lines.length - MAX_LOG_LINES)
    }
  }

  function viewApps(botId: string) {
    void router.push({
      name: 'bot-detail',
      params: { botName: botId },
      query: { tab: 'apps' },
    }).catch(() => {})
  }

  function doneMessage(operation: AppOperation): string {
    const args = { name: operation.name }
    switch (operation.action) {
      case 'remove':
        return t('apps.background.removed', args)
      case 'update':
        return t('apps.background.updated', args)
      default:
        return operation.result === 'partial'
          ? t('apps.background.partial', args)
          : t('apps.background.installed', args)
    }
  }

  function notifyBackground(operation: AppOperation) {
    if (operation.status === 'unknown') {
      toast.warning(t('apps.progress.unknownTitle'), {
        description: operation.error,
        duration: ACTIONABLE_TOAST_MS,
        action: { label: t('apps.viewBotApps'), onClick: () => viewApps(operation.botId) },
      })
      return
    }
    if (operation.status === 'done') {
      const handler = doneHandlers.get(operation.key)
      if (handler) {
        handler(operation)
        return
      }
      const notify = operation.result === 'partial' ? toast.warning : toast.success
      notify(doneMessage(operation), {
        duration: ACTIONABLE_TOAST_MS,
        action: { label: t('apps.viewBotApps'), onClick: () => viewApps(operation.botId) },
      })
      return
    }
    toast.error(t('apps.background.failed', { name: operation.name }), {
      description: operation.error,
      duration: ACTIONABLE_TOAST_MS,
    })
  }

  function settle(operation: AppOperation) {
    const queryCache = useQueryCache()
    void invalidateBotApps(queryCache, operation.botId)
    void invalidateBotDependencies(queryCache, operation.botId)
    void queryCache.invalidateQueries({ key: ['bot-connectors', operation.botId] })
    void queryCache.invalidateQueries({ key: ['bot-agents', operation.botId] })
    if (!isViewed(operation.key)) {
      notifyBackground(operation)
      forget(operation.key)
    }
  }

  function stepFor(operation: AppOperation, kind: AppStepKind, id: string): AppOperationStep {
    let step = operation.steps.find(entry => entry.kind === kind && entry.id === id)
    if (!step) {
      step = { kind, id, status: 'running', version: '', message: '' }
      operation.steps.push(step)
    }
    return step
  }

  function delay(ms: number, signal: AbortSignal): Promise<void> {
    return new Promise((resolve) => {
      const timer = setTimeout(resolve, ms)
      signal.addEventListener('abort', () => { clearTimeout(timer); resolve() }, { once: true })
    })
  }

  /** What a step that was still streaming ends up as once the Server confirms the operation. */
  function settledStepStatus(action: AppOperationAction): string {
    switch (action) {
      case 'update': return 'updated'
      case 'remove': return 'removed'
      default: return 'installed'
    }
  }

  function applySettled(operation: AppOperation, item: AppItem | undefined) {
    for (const step of operation.steps) {
      if (step.status === 'running') step.status = settledStepStatus(operation.action)
    }
    if (operation.action === 'remove') {
      operation.status = 'done'
      operation.result = 'removed'
      return
    }
    if (item?.status === 'failed') {
      operation.status = 'error'
      operation.error = item.last_error || t('apps.progress.failedTitle')
      return
    }
    operation.status = 'done'
    operation.result = item?.status ?? ''
    if (item?.version) operation.version = item.version
  }

  /**
   * Polls the App list until the Server has recorded the outcome of an
   * operation whose stream was lost. Returns false when it stays in progress
   * past the deadline.
   */
  async function reconcile(operation: AppOperation, signal: AbortSignal): Promise<boolean> {
    pushLine(operation, 'stderr', t('apps.progress.reconnecting'))
    const deadline = Date.now() + RECONCILE_MAX_MS
    while (Date.now() < deadline && !signal.aborted) {
      await delay(RECONCILE_POLL_MS, signal)
      if (signal.aborted) return false
      let items: AppItem[]
      try {
        const { data } = await getBotsByBotIdApps({
          path: { bot_id: operation.botId },
          query: operation.targetId ? { workspace_target_id: operation.targetId } : undefined,
          signal,
          throwOnError: true,
        })
        items = data.items ?? []
      } catch {
        continue
      }
      const item = items.find(entry => entry.registry_id === operation.registryId && entry.app_id === operation.appId)
      if (operation.action === 'remove') {
        if (item?.installation_id && appInProgress(item)) continue
        applySettled(operation, item?.installation_id ? item : undefined)
        return true
      }
      if (!item || appInProgress(item)) continue
      applySettled(operation, item)
      return true
    }
    return false
  }

  async function consume(operation: AppOperation, signal: AbortSignal) {
    try {
      const stream = streamAppOperation({
        botId: operation.botId,
        action: operation.action,
        installationId: operation.installationId || undefined,
        install: operation.install,
        update: operation.update,
        registryId: operation.registryId,
        appId: operation.appId,
        removeUnreferencedRequired: operation.removeUnreferencedRequired,
        signal,
      })
      for await (const event of stream) {
        if (signal.aborted) return
        switch (event.type) {
          case 'started':
            if (event.version) operation.version = event.version
            break
          case 'step':
            stepFor(operation, event.kind, event.id).status = 'running'
            break
          case 'log':
            pushLine(operation, event.stream, event.data)
            break
          case 'step_done': {
            const step = stepFor(operation, event.kind, event.id)
            step.status = event.status
            step.version = event.version ?? ''
            step.message = event.message ?? ''
            break
          }
          case 'done':
            operation.status = 'done'
            operation.result = event.status ?? ''
            if (event.version) operation.version = event.version
            break
          case 'error':
            operation.status = 'error'
            operation.error = event.message
            break
          default:
            break
        }
      }
      if (operation.status === 'running' && !(await reconcile(operation, signal))) {
        operation.status = 'unknown'
        operation.error = t('apps.progress.unknownHint')
      }
    } catch (error) {
      if (signal.aborted) return
      if (operation.status !== 'running') return
      if (apiErrorStatus(error)) {
        operation.status = 'error'
        operation.error = resolveApiErrorMessage(error, t('apps.progress.failedTitle'))
      } else if (!(await reconcile(operation, signal))) {
        operation.status = 'unknown'
        operation.error = t('apps.progress.unknownHint')
      }
    } finally {
      if (!signal.aborted) settle(operation)
    }
  }

  function run(operation: AppOperation) {
    controllers.get(operation.key)?.abort()
    const controller = new AbortController()
    controllers.set(operation.key, controller)
    operation.status = 'running'
    operation.result = ''
    operation.steps = []
    operation.lines = []
    operation.error = ''
    void consume(operation, controller.signal)
  }

  /**
   * Starts one operation, or reports why it did not: the same App already
   * streaming is returned as `running`, another App of the bot streaming
   * as `busy` (the Server serializes workspace scripts).
   */
  function start(input: StartAppOperationInput): StartAppOperationResult {
    if (!input.botId || !input.registryId || !input.appId) return { kind: 'invalid' }
    const key = appOperationKey(input.botId, input.registryId, input.appId)
    const existing = operations.get(key)
    if (existing?.status === 'running') return { kind: 'running', operation: existing }
    const other = runningFor(input.botId)
    if (other) return { kind: 'busy', operation: other }

    const previousViewers = viewers.get(key)
    forget(key)
    if (previousViewers?.size) viewers.set(key, previousViewers)
    if (input.onBackgroundDone) doneHandlers.set(key, input.onBackgroundDone)

    const operation = reactive<AppOperation>({
      key,
      botId: input.botId,
      targetId: input.targetId,
      registryId: input.registryId,
      appId: input.appId,
      installationId: input.installationId ?? '',
      name: input.name,
      action: input.action,
      install: input.install,
      update: input.update,
      removeUnreferencedRequired: input.removeUnreferencedRequired ?? false,
      status: 'running',
      result: '',
      version: input.install?.revision ? '' : '',
      steps: [],
      lines: [],
      error: '',
    })
    operations.set(key, operation)
    run(operation)
    return { kind: 'started', operation }
  }

  /** Replays a failed operation in place. */
  function retry(key: string): boolean {
    const operation = operations.get(key)
    if (!operation || operation.status !== 'error') return false
    if (runningFor(operation.botId)) return false
    run(operation)
    return true
  }

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
