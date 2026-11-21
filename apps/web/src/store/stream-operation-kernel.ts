import { shallowReactive } from 'vue'
import { useQueryCache } from '@pinia/colada'
import { toast } from '@felinic/ui'
import type { Router } from 'vue-router'
import { onAuthSessionCleared } from '@/lib/auth-session'
import type { DependencyLogLine, DependencyProgressStatus } from '@/utils/workspace-dependency'

const MAX_LOG_LINES = 2000
const ACTIONABLE_TOAST_MS = 8000

export interface BaseStreamOperation {
  key: string
  botId: string
  status: DependencyProgressStatus
  lines: DependencyLogLine[]
  error: string
}

export type StartStreamOperationResult<TOp> =
  | { kind: 'started'; operation: TOp }
  | { kind: 'running'; operation: TOp }
  | { kind: 'busy'; operation: TOp }
  | { kind: 'invalid' }

export interface StreamOperationKernelConfig<TOp extends BaseStreamOperation> {
  router: Router
  actionLabel: () => string
  unknownTitle: (operation: TOp) => string
  doneNotice: (operation: TOp) => { message: string; warning?: boolean }
  failedTitle: (operation: TOp) => string
  resetForRun: (operation: TOp) => void
  invalidateOnSettle: (queryCache: ReturnType<typeof useQueryCache>, operation: TOp) => void
  consume: (
    operation: TOp,
    signal: AbortSignal,
    helpers: { pushLine: (stream: DependencyLogLine['stream'], data: string) => void },
  ) => Promise<void>
}

export function abortableDelay(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms)
    signal.addEventListener('abort', () => { clearTimeout(timer); resolve() }, { once: true })
  })
}

/**
 * Polls a server endpoint until a disconnected stream's operation reaches a
 * terminal state or `maxMs` expires.
 */
export async function pollUntilReconciled(options: {
  intervalMs: number
  maxMs: number
  signal: AbortSignal
  check: () => Promise<boolean>
}): Promise<boolean> {
  const deadline = Date.now() + options.maxMs
  while (Date.now() < deadline && !options.signal.aborted) {
    await abortableDelay(options.intervalMs, options.signal)
    if (options.signal.aborted) return false
    if (await options.check()) return true
  }
  return false
}

/**
 * Headless operation-tracking kernel shared by App and Workspace Dependency
 * SSE stores: bounded log buffer, viewer ref-counting across dialogs, background
 * continuation on dialog close, unattended completion/error/unknown Toasts,
 * single-active-per-bot gating, and auth-session teardown.
 */
export function createStreamOperationKernel<TOp extends BaseStreamOperation>(
  config: StreamOperationKernelConfig<TOp>,
) {
  const operations = shallowReactive(new Map<string, TOp>())
  const viewers = new Map<string, Set<string>>()
  const controllers = new Map<string, AbortController>()
  const doneHandlers = new Map<string, (operation: TOp) => void>()
  let lineSequence = 0

  function getByKey(key: string): TOp | undefined {
    return operations.get(key)
  }

  function runningFor(botId: string): TOp | undefined {
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

  function pushLine(operation: TOp, stream: DependencyLogLine['stream'], data: string) {
    operation.lines.push({ id: ++lineSequence, stream, data })
    if (operation.lines.length > MAX_LOG_LINES) {
      operation.lines.splice(0, operation.lines.length - MAX_LOG_LINES)
    }
  }

  function openBotAppsTab(botId: string) {
    void config.router.push({
      name: 'bot-detail',
      params: { botName: botId },
      query: { tab: 'apps' },
    }).catch(() => {})
  }

  function notifyBackground(operation: TOp) {
    if (operation.status === 'unknown') {
      toast.warning(config.unknownTitle(operation), {
        description: operation.error,
        duration: ACTIONABLE_TOAST_MS,
        action: {
          label: config.actionLabel(),
          onClick: () => openBotAppsTab(operation.botId),
        },
      })
      return
    }
    if (operation.status === 'done') {
      const handler = doneHandlers.get(operation.key)
      if (handler) {
        handler(operation)
        return
      }
      const { message, warning } = config.doneNotice(operation)
      const notify = warning ? toast.warning : toast.success
      notify(message, {
        duration: ACTIONABLE_TOAST_MS,
        action: {
          label: config.actionLabel(),
          onClick: () => openBotAppsTab(operation.botId),
        },
      })
      return
    }
    toast.error(config.failedTitle(operation), {
      description: operation.error,
      duration: ACTIONABLE_TOAST_MS,
    })
  }

  function settle(operation: TOp) {
    const queryCache = useQueryCache()
    config.invalidateOnSettle(queryCache, operation)
    if (!isViewed(operation.key)) {
      notifyBackground(operation)
      forget(operation.key)
    }
  }

  async function executeWithSettle(operation: TOp, signal: AbortSignal) {
    try {
      await config.consume(operation, signal, {
        pushLine: (stream, data) => pushLine(operation, stream, data),
      })
    } finally {
      if (!signal.aborted) settle(operation)
    }
  }

  function run(operation: TOp) {
    controllers.get(operation.key)?.abort()
    const controller = new AbortController()
    controllers.set(operation.key, controller)
    operation.status = 'running'
    operation.lines = []
    operation.error = ''
    config.resetForRun(operation)
    void executeWithSettle(operation, controller.signal)
  }

  function startOperation(params: {
    key: string
    botId: string
    onBackgroundDone?: (operation: TOp) => void
    create: () => TOp
  }): StartStreamOperationResult<TOp> {
    const existing = operations.get(params.key)
    if (existing?.status === 'running') return { kind: 'running', operation: existing }
    const other = runningFor(params.botId)
    if (other) return { kind: 'busy', operation: other }

    const previousViewers = viewers.get(params.key)
    forget(params.key)
    if (previousViewers?.size) viewers.set(params.key, previousViewers)
    if (params.onBackgroundDone) doneHandlers.set(params.key, params.onBackgroundDone)

    const operation = params.create()
    operations.set(params.key, operation)
    run(operation)
    return { kind: 'started', operation }
  }

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
    getByKey,
    runningFor,
    isViewed,
    view,
    unview,
    startOperation,
    retry,
    reset,
  }
}
