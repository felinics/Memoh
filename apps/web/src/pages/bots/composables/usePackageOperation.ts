import { computed, onBeforeUnmount, onDeactivated, shallowRef, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from '@felinic/ui'
import { usePackageOperationsStore, type PackageOperation, type StartPackageOperationInput } from '@/store/package-operations'

// A surface's view onto the shared Package operation store: which operation
// its progress dialog shows and whether it is showing it. The stream lives in
// the store, so closing the dialog ("run in background"), switching tabs or
// leaving the page never interrupts it.

let viewerSequence = 0

export function usePackageOperation(botId: Ref<string>, viewerName = 'packages') {
  const { t } = useI18n()
  const store = usePackageOperationsStore()
  const viewerId = `${viewerName}:${++viewerSequence}`

  const active = shallowRef<PackageOperation | null>(null)
  const progressOpen = shallowRef(false)

  const running = computed(() => !!store.runningFor(botId.value))

  /** True for the Package whose stream this client holds. */
  function ownsStream(registryId: string | undefined, packageId: string | undefined): boolean {
    return store.get(botId.value, registryId, packageId)?.status === 'running'
  }

  function show(operation: PackageOperation) {
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
   * Starts one operation and opens the progress dialog. The same Package
   * already streaming just reopens its log; another Package streaming for
   * this bot is refused.
   */
  function start(input: Omit<StartPackageOperationInput, 'botId'>): boolean {
    const result = store.start({ ...input, botId: botId.value })
    switch (result.kind) {
      case 'started':
      case 'running':
        show(result.operation)
        return true
      case 'busy':
        toast.error(t('packages.busy'))
        return false
      default:
        return false
    }
  }

  function retry(): boolean {
    const operation = active.value
    if (!operation) return false
    return store.retry(operation.key)
  }

  function viewProgress(registryId: string | undefined, packageId: string | undefined) {
    const operation = store.get(botId.value, registryId, packageId)
    if (operation) show(operation)
  }

  function setProgressOpen(open: boolean) {
    if (open) {
      if (active.value) show(active.value)
      return
    }
    hide()
  }

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
