import { defineStore } from 'pinia'
import { reactive, unref } from 'vue'
import { useQueryCache } from '@pinia/colada'
import i18n from '@/i18n'
import { useRouter } from 'vue-router'
import {
  botDependenciesQueryKey,
  invalidateBotDependencies,
  type DependencyItem,
  type DependencyListResponse,
  type DependencyOperationAction,
  type DependencyStatus,
} from '@/composables/api/useWorkspaceDependencies'
import { streamDependencyOperation } from '@/composables/api/useWorkspaceDependencyStream'
import { isApiErrorAnswered, isApiErrorCode, resolveApiErrorMessage } from '@/utils/api-error'
import {
  dependencyDisplayName,
  formatDependencyVersion,
  type DependencyLogLine,
  type DependencyProgressStatus,
} from '@/utils/workspace-dependency'
import {
  createStreamOperationKernel,
  type StartStreamOperationResult,
} from './stream-operation-kernel'

// Streamed dependency operations that outlive the dialog that started them.
// Shared lifecycle (bounded log buffer, viewer ref-counting, background Toasts,
// auth teardown) lives in stream-operation-kernel; this store keeps the
// confirmed-revision / session replay state and optimistic query-cache badge.

export interface DependencyOperation {
  definitionRevision: string
  /** Prerequisite revisions the confirmation showed. Replayed by retry. */
  prerequisiteRevisions?: Record<string, string>
  /** `operationKey(botId, depId)`. */
  key: string
  botId: string

  sessionId?: string
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
  definitionRevision?: string
  prerequisiteRevisions?: Record<string, string>
  botId: string

  /** Source conversation for authorized operation notifications only. */
  sessionId?: string
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

export type StartDependencyOperationResult = StartStreamOperationResult<DependencyOperation>

export function operationKey(botId: string, depId: string): string {
  return `${botId}/${depId}`
}

function optimisticStatus(action: DependencyOperationAction): DependencyStatus {
  return action === 'update' ? 'updating' : 'installing'
}

export const useDependencyOperationsStore = defineStore('dependency-operations', () => {
  const t = i18n.global.t
  const router = useRouter()

  function patchCachedStatus(operation: DependencyOperation, status: DependencyStatus) {
    const queryCache = useQueryCache()
    const key = botDependenciesQueryKey(operation.botId)
    const current = queryCache.getQueryData<DependencyListResponse>(key)
    if (!current?.items) return
    queryCache.setQueryData<DependencyListResponse>(key, {
      ...current,
      items: current.items.map(entry => (entry.id === operation.item.id ? { ...entry, status } : entry)),
    })
  }

  function displayName(operation: DependencyOperation): string {
    return dependencyDisplayName(operation.item, unref(i18n.global.locale))
  }

  function doneMessage(operation: DependencyOperation): string {
    const args = { name: displayName(operation) }
    switch (operation.action) {
      case 'update':
        return t('bots.dependencies.background.updated', args)
      case 'reinstall':
        return t('bots.dependencies.background.reinstalled', args)
      default:
        return t('bots.dependencies.background.installed', args)
    }
  }

  const kernel = createStreamOperationKernel<DependencyOperation>({
    router,
    actionLabel: () => t('apps.viewBotApps'),
    unknownTitle: () => t('bots.dependencies.progress.unknownTitle'),
    doneNotice: operation => ({ message: doneMessage(operation) }),
    failedTitle: operation => t('bots.dependencies.background.failed', { name: displayName(operation) }),
    resetForRun: (operation) => {
      operation.resultVersion = ''
      operation.entrypoint = ''
      patchCachedStatus(operation, optimisticStatus(operation.action))
    },
    invalidateOnSettle: (queryCache, operation) => {
      void invalidateBotDependencies(queryCache, operation.botId)
      if (operation.item.category === 'agent') {
        void queryCache.invalidateQueries({ key: ['bot-agents', operation.botId] })
      }
    },
    consume: async (operation, signal, { pushLine }) => {
      try {
        const stream = streamDependencyOperation(
          operation.botId,
          operation.item.id ?? '',
          operation.action,
          {
            version: operation.version,
            definitionRevision: operation.definitionRevision || undefined,
            prerequisiteRevisions: operation.prerequisiteRevisions,
            sessionId: operation.sessionId,
            signal,
          },
        )
        for await (const event of stream) {
          if (signal.aborted) return
          switch (event.type) {
            case 'started':
              operation.definitionRevision = event.definition_revision ?? operation.definitionRevision
              break
            case 'log':
              pushLine(event.stream, event.data)
              break
            case 'done':
              operation.status = 'done'
              operation.resultVersion = formatDependencyVersion(event.version)
              operation.entrypoint = Object.values(event.entrypoints ?? {})[0] ?? ''
              break
            case 'error':
              operation.status = event.code === 'workspace_dependency_operation_unknown' ? 'unknown' : 'error'
              operation.error = operation.status === 'unknown' ? t('bots.dependencies.progress.unknownHint') : event.message
              break
            default:
              break
          }
        }
        // Losing the observation stream is not evidence that the script stopped.
        if (operation.status === 'running') {
          operation.status = 'unknown'
          operation.error = t('bots.dependencies.progress.unknownHint')
        }
      } catch (error) {
        if (signal.aborted || operation.status !== 'running') return
        operation.status = isApiErrorCode(error, 'workspace_dependency_operation_unknown') || !isApiErrorAnswered(error)
          ? 'unknown'
          : 'error'
        operation.error = operation.status === 'unknown'
          ? t('bots.dependencies.progress.unknownHint')
          : resolveApiErrorMessage(error, t('bots.dependencies.progress.failedTitle'))
      }
    },
  })

  function get(botId: string, depId: string | undefined): DependencyOperation | undefined {
    if (!botId || !depId) return undefined
    return kernel.getByKey(operationKey(botId, depId))
  }

  function start(input: StartDependencyOperationInput): StartDependencyOperationResult {
    const depId = input.item.id ?? ''
    if (!input.botId || !depId) return { kind: 'invalid' }
    const key = operationKey(input.botId, depId)
    return kernel.startOperation({
      key,
      botId: input.botId,
      onBackgroundDone: input.onBackgroundDone,
      create: () => reactive<DependencyOperation>({
        key,
        botId: input.botId,
        sessionId: input.sessionId,
        item: input.item,
        action: input.action,
        version: input.version?.trim() ?? '',
        definitionRevision: input.definitionRevision?.trim() || input.item.definition_revision?.trim() || '',
        prerequisiteRevisions: input.prerequisiteRevisions,
        status: 'running',
        lines: [],
        error: '',
        resultVersion: '',
        entrypoint: '',
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
