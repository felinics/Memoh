import { reactive } from 'vue'
import { defineStore } from 'pinia'
import i18n from '@/i18n'
import { useRouter } from 'vue-router'
import { getBotsByBotIdApps } from '@memohai/sdk'
import { invalidateBotApps, appInProgress, appLastError, type AppItem } from '@/composables/api/useApps'
import { invalidateBotDependencies } from '@/composables/api/useWorkspaceDependencies'
import {
  streamAppOperation,
  type AppInstallTarget,
  type AppOperationAction,
  type AppStepKind,
  type AppUpdateSelection,
} from '@/composables/api/useAppStream'
import { isApiErrorAnswered, resolveApiErrorMessage } from '@/utils/api-error'
import type { DependencyLogLine, DependencyProgressStatus } from '@/utils/workspace-dependency'
import {
  createStreamOperationKernel,
  pollUntilReconciled,
  type StartStreamOperationResult,
} from './stream-operation-kernel'

// Streamed App operations that outlive the dialog that started them,
// keyed by bot + registry + app. Shared lifecycle (bounded log buffer, viewer
// ref-counting, background Toasts, auth teardown) lives in stream-operation-kernel.

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

export type StartAppOperationResult = StartStreamOperationResult<AppOperation>

const RECONCILE_POLL_MS = 3000
const RECONCILE_MAX_MS = 10 * 60_000

export function appOperationKey(botId: string, registryId: string, appId: string): string {
  return `${botId}/${registryId}/${appId}`
}

export const useAppOperationsStore = defineStore('app-operations', () => {
  const t = i18n.global.t
  const router = useRouter()

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

  function stepFor(operation: AppOperation, kind: AppStepKind, id: string): AppOperationStep {
    let step = operation.steps.find(entry => entry.kind === kind && entry.id === id)
    if (!step) {
      step = { kind, id, status: 'running', version: '', message: '' }
      operation.steps.push(step)
    }
    return step
  }

  function settledStepStatus(action: AppOperationAction): string {
    switch (action) {
      case 'update': return 'updated'
      case 'remove': return 'removed'
      default: return 'installed'
    }
  }

  function applySettled(operation: AppOperation, item: AppItem | undefined) {
    // Failed removals retain the installation. Check failure before inferring
    // either step completion or a successful removal.
    if (item?.status === 'failed') {
      operation.status = 'error'
      operation.error = appLastError(item, key => t(key)) || t('apps.progress.failedTitle')
      return
    }
    for (const step of operation.steps) {
      if (step.status === 'running') step.status = settledStepStatus(operation.action)
    }
    if (operation.action === 'remove') {
      operation.status = 'done'
      operation.result = 'removed'
      return
    }
    operation.status = 'done'
    operation.result = item?.status ?? ''
    if (item?.version) operation.version = item.version
  }

  async function reconcile(
    operation: AppOperation,
    signal: AbortSignal,
    pushLine: (stream: DependencyLogLine['stream'], data: string) => void,
  ): Promise<boolean> {
    pushLine('stderr', t('apps.progress.reconnecting'))
    return pollUntilReconciled({
      intervalMs: RECONCILE_POLL_MS,
      maxMs: RECONCILE_MAX_MS,
      signal,
      check: async () => {
        let items: AppItem[]
        try {
          const { data } = await getBotsByBotIdApps({
            path: { bot_id: operation.botId },
            signal,
            throwOnError: true,
          })
          items = data.items ?? []
        } catch {
          return false
        }
        const item = items.find(entry => entry.registry_id === operation.registryId && entry.app_id === operation.appId)
        if (operation.action === 'remove') {
          if (item?.installation_id && item.status !== 'failed') return false
          applySettled(operation, item?.installation_id ? item : undefined)
          return true
        }
        if (!item || appInProgress(item)) return false
        applySettled(operation, item)
        return true
      },
    })
  }

  const kernel = createStreamOperationKernel<AppOperation>({
    router,
    actionLabel: () => t('apps.viewBotApps'),
    unknownTitle: () => t('apps.progress.unknownTitle'),
    doneNotice: operation => ({
      message: doneMessage(operation),
      warning: operation.result === 'partial',
    }),
    failedTitle: operation => t('apps.background.failed', { name: operation.name }),
    resetForRun: (operation) => {
      operation.result = ''
      operation.steps = []
    },
    invalidateOnSettle: (queryCache, operation) => {
      void invalidateBotApps(queryCache, operation.botId)
      void invalidateBotDependencies(queryCache, operation.botId)
      void queryCache.invalidateQueries({ key: ['bot-connectors', operation.botId] })
      void queryCache.invalidateQueries({ key: ['bot-agents', operation.botId] })
    },
    consume: async (operation, signal, { pushLine }) => {
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
              pushLine(event.stream, event.data)
              break
            case 'step_done': {
              const step = stepFor(operation, event.kind, event.id)
              step.status = event.status
              step.version = event.version ?? ''
              step.message = event.code ? t(`errors.${event.code}`) : (event.message ?? '')
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
        if (operation.status === 'running' && !(await reconcile(operation, signal, pushLine))) {
          operation.status = 'unknown'
          operation.error = t('apps.progress.unknownHint')
        }
      } catch (error) {
        if (signal.aborted || operation.status !== 'running') return
        if (isApiErrorAnswered(error)) {
          operation.status = 'error'
          operation.error = resolveApiErrorMessage(error, t('apps.progress.failedTitle'))
        } else if (!(await reconcile(operation, signal, pushLine))) {
          operation.status = 'unknown'
          operation.error = t('apps.progress.unknownHint')
        }
      }
    },
  })

  function get(botId: string, registryId: string | undefined, appId: string | undefined): AppOperation | undefined {
    if (!botId || !registryId || !appId) return undefined
    return kernel.getByKey(appOperationKey(botId, registryId, appId))
  }

  function start(input: StartAppOperationInput): StartAppOperationResult {
    if (!input.botId || !input.registryId || !input.appId) return { kind: 'invalid' }
    const key = appOperationKey(input.botId, input.registryId, input.appId)
    return kernel.startOperation({
      key,
      botId: input.botId,
      onBackgroundDone: input.onBackgroundDone,
      create: () => reactive<AppOperation>({
        key,
        botId: input.botId,
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
        version: '',
        steps: [],
        lines: [],
        error: '',
      }),
    })
  }

  return {
    operations: kernel.operations,
    get,
    runningFor: kernel.runningFor,
    isViewed: kernel.isViewed,
    view: kernel.view,
    unview: kernel.unview,
    start,
    retry: kernel.retry,
    reset: kernel.reset,
  }
})
