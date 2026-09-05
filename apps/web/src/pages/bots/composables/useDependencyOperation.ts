import { computed, onBeforeUnmount, onDeactivated, shallowRef, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from '@felinic/ui'
import type { DependencyItem, DependencyOperationAction } from '@/composables/api/useWorkspaceDependencies'
import { useDependencyOperationsStore, type DependencyOperation } from '@/store/dependency-operations'

// The Dependencies panel's view onto the shared operation store: which
// operation its progress dialog shows, and whether it is showing it. The
// stream itself lives in the store, so "run in background" (closing the
// dialog, switching tabs, leaving the page) never interrupts it; the row keeps
// "View progress" while the store still holds the stream, and the outcome
// lands as a toast when no dialog is left to show it.

let viewerSequence = 0

export function useDependencyOperation(botId: Ref<string>, targetId: Ref<string>) {
  const { t } = useI18n()
  const store = useDependencyOperationsStore()
  const viewerId = `bot-dependencies:${++viewerSequence}`

  // The operation the dialog renders. Kept across close so the dialog fades
  // out with its content intact even after the store dropped the record.
  const active = shallowRef<DependencyOperation | null>(null)
  const progressOpen = shallowRef(false)

  const running = computed(() => !!store.runningFor(botId.value))

  /** True for the row whose stream this client holds — it alone can show the log. */
  function ownsStream(depId: string | undefined): boolean {
    return store.get(botId.value, depId)?.status === 'running'
  }

  function show(operation: DependencyOperation) {
    if (progressOpen.value && active.value && active.value.key !== operation.key) {
      store.unview(active.value.key, viewerId)
    }
    active.value = operation
    progressOpen.value = true
    store.view(operation.key, viewerId)
  }

  function hide() {
    if (!progressOpen.value) return
    progressOpen.value = false
    if (active.value) store.unview(active.value.key, viewerId)
  }

  /**
   * Starts one operation and opens the progress dialog. A dependency already
   * streaming just reopens its log; another dependency streaming for this bot
   * is refused (the Server would answer `workspace_dependency.busy`).
   */
  function start(item: DependencyItem, action: DependencyOperationAction, options: { version?: string } = {}): boolean {
    const result = store.start({
      botId: botId.value,
      targetId: targetId.value,
      item,
      action,
      version: options.version,
    })
    switch (result.kind) {
      case 'started':
      case 'running':
        show(result.operation)
        return true
      case 'busy':
        toast.error(t('bots.dependencies.busy'))
        return false
      default:
        return false
    }
  }

  /** Replays the failed operation from the progress dialog's Retry. */
  function retry(): boolean {
    const operation = active.value
    if (!operation) return false
    return store.retry(operation.key)
  }

  function viewProgress(item: DependencyItem) {
    const operation = store.get(botId.value, item.id)
    if (operation) show(operation)
  }

  // Closing only hides the dialog: a running operation keeps streaming in the
  // store (the row keeps "View progress"), a finished one is forgotten there.
  function setProgressOpen(open: boolean) {
    if (open) {
      if (active.value) show(active.value)
      return
    }
    hide()
  }

  // The tab is KeepAlive'd: a dialog left open while the user looks at another
  // tab would silently swallow the verdict, so deactivating counts as closing.
  onDeactivated(hide)
  onBeforeUnmount(hide)

  return {
    active,
    progressOpen,
    running,
    ownsStream,
    start,
    retry,
    viewProgress,
    setProgressOpen,
  }
}
