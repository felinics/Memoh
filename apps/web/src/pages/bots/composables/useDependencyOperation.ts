import { computed, onBeforeUnmount, reactive, shallowRef, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQueryCache } from '@pinia/colada'
import { toast } from '@felinic/ui'
import {
  botDependenciesQueryKey,
  invalidateBotDependencies,
  type DependencyItem,
  type DependencyListResponse,
  type DependencyOperationAction,
  type DependencyStatus,
} from '@/composables/api/useWorkspaceDependencies'
import { streamDependencyOperation } from '@/composables/api/useWorkspaceDependencyStream'
import { resolveApiErrorMessage } from '@/utils/api-error'
import {
  dependencyDisplayName,
  type DependencyLogLine,
  type DependencyProgressStatus,
} from '@/utils/workspace-dependency'

// One streamed dependency operation at a time, owned by the panel that
// started it. The composable holds the live log and outcome so the progress
// dialog can be closed ("run in background") and reopened from the row without
// losing a line, and it keeps the HTTP stream alive across dialog visibility.
// There is deliberately no cancel: aborting the stream would not stop the
// script inside the workspace, so the only abort is the component unmounting.

export interface DependencyOperationState {
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

// Keeps a runaway script (npm's progress spew) from growing the reactive log
// without bound; the head is dropped, the tail is what the user reads anyway.
const MAX_LOG_LINES = 2000

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

function progressTitleKey(action: DependencyOperationAction): string {
  switch (action) {
    case 'remove':
      return 'bots.dependencies.progress.removing'
    case 'update':
      return 'bots.dependencies.progress.updating'
    case 'reinstall':
      return 'bots.dependencies.progress.reinstalling'
    default:
      return 'bots.dependencies.progress.installing'
  }
}

export function useDependencyOperation(botId: Ref<string>, targetId: Ref<string>) {
  const { t } = useI18n()
  const queryCache = useQueryCache()

  const active = shallowRef<DependencyOperationState | null>(null)
  const progressOpen = shallowRef(false)
  let controller: AbortController | null = null
  let lineSequence = 0

  const running = computed(() => active.value?.status === 'running')
  const title = computed(() => {
    const state = active.value
    if (!state) return ''
    return t(progressTitleKey(state.action), { name: dependencyDisplayName(state.item) })
  })

  /** True for the row whose stream this client holds — it alone can show the log. */
  function ownsStream(depId: string | undefined): boolean {
    return running.value && !!depId && active.value?.item.id === depId
  }

  // The list refetches only when the stream ends, so the row would keep saying
  // "installed" for the whole download; patching the cached status makes the
  // badge spin immediately, and every target of the bot is invalidated after.
  function patchCachedStatus(state: DependencyOperationState, status: DependencyStatus) {
    const key = botDependenciesQueryKey(state.botId, state.targetId)
    const current = queryCache.getQueryData<DependencyListResponse>(key)
    if (!current?.items) return
    queryCache.setQueryData<DependencyListResponse>(key, {
      ...current,
      items: current.items.map(entry => (entry.id === state.item.id ? { ...entry, status } : entry)),
    })
  }

  function pushLine(state: DependencyOperationState, stream: DependencyLogLine['stream'], data: string) {
    state.lines.push({ id: ++lineSequence, stream, data })
    if (state.lines.length > MAX_LOG_LINES) {
      state.lines.splice(0, state.lines.length - MAX_LOG_LINES)
    }
  }

  async function consume(state: DependencyOperationState, signal: AbortSignal) {
    try {
      const stream = streamDependencyOperation(
        state.botId,
        state.item.id ?? '',
        state.action,
        state.targetId || undefined,
        { version: state.version, signal },
      )
      for await (const event of stream) {
        if (signal.aborted) return
        switch (event.type) {
          case 'log':
            pushLine(state, event.stream, event.data)
            break
          case 'done':
            state.status = 'done'
            state.resultVersion = event.version ?? ''
            state.entrypoint = Object.values(event.entrypoints ?? {})[0] ?? ''
            break
          case 'error':
            state.status = 'error'
            state.error = event.message
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
      if (state.status === 'running') {
        state.status = 'error'
        state.error = t('bots.dependencies.progress.failedTitle')
      }
    } catch (error) {
      if (signal.aborted) return
      state.status = 'error'
      state.error = resolveApiErrorMessage(error, t('bots.dependencies.progress.failedTitle'))
    } finally {
      if (!signal.aborted) {
        void invalidateBotDependencies(queryCache, state.botId)
        if (state.item.category === 'agent') {
          void queryCache.invalidateQueries({ key: ['bot-agents', state.botId] })
        }
        // Sent to the background, the dialog is not there to show the verdict;
        // a toast is the one place the outcome can still land.
        if (!progressOpen.value && active.value === state) {
          if (state.status === 'done') {
            toast.success(t('bots.dependencies.backgroundDone', { name: dependencyDisplayName(state.item) }))
          } else {
            toast.error(state.error)
          }
          active.value = null
        }
      }
    }
  }

  /**
   * Starts one operation and opens the progress dialog. Refused while another
   * stream is running (the Server also rejects with `workspace_dependency.busy`).
   */
  function start(item: DependencyItem, action: DependencyOperationAction, options: { version?: string } = {}): boolean {
    if (running.value || !botId.value || !item.id) return false
    controller?.abort()
    controller = new AbortController()

    const state = reactive<DependencyOperationState>({
      botId: botId.value,
      targetId: targetId.value,
      item,
      action,
      version: options.version?.trim() ?? '',
      status: 'running',
      lines: [],
      error: '',
      resultVersion: '',
      entrypoint: '',
    })
    active.value = state
    progressOpen.value = true
    patchCachedStatus(state, optimisticStatus(action))
    void consume(state, controller.signal)
    return true
  }

  /** Replays the failed operation from the progress dialog's Retry. */
  function retry(): boolean {
    const state = active.value
    if (!state || state.status !== 'error') return false
    return start(state.item, state.action, { version: state.version })
  }

  function viewProgress() {
    if (active.value) progressOpen.value = true
  }

  // Closing a finished dialog forgets the operation; closing a running one only
  // hides it (the dialog's "run in background") and the row keeps "View progress".
  function setProgressOpen(open: boolean) {
    progressOpen.value = open
    if (!open && active.value && active.value.status !== 'running') {
      active.value = null
    }
  }

  onBeforeUnmount(() => {
    controller?.abort()
    controller = null
  })

  return {
    active,
    progressOpen,
    running,
    title,
    ownsStream,
    start,
    retry,
    viewProgress,
    setProgressOpen,
  }
}
