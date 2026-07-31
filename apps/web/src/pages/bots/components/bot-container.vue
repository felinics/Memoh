<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { CalloutBanner, ConfirmPopover, InlineLoadingRow, MetricReadout, PageShell, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { useQuery } from '@pinia/colada'
import { Play, AlertCircle, ChevronRight } from 'lucide-vue-next'
import {
  deleteBotsByBotIdContainer,
  getBotsByBotIdContainer,
  getBotsByBotIdContainerMetrics,
  getBotsByBotIdContainerSnapshots,
  getBotsById,
  postBotsByBotIdContainerDataRestore,
  postBotsByBotIdContainerSnapshots,
  postBotsByBotIdContainerSnapshotsRollback,
  postBotsByBotIdContainerStart,
  postBotsByBotIdContainerStop,
  putBotsByBotIdContainerMetrics,
  type HandlersCreateContainerRequest,
  type HandlersGetContainerMetricsResponse,
  type HandlersGetContainerResponse,
  type HandlersUpdateContainerMetricsRequest,
  type HandlersListSnapshotsResponse,
} from '@memohai/sdk'
import {
  postBotsByBotIdContainerStream,
  type ContainerCreateLayerStatus,
  type ContainerCreateStreamEvent,
} from '@/composables/api/useContainerStream'
import {
  Button,
  Input,
  Label,
  Badge,
  Spinner,
  Switch,
  Textarea,
  Dialog,
  DialogContent,
  DialogScrollContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
  DialogClose,
} from '@felinic/ui'
import ContainerCreateProgress from './container-create-progress.vue'
import { useSyncedQueryParam } from '@/composables/useSyncedQueryParam'
import { useBotStatusMeta } from '@/composables/useBotStatusMeta'
import { useCapabilitiesStore } from '@/store/capabilities'
import { formatDateTime, formatRelativeTime } from '@/utils/date-time'
import { shortenImageRef } from '@/utils/image-ref'
import { formatMetricBytes, formatMetricPercent } from '@/utils/format-bytes'
import { resolveApiErrorMessage } from '@/utils/api-error'

const route = useRoute()
const { t, locale } = useI18n()

type ContainerAction =
  | 'create'
  | 'start'
  | 'stop'
  | 'delete'
  | 'delete-preserve'
  | 'snapshot'
  | 'restore'
  | 'rollback'
  | 'recreate'
  | ''

const containerLoading = ref(false)
const containerAction = ref<ContainerAction>('')
const rollbackVersion = ref<number | null>(null)
const createRestoreData = ref(false)
const createImage = ref('')
const createImagePrefilled = ref(false)
const createGPUEnabled = ref(false)
const createGPUDevices = ref('')
const createGPUPrefilled = ref(false)
const newSnapshotName = ref('')

// Each deep operation gets its own focused dialog. The root surface only answers
// "is this workspace running and how much is it using" — the inputs, snapshot
// list, diagnostics, and destructive choices live behind a named entry point so
// the 99% who just glance at status never carry their visual weight, while the
// 1% who arrive with a purpose find the button and open the form.
const createDialogOpen = ref(false)
const limitsDialogOpen = ref(false)
const snapshotsDialogOpen = ref(false)
const detailsDialogOpen = ref(false)
const stopDialogOpen = ref(false)
const deleteDialogOpen = ref(false)
// Default to the safe side: keep a recoverable copy of the data on delete.
const deleteKeepData = ref(true)
// Create dialog's secondary options (restore / GPU), collapsed by default.
const createMoreOptions = ref(false)

interface CreateProgress {
  phase: 'preserving' | 'pulling' | 'creating' | 'restoring' | 'complete' | 'error'
  layers?: ContainerCreateLayerStatus[]
  image?: string
  error?: string
}
const createProgress = ref<CreateProgress | null>(null)

const createProgressPercent = computed(() => {
  const layers = createProgress.value?.layers
  if (!layers || layers.length === 0) return 0
  let totalOffset = 0
  let totalSize = 0
  for (const l of layers) {
    totalOffset += l.offset
    totalSize += l.total
  }
  return totalSize > 0 ? Math.round((totalOffset / totalSize) * 100) : 0
})

const capabilitiesStore = useCapabilitiesStore()
const routeIdentifier = computed(() => route.params.botName as string)
const botId = computed(() => bot.value?.id ?? '')
const containerBusy = computed(() => containerLoading.value || containerAction.value !== '')

type BotContainerInfo = HandlersGetContainerResponse
type BotContainerMetrics = HandlersGetContainerMetricsResponse
type BotContainerResourceLimits = NonNullable<HandlersGetContainerMetricsResponse['resource_limits']>
type BotContainerSnapshot = HandlersListSnapshotsResponse extends { snapshots?: (infer T)[] } ? T : never

const bytesPerGiB = 1024 * 1024 * 1024

const containerInfo = ref<BotContainerInfo | null>(null)
const containerMetrics = ref<BotContainerMetrics | null>(null)
const resourceLimits = computed(() => containerMetrics.value?.resource_limits ?? null)
const containerMissing = ref(false)
const snapshots = ref<BotContainerSnapshot[]>([])
const metricsLoading = ref(false)
const resourceLimitsLoading = computed(() => metricsLoading.value)
const resourceLimitsSaving = ref(false)
const resourceLimitApplyPromptVisible = ref(false)
const snapshotsLoading = ref(false)
const cpuLimitCores = ref('')
const memoryLimitGiB = ref('')
const storageLimitGiB = ref('')

function resolveErrorMessage(error: unknown, fallback: string): string {
  return resolveApiErrorMessage(error, fallback)
}

async function runContainerAction<T>(
  action: ContainerAction,
  operation: () => Promise<T>,
  successMessage?: string | ((result: T) => string),
) {
  containerAction.value = action
  try {
    const result = await operation()
    const message = typeof successMessage === 'function'
      ? successMessage(result)
      : successMessage
    if (message) {
      toast.success(message)
    }
    return result
  } catch (error) {
    toast.error(resolveErrorMessage(error, t('bots.container.actionFailed')))
    return undefined
  } finally {
    containerAction.value = ''
  }
}

async function loadContainerData(showLoadingToast: boolean) {
  await capabilitiesStore.load()
  containerLoading.value = true
  try {
    const result = await getBotsByBotIdContainer({ path: { bot_id: botId.value } })
    if (result.error !== undefined) {
      if (result.response?.status === 404) {
        containerInfo.value = null
        containerMetrics.value = null
        containerMissing.value = true
        snapshots.value = []
        await loadContainerMetrics(showLoadingToast)
        return
      }
      throw result.error
    }

    containerInfo.value = result.data
    containerMissing.value = false

    const metricsPromise = loadContainerMetrics(showLoadingToast)

    if (capabilitiesStore.snapshotSupported) {
      await Promise.all([metricsPromise, loadSnapshots(true)])
    } else {
      snapshots.value = []
      await metricsPromise
    }
  } catch (error) {
    if (showLoadingToast) {
      toast.error(resolveErrorMessage(error, t('bots.container.loadFailed')))
    }
  } finally {
    containerLoading.value = false
  }
}

async function loadContainerMetrics(showLoadingToast: boolean) {
  if (!botId.value) return

  metricsLoading.value = true
  try {
    const { data } = await getBotsByBotIdContainerMetrics({
      path: { bot_id: botId.value },
      throwOnError: true,
    })
    containerMetrics.value = data
    applyResourceLimitForm(data.resource_limits ?? null)
    resourceLimitApplyPromptVisible.value = !!data.resource_limits?.requires_recreate && !!containerInfo.value
  } catch (error) {
    containerMetrics.value = null
    resourceLimitApplyPromptVisible.value = false
    if (showLoadingToast) {
      toast.error(resolveErrorMessage(error, t('bots.container.metricsLoadFailed')))
    }
  } finally {
    metricsLoading.value = false
  }
}

// `silent` is used on the initial/background page load: snapshots are secondary
// data behind a dialog, so a failure there must not raise a page-level error —
// otherwise a stale-but-rendered workspace whose live container is gone greets
// the visitor with a contradictory "container not found" toast. Explicit actions
// (create snapshot) still surface their reload failures.
async function loadSnapshots(silent = false) {
  if (!containerInfo.value || !capabilitiesStore.snapshotSupported) {
    snapshots.value = []
    return
  }

  snapshotsLoading.value = true
  try {
    const { data } = await getBotsByBotIdContainerSnapshots({
      path: { bot_id: botId.value },
      throwOnError: true,
    })
    snapshots.value = data.snapshots ?? []
  } catch (error) {
    snapshots.value = []
    if (!silent) {
      toast.error(resolveErrorMessage(error, t('bots.container.snapshotLoadFailed')))
    }
  } finally {
    snapshotsLoading.value = false
  }
}

// Silent background sample: refresh only the live metrics (the values that
// actually move), without touching loading flags — so it never disables the
// action buttons — and without reseeding the limits form or clobbering the last
// good sample on a transient error. This is what replaces the manual refresh.
async function refreshContainerMetricsSilently() {
  if (!botId.value || !containerInfo.value) return
  try {
    const { data } = await getBotsByBotIdContainerMetrics({
      path: { bot_id: botId.value },
      throwOnError: true,
    })
    containerMetrics.value = data
    resourceLimitApplyPromptVisible.value = !!data.resource_limits?.requires_recreate
  } catch {
    // Background poll stays quiet; keep the last good reading on screen.
  }
}

const { data: bot, refetch: refetchBot } = useQuery({
  key: () => ['bot', routeIdentifier.value],
  query: async () => {
    const { data } = await getBotsById({ path: { id: routeIdentifier.value }, throwOnError: true })
    return data
  },
  enabled: () => !!routeIdentifier.value,
})

function rememberedWorkspaceImage(metadata: Record<string, unknown> | undefined): string {
  const workspace = metadata?.workspace
  if (!workspace || typeof workspace !== 'object' || Array.isArray(workspace)) return ''
  const image = (workspace as Record<string, unknown>).image
  return typeof image === 'string' ? shortenImageRef(image) : ''
}

type RememberedWorkspaceGPU = {
  exists: boolean
  devices: string[]
}

function rememberedWorkspaceGPU(metadata: Record<string, unknown> | undefined): RememberedWorkspaceGPU {
  const workspace = metadata?.workspace
  if (!workspace || typeof workspace !== 'object' || Array.isArray(workspace)) {
    return { exists: false, devices: [] }
  }

  const workspaceRecord = workspace as Record<string, unknown>
  if (!Object.prototype.hasOwnProperty.call(workspaceRecord, 'gpu')) {
    return { exists: false, devices: [] }
  }

  const gpu = workspaceRecord.gpu
  if (!gpu || typeof gpu !== 'object' || Array.isArray(gpu)) {
    return { exists: true, devices: [] }
  }

  const rawDevices = (gpu as Record<string, unknown>).devices
  const devices = Array.isArray(rawDevices)
    ? rawDevices.filter((value): value is string => typeof value === 'string').map(value => value.trim()).filter(Boolean)
    : []

  return { exists: true, devices: [...new Set(devices)] }
}

function parseCDIDevices(value: string): string[] {
  return [...new Set(
    value
      .split(/[\n,]/)
      .map(item => item.trim())
      .filter(Boolean),
  )]
}

const rememberedCreateImage = computed(() => rememberedWorkspaceImage(bot.value?.metadata as Record<string, unknown> | undefined))
const rememberedCreateGPU = computed(() => rememberedWorkspaceGPU(bot.value?.metadata as Record<string, unknown> | undefined))
const displayedContainerImage = computed(() => shortenImageRef(containerInfo.value?.image))
const displayedCDIDevices = computed(() => containerInfo.value?.cdi_devices ?? [])

const { isPending: botLifecyclePending } = useBotStatusMeta(bot, t)

// Runtime status as a Badge variant (like bot-overview), not a loose pulse dot
const runtimeStatusKey = computed(() => {
  const status = (containerInfo.value?.status ?? '').trim().toLowerCase()
  if (status === 'running') return 'running'
  if (status === 'created') return 'created'
  if (status === 'stopped' || status === 'exited') return 'stopped'
  return 'unknown'
})

const runtimeStatusVariant = computed<'success' | 'secondary' | 'default'>(() => {
  switch (runtimeStatusKey.value) {
    case 'running': return 'success'
    case 'created': return 'default'
    case 'stopped': return 'secondary'
    default: return 'secondary'
  }
})

const runtimeStatusLabel = computed(() => {
  switch (runtimeStatusKey.value) {
    case 'running': return t('bots.container.statusRunning')
    case 'created': return t('bots.container.statusCreated')
    case 'stopped': return t('bots.container.statusStopped')
    default: return t('bots.container.statusUnknown')
  }
})

const isContainerTaskRunning = computed(() => {
  const info = containerInfo.value
  if (!info) return false
  if (info.task_running) return true
  const status = (info.status ?? '').trim().toLowerCase()
  if (status === 'stopped' || status === 'exited') return false
  return false
})

const hasPreservedData = computed(() => !!containerInfo.value?.has_preserved_data)
const isLegacy = computed(() => !!containerInfo.value?.legacy)

function applyCreateContainerEvent(event: ContainerCreateStreamEvent): boolean {
  switch (event.type) {
    case 'pulling':
      createProgress.value = { phase: 'pulling', image: event.image }
      return false
    case 'pull_progress':
      createProgress.value = {
        phase: 'pulling',
        image: createProgress.value?.image,
        layers: event.layers,
      }
      return false
    case 'pull_skipped':
    case 'pull_delegated':
      createProgress.value = { phase: 'pulling', image: event.image }
      return false
    case 'creating':
      createProgress.value = { phase: 'creating' }
      return false
    case 'restoring':
      createProgress.value = { phase: 'restoring' }
      return false
    case 'complete':
      return !!event.container.data_restored
    case 'error':
      createProgress.value = { phase: 'error', error: event.message }
      throw new Error(event.message || 'Unknown error')
  }
}

async function createContainerSSE(body: HandlersCreateContainerRequest): Promise<{ dataRestored: boolean }> {
  const { stream } = await postBotsByBotIdContainerStream({
    path: { bot_id: botId.value },
    body,
    throwOnError: true,
  })

  let dataRestored = false
  for await (const event of stream) {
    dataRestored = applyCreateContainerEvent(event) || dataRestored
  }

  return { dataRestored }
}

function openCreateDialog() {
  if (botLifecyclePending.value) return
  createMoreOptions.value = false
  createProgress.value = null
  createDialogOpen.value = true
}

async function handleCreateContainer() {
  if (botLifecyclePending.value) return

  containerAction.value = 'create'
  createProgress.value = { phase: 'pulling' }
  try {
    const gpuDevices = parseCDIDevices(createGPUDevices.value)
    if (createGPUEnabled.value && gpuDevices.length === 0) {
      throw new Error(t('bots.container.gpuDevicesRequired'))
    }

    const body: HandlersCreateContainerRequest = {
      restore_data: createRestoreData.value,
    }
    const trimmedImage = createImage.value.trim()
    if (trimmedImage) body.image = trimmedImage
    if (createGPUEnabled.value || rememberedCreateGPU.value.exists) {
      body.gpu = {
        devices: createGPUEnabled.value ? gpuDevices : [],
      }
    }

    const { dataRestored } = await createContainerSSE(body)
    createRestoreData.value = false
    createImage.value = ''
    createGPUEnabled.value = false
    createGPUDevices.value = ''
    createDialogOpen.value = false
    await loadContainerData(false)
    await refetchBot()
    toast.success(dataRestored
      ? t('bots.container.createRestoreSuccess')
      : t('bots.container.createSuccess'))
  }
  catch (error) {
    toast.error(resolveErrorMessage(error, t('bots.container.actionFailed')))
  }
  finally {
    containerAction.value = ''
    createProgress.value = null
  }
}

async function handleRecreateContainer(): Promise<boolean> {
  if (botLifecyclePending.value || !containerInfo.value) return false

  containerAction.value = 'recreate'
  try {
    createProgress.value = { phase: 'preserving' }
    await deleteBotsByBotIdContainer({
      path: { bot_id: botId.value },
      query: { preserve_data: true },
      throwOnError: true,
    })

    createProgress.value = { phase: 'pulling' }
    await createContainerSSE({ restore_data: true })
    await loadContainerData(false)
    toast.success(t('bots.container.legacyRecreateSuccess'))
    return true
  }
  catch (error) {
    toast.error(resolveErrorMessage(error, t('bots.container.actionFailed')))
    return false
  }
  finally {
    containerAction.value = ''
    createProgress.value = null
  }
}

function trimTrailingZeros(value: string): string {
  return value.replace(/(\.\d*?)0+$/, '$1').replace(/\.$/, '')
}

function limitInputFromMillicores(value?: number): string {
  if (!value || value <= 0) return ''
  return trimTrailingZeros((value / 1000).toFixed(3))
}

function limitInputFromBytes(value?: number): string {
  if (!value || value <= 0) return ''
  return trimTrailingZeros((value / bytesPerGiB).toFixed(2))
}

function applyResourceLimitForm(value: BotContainerResourceLimits | null) {
  const desired = value?.desired
  cpuLimitCores.value = limitInputFromMillicores(desired?.cpu_millicores)
  memoryLimitGiB.value = limitInputFromBytes(desired?.memory_bytes)
  storageLimitGiB.value = limitInputFromBytes(desired?.storage_bytes)
}

function parseLimitInput(value: string, fieldLabel: string): number {
  const trimmed = value.trim()
  if (!trimmed) return 0

  const parsed = Number(trimmed)
  if (!Number.isFinite(parsed) || parsed < 0) {
    throw new Error(t('bots.container.resourceLimits.invalidNumber', { field: fieldLabel }))
  }
  return parsed
}

function buildResourceLimitsPayload(): NonNullable<HandlersUpdateContainerMetricsRequest['resource_limits']> {
  const cpuCores = parseLimitInput(cpuLimitCores.value, t('bots.container.resourceLimits.cpuLabel'))
  const memoryGiB = parseLimitInput(memoryLimitGiB.value, t('bots.container.resourceLimits.memoryLabel'))
  const storageGiB = parseLimitInput(storageLimitGiB.value, t('bots.container.resourceLimits.storageLabel'))

  return {
    cpu_millicores: Math.round(cpuCores * 1000),
    memory_bytes: Math.round(memoryGiB * bytesPerGiB),
    storage_bytes: Math.round(storageGiB * bytesPerGiB),
  }
}

function openLimitsDialog() {
  applyResourceLimitForm(resourceLimits.value)
  limitsDialogOpen.value = true
}

async function handleSaveResourceLimits() {
  if (!botId.value || resourceLimitsSaving.value) return

  let resourceLimitBody: NonNullable<HandlersUpdateContainerMetricsRequest['resource_limits']>
  try {
    resourceLimitBody = buildResourceLimitsPayload()
  } catch (error) {
    toast.error(resolveErrorMessage(error, t('bots.container.resourceLimits.saveFailed')))
    return
  }

  resourceLimitsSaving.value = true
  try {
    const { data } = await putBotsByBotIdContainerMetrics({
      path: { bot_id: botId.value },
      body: { resource_limits: resourceLimitBody },
      throwOnError: true,
    })
    containerMetrics.value = data
    applyResourceLimitForm(data.resource_limits ?? null)
    resourceLimitApplyPromptVisible.value = !!data.resource_limits?.requires_recreate && !!containerInfo.value
    limitsDialogOpen.value = false
    toast.success(resourceLimitApplyPromptVisible.value
      ? t('bots.container.resourceLimits.saveRequiresRecreate')
      : t('bots.container.resourceLimits.saveSuccess'))
  } catch (error) {
    toast.error(resolveErrorMessage(error, t('bots.container.resourceLimits.saveFailed')))
  } finally {
    resourceLimitsSaving.value = false
  }
}

async function handleApplyResourceLimitsNow() {
  const applied = await handleRecreateContainer()
  if (applied) {
    resourceLimitApplyPromptVisible.value = false
    await loadContainerMetrics(false)
  }
}

function formatLimitCPU(value?: number): string {
  if (!value || value <= 0) return t('bots.container.resourceLimits.unlimited')
  return t('bots.container.resourceLimits.cpuCoresValue', {
    value: trimTrailingZeros((value / 1000).toFixed(3)),
  })
}

const storageHardLimitSupported = computed(() =>
  resourceLimits.value?.capabilities?.storage?.hard_limit_supported === true,
)
const storageSoftLimitExceeded = computed(() =>
  resourceLimits.value?.observed?.storage_over_soft_limit === true,
)

// Metrics display
const containerMetricsStatus = computed(() => containerMetrics.value?.status)
const cpuMetrics = computed(() => containerMetrics.value?.metrics?.cpu)
const memoryMetrics = computed(() => containerMetrics.value?.metrics?.memory)
const storageMetrics = computed(() => containerMetrics.value?.metrics?.storage)
const metricsBackendUnsupported = computed(() => containerMetrics.value?.supported === false)
const metricsTaskRunning = computed(() => containerMetricsStatus.value?.task_running)
const hasAnyMetric = computed(() =>
  !!cpuMetrics.value || !!memoryMetrics.value || !!storageMetrics.value,
)
const cpuMetricValueText = computed(() => formatMetricPercent(cpuMetrics.value?.usage_percent))
const memoryMetricValueText = computed(() => formatMetricBytes(memoryMetrics.value?.usage_bytes))
const storageMetricValueText = computed(() => formatMetricBytes(storageMetrics.value?.used_bytes))
// Like CPU, storage has no live ceiling in the payload, so the cap comes from the
// configured limit — never the mount path, which is diagnostic (it lives in Details)
// and renders as a bare "/" that says nothing about capacity.
const storageMetricHintText = computed(() => {
  if (!storageMetrics.value) return ''
  const limit = resourceLimits.value?.desired?.storage_bytes
  if (limit && limit > 0) {
    return `${formatMetricBytes(storageMetrics.value?.used_bytes)} / ${formatMetricBytes(limit)}`
  }
  return t('bots.container.metricsUnlimited')
})
const sampledAtText = computed(() =>
  formatRelativeTime(containerMetrics.value?.sampled_at, { locale: locale.value, fallback: '' }),
)
const memoryMetricHintText = computed(() => {
  const limit = memoryMetrics.value?.limit_bytes
  if (limit && limit > 0) {
    const usagePercent = formatMetricPercent(memoryMetrics.value?.usage_percent)
    return `${formatMetricBytes(memoryMetrics.value?.usage_bytes)} / ${formatMetricBytes(limit)}${usagePercent === '--' ? '' : ` (${usagePercent})`}`
  }
  if (memoryMetrics.value) {
    return t('bots.container.metricsUnlimited')
  }
  return t('bots.container.metricsUnavailable')
})

// CPU has no live cgroup ceiling in the metrics payload, so the cap comes from the
// configured limit. Mirrors Memory's tile so every gauge states its ceiling.
const cpuMetricHintText = computed(() => {
  if (!cpuMetrics.value) return ''
  const cpuLimit = resourceLimits.value?.desired?.cpu_millicores
  if (cpuLimit && cpuLimit > 0) return formatLimitCPU(cpuLimit)
  return t('bots.container.metricsUnlimited')
})

// Runtime metric tiles for the overview-style tile grid
const runtimeMetricCards = computed(() => [
  { key: 'cpu', label: t('bots.container.metricsLabels.cpu'), value: cpuMetricValueText.value, sub: cpuMetricHintText.value },
  { key: 'memory', label: t('bots.container.metricsLabels.memory'), value: memoryMetricValueText.value, sub: memoryMetricHintText.value },
  { key: 'storage', label: t('bots.container.metricsLabels.storage'), value: storageMetricValueText.value, sub: storageMetricHintText.value },
])

const runtimeMetricsNote = computed(() => {
  if (metricsBackendUnsupported.value) return t('bots.container.metricsUnsupported')
  if (metricsTaskRunning.value === false) return t('bots.container.metricsStopped')
  if (!hasAnyMetric.value) return t('bots.container.metricsUnavailable')
  return ''
})

const runtimeHasMetrics = computed(() => !metricsBackendUnsupported.value && hasAnyMetric.value)

async function handleStopContainer() {
  if (botLifecyclePending.value || !containerInfo.value) return

  await runContainerAction(
    'stop',
    async () => {
      await postBotsByBotIdContainerStop({ path: { bot_id: botId.value }, throwOnError: true })
      await loadContainerData(false)
    },
    t('bots.container.stopSuccess'),
  )
}

// Stopping kills whatever the runtime is mid-doing, so it goes through a confirm
// step. Starting is safe and stays one click.
async function confirmStopContainer() {
  stopDialogOpen.value = false
  await handleStopContainer()
}

async function handleStartContainer() {
  if (botLifecyclePending.value || !containerInfo.value) return

  await runContainerAction(
    'start',
    async () => {
      await postBotsByBotIdContainerStart({ path: { bot_id: botId.value }, throwOnError: true })
      await loadContainerData(false)
    },
    t('bots.container.startSuccess'),
  )
}

function openDeleteDialog() {
  if (botLifecyclePending.value || !containerInfo.value) return
  deleteKeepData.value = true
  deleteDialogOpen.value = true
}

async function handleDeleteContainer(preserveData: boolean) {
  if (botLifecyclePending.value || !containerInfo.value) return

  const action: ContainerAction = preserveData ? 'delete-preserve' : 'delete'
  const successMessage = preserveData
    ? t('bots.container.deletePreserveSuccess')
    : t('bots.container.deleteSuccess')
  const lastImage = shortenImageRef(containerInfo.value.image)

  await runContainerAction(
    action,
    async () => {
      await deleteBotsByBotIdContainer({
        path: { bot_id: botId.value },
        query: preserveData ? { preserve_data: true } : undefined,
        throwOnError: true,
      })
      containerInfo.value = null
      containerMetrics.value = null
      containerMissing.value = true
      snapshots.value = []
      createRestoreData.value = preserveData
      createImage.value = lastImage
      createImagePrefilled.value = !!lastImage
      await loadContainerMetrics(false)
    },
    successMessage,
  )
}

async function confirmDeleteContainer() {
  const keep = deleteKeepData.value
  deleteDialogOpen.value = false
  await handleDeleteContainer(keep)
}

async function handleRestorePreservedData() {
  if (botLifecyclePending.value || !containerInfo.value || !hasPreservedData.value) return

  await runContainerAction(
    'restore',
    async () => {
      await postBotsByBotIdContainerDataRestore({
        path: { bot_id: botId.value },
        throwOnError: true,
      })
      await loadContainerData(false)
    },
    t('bots.container.restoreSuccess'),
  )
}

const statusKeyMap: Record<string, string> = {
  created: 'statusCreated',
  running: 'statusRunning',
  stopped: 'statusStopped',
  exited: 'statusExited',
}

const containerStatusText = computed(() => {
  const status = (containerInfo.value?.status ?? '').trim().toLowerCase()
  const key = statusKeyMap[status] ?? 'statusUnknown'
  return t(`bots.container.${key}`)
})

const containerTaskText = computed(() => {
  const info = containerInfo.value
  if (!info) return '-'

  const status = (info.status ?? '').trim().toLowerCase()
  if (status === 'exited') return t('bots.container.taskCompleted')
  return info.task_running ? t('bots.container.taskRunning') : t('bots.container.taskStopped')
})

function formatDate(value: string | undefined): string {
  return formatDateTime(value, { fallback: '-' })
}

function snapshotCreatedAt(value: BotContainerSnapshot) {
  const timestamp = Date.parse(value.created_at ?? '')
  return Number.isNaN(timestamp) ? Number.NEGATIVE_INFINITY : timestamp
}

function snapshotDisplayName(value: BotContainerSnapshot) {
  return (value.display_name ?? value.name ?? value.runtime_snapshot_name ?? '').trim() || '-'
}

function snapshotSourceText(value: BotContainerSnapshot) {
  const source = (value.source ?? '').trim().toLowerCase()
  if (!source) return '-'

  const sourceKeyMap: Record<string, string> = {
    manual: 'sourceManual',
    pre_exec: 'sourcePreExec',
    rollback: 'sourceRollback',
  }
  const sourceKey = sourceKeyMap[source]
  return sourceKey ? t(`bots.container.${sourceKey}`) : source
}

function canRollbackSnapshot(value: BotContainerSnapshot) {
  return !!value.managed && typeof value.version === 'number' && value.version > 0
}

async function handleRollbackSnapshot(snapshot: BotContainerSnapshot) {
  if (
    botLifecyclePending.value
    || !containerInfo.value
    || !canRollbackSnapshot(snapshot)
    || snapshot.version === undefined
  ) {
    return
  }

  rollbackVersion.value = snapshot.version
  await runContainerAction(
    'rollback',
    async () => {
      await postBotsByBotIdContainerSnapshotsRollback({
        path: { bot_id: botId.value },
        body: { version: snapshot.version },
        throwOnError: true,
      })
      await loadContainerData(false)
    },
    t('bots.container.rollbackSuccess'),
  )
  rollbackVersion.value = null
}

async function handleCreateSnapshot() {
  if (botLifecyclePending.value || !containerInfo.value || !capabilitiesStore.snapshotSupported) return

  await runContainerAction(
    'snapshot',
    async () => {
      await postBotsByBotIdContainerSnapshots({
        path: { bot_id: botId.value },
        body: { snapshot_name: newSnapshotName.value.trim() },
        throwOnError: true,
      })
      newSnapshotName.value = ''
      await loadSnapshots()
    },
    t('bots.container.snapshotSuccess'),
  )
}

const sortedSnapshots = computed(() => {
  return [...snapshots.value].sort((left, right) => {
    const managedDiff = Number(!!right.managed) - Number(!!left.managed)
    if (managedDiff !== 0) return managedDiff

    const leftVersion = left.version ?? Number.NEGATIVE_INFINITY
    const rightVersion = right.version ?? Number.NEGATIVE_INFINITY
    if (leftVersion !== rightVersion) return rightVersion - leftVersion

    const createdDiff = snapshotCreatedAt(right) - snapshotCreatedAt(left)
    if (createdDiff !== 0) return createdDiff

    return snapshotDisplayName(left).localeCompare(snapshotDisplayName(right))
  })
})

// Only managed snapshots are user-facing restore points. The snapshotter also
// reports the base image's read-only layers (source "image_layer"); those are
// infrastructure, not something a user takes or rolls back to, so they never
// belong in a "restore" list — surfacing them was the noise that overran the dialog.
const displayedSnapshots = computed(() => sortedSnapshots.value.filter(item => item.managed))

// The Snapshots & restore entry exists whenever there is something to do:
// snapshots can be taken (backend support) or preserved data can be restored.
const showDataRow = computed(() =>
  capabilitiesStore.snapshotSupported || hasPreservedData.value,
)

const activeTab = useSyncedQueryParam('tab', 'overview')

watch(containerMissing, (missing) => {
  if (!missing) {
    createImagePrefilled.value = false
    createGPUPrefilled.value = false
  }
})

watch([containerMissing, rememberedCreateImage], ([missing, remembered]) => {
  if (!missing || createImagePrefilled.value) return
  if (!remembered || createImage.value.trim()) return
  createImage.value = remembered
  createImagePrefilled.value = true
}, { immediate: true })

watch([containerMissing, rememberedCreateGPU], ([missing, remembered]) => {
  if (!missing || createGPUPrefilled.value) return
  if (!remembered.exists) return
  if (createGPUEnabled.value || createGPUDevices.value.trim()) return
  createGPUEnabled.value = remembered.devices.length > 0
  createGPUDevices.value = remembered.devices.join('\n')
  createGPUPrefilled.value = true
}, { immediate: true })

// Keep usage fresh on its own. We poll the metrics endpoint on a steady cadence,
// but only when the user can actually see it (Workspace tab + page visible) and
// when nothing else is mid-flight, so the poll never fights an in-progress action.
const METRICS_POLL_INTERVAL_MS = 10_000
let metricsPollTimer: ReturnType<typeof setInterval> | null = null

function pageVisible() {
  return typeof document === 'undefined' || document.visibilityState === 'visible'
}

function canPollMetrics() {
  return (
    activeTab.value === 'container'
    && !!botId.value
    && !!containerInfo.value
    && !containerBusy.value
    && pageVisible()
  )
}

function stopMetricsPoll() {
  if (metricsPollTimer !== null) {
    clearInterval(metricsPollTimer)
    metricsPollTimer = null
  }
}

function startMetricsPoll() {
  stopMetricsPoll()
  metricsPollTimer = setInterval(() => {
    if (canPollMetrics()) void refreshContainerMetricsSilently()
  }, METRICS_POLL_INTERVAL_MS)
}

// Returning to the tab/window shouldn't wait a full interval for fresh numbers.
function handleVisibilityChange() {
  if (pageVisible() && canPollMetrics()) void refreshContainerMetricsSilently()
}

onMounted(() => document.addEventListener('visibilitychange', handleVisibilityChange))
onBeforeUnmount(() => {
  document.removeEventListener('visibilitychange', handleVisibilityChange)
  stopMetricsPoll()
})

watch([activeTab, botId], ([tab]) => {
  if (!botId.value) {
    stopMetricsPoll()
    return
  }
  if (tab === 'container') {
    void loadContainerData(true)
    startMetricsPoll()
  } else {
    stopMetricsPoll()
  }
}, { immediate: true })
</script>

<template>
  <PageShell
    :title="$t('bots.container.title')"
    :description="$t('bots.container.subtitle')"
    variant="tab"
  >
    <!-- No title-row toolbar: usage keeps itself fresh by polling while the tab is
         open and visible, so there is no manual "refresh" to push onto the user. -->

    <!-- Loading -->
    <InlineLoadingRow
      v-if="containerLoading && !containerInfo && !containerMissing"
      size="md"
      surface="tab"
    >
      {{ $t('common.loading') }}
    </InlineLoadingRow>

    <!-- ────────────── EMPTY STATE (no workspace) ────────────── -->
    <template v-else-if="containerMissing">
      <!-- Bot lifecycle pending -->
      <div
        v-if="botLifecyclePending"
        class="mb-8 flex items-center gap-3 rounded-[var(--radius-menu-shell)] border border-border bg-card px-4 py-3"
      >
        <AlertCircle class="size-4 shrink-0 text-muted-foreground" />
        <p class="text-sm text-muted-foreground">
          {{ $t('bots.container.botNotReady') }}
        </p>
      </div>

      <!-- The empty surface keeps the populated frame (solid card) and carries the
           single guiding action — creating is a deliberate step, so it opens a
           focused dialog rather than dumping a form onto the root. -->
      <SettingsSection>
        <!-- ui-allow-shape: empty-state block, not a data row — a centered
             (flex-col, py-12) title + description + single CTA that fills the card
             frame. A SettingsRow is a horizontal label↔control pair; this is a
             different relationship (a vertical empty state), so it stays hand-written. -->
        <div class="flex min-h-[3.75rem] flex-col items-center justify-center gap-4 px-4 py-12 text-center">
          <div>
            <p class="text-sm font-medium text-foreground">
              {{ $t('bots.container.refactored.emptyTitle') }}
            </p>
            <p class="mt-1 max-w-sm text-xs text-muted-foreground">
              {{ $t('bots.container.refactored.emptyDescription') }}
            </p>
          </div>
          <Button
            :disabled="containerBusy || botLifecyclePending"
            @click="openCreateDialog"
          >
            <Play class="size-4" />
            {{ $t('bots.container.refactored.createSubmit') }}
          </Button>
        </div>
      </SettingsSection>
    </template>

    <!-- ────────────── ACTIVE WORKSPACE ────────────── -->
    <template v-else-if="containerInfo">
      <div class="space-y-8">
        <!-- Issue banners: visible only when there IS a problem (a healthy
             workspace shows none of these). -->
        <div
          v-if="isLegacy || (createProgress && containerAction === 'recreate') || resourceLimitApplyPromptVisible || storageSoftLimitExceeded"
          class="space-y-3"
        >
          <!-- Legacy architecture -->
          <CalloutBanner
            v-if="isLegacy"
            tone="warning"
            :title="$t('bots.container.refactored.issueLegacyTitle')"
            :description="$t('bots.container.refactored.issueLegacyHint')"
          >
            <Button
              variant="outline"
              size="sm"
              class="shrink-0 self-start sm:self-auto"
              :disabled="containerBusy || botLifecyclePending"
              :loading="containerAction === 'recreate'"
              @click="handleRecreateContainer"
            >
              {{ $t('bots.container.legacyRecreate') }}
            </Button>
          </CalloutBanner>

          <!-- Recreate progress -->
          <ContainerCreateProgress
            v-if="createProgress && containerAction === 'recreate'"
            :phase="createProgress.phase"
            :percent="createProgressPercent"
            :error="createProgress.error"
          />

          <!-- Resource-limits pending-rebuild -->
          <CalloutBanner
            v-if="resourceLimitApplyPromptVisible"
            tone="warning"
            :title="$t('bots.container.refactored.issueRecreateTitle')"
            :description="$t('bots.container.refactored.issueRecreateHint')"
          >
            <Button
              variant="ghost"
              size="sm"
              :disabled="containerBusy || botLifecyclePending"
              @click="resourceLimitApplyPromptVisible = false"
            >
              {{ $t('bots.container.resourceLimits.saveForLater') }}
            </Button>
            <ConfirmPopover
              :title="$t('bots.container.resourceLimits.recreateConfirmTitle')"
              :message="$t('bots.container.resourceLimits.recreateConfirm')"
              :cancel-text="$t('common.cancel')"
              :confirm-text="$t('bots.container.resourceLimits.recreateNow')"
              :loading="containerAction === 'recreate'"
              @confirm="handleApplyResourceLimitsNow"
            >
              <template #trigger>
                <Button
                  variant="outline"
                  size="sm"
                  :disabled="containerBusy || botLifecyclePending"
                  :loading="containerAction === 'recreate'"
                >
                  {{ $t('bots.container.resourceLimits.recreateNow') }}
                </Button>
              </template>
            </ConfirmPopover>
          </CalloutBanner>

          <!-- Storage soft-limit exceeded: a title-only warning, no action -->
          <CalloutBanner
            v-if="storageSoftLimitExceeded"
            tone="warning"
            :title="$t('bots.container.resourceLimits.storageSoftExceeded')"
          />
        </div>

        <!-- ─── Container: status + usage, the one thing a glance comes for ─── -->
        <section class="space-y-2.5">
          <div class="flex min-h-7 items-center justify-between gap-4 px-2">
            <div class="flex min-w-0 items-center gap-2">
              <h2 class="text-label font-medium text-muted-foreground">
                {{ $t('bots.container.refactored.containerTitle') }}
              </h2>
              <Badge
                :variant="runtimeStatusVariant"
                size="sm"
              >
                {{ runtimeStatusLabel }}
              </Badge>
            </div>
            <span
              v-if="sampledAtText"
              class="shrink-0 text-caption tabular-nums text-muted-foreground"
            >
              {{ $t('bots.container.refactored.runtimeUpdated', { time: sampledAtText }) }}
            </span>
          </div>

          <!-- Three sibling tiles with breathing room — calmer than hairline-joined
               tiles for a three-value readout. Caller owns the grid; each cell is a
               framed MetricReadout. -->
          <div
            v-if="runtimeHasMetrics"
            class="grid grid-cols-3 gap-3"
          >
            <MetricReadout
              v-for="m in runtimeMetricCards"
              :key="m.key"
              :label="m.label"
              :value="m.value"
              :sub="m.sub"
            />
          </div>
          <!-- ui-allow-shape: a standalone note card (its own rounded border + bg-card
               surface, px-4 not the section's mx-4), shown when runtime metrics are
               unavailable. Not a section-child SettingsRow: it's a self-contained
               card, a different surface relationship. Row height only for rhythm. -->
          <div
            v-else
            class="flex min-h-[3.75rem] items-center rounded-[var(--radius-menu-shell)] border border-border bg-card px-4 py-3 text-sm text-muted-foreground"
          >
            {{ runtimeMetricsNote || $t('bots.container.refactored.runtimeUnavailable') }}
          </div>
        </section>

        <!-- ─── Manage: quiet entry points; each opens its own focused form ─── -->
        <SettingsSection :title="$t('bots.container.refactored.manageTitle')">
          <SettingsRow :label="$t('bots.container.resourceLimits.title')">
            <Button
              variant="outline"
              size="sm"
              :disabled="containerBusy || botLifecyclePending || resourceLimitsLoading"
              @click="openLimitsDialog"
            >
              {{ $t('common.edit') }}
            </Button>
          </SettingsRow>

          <SettingsRow
            v-if="showDataRow"
            :label="$t('bots.container.refactored.dataTitle')"
          >
            <Button
              variant="outline"
              size="sm"
              :disabled="containerBusy || botLifecyclePending"
              @click="snapshotsDialogOpen = true"
            >
              {{ $t('bots.container.refactored.manageAction') }}
            </Button>
          </SettingsRow>

          <SettingsRow :label="$t('bots.container.refactored.detailsTitle')">
            <Button
              variant="outline"
              size="sm"
              @click="detailsDialogOpen = true"
            >
              {{ $t('bots.container.refactored.viewAction') }}
            </Button>
          </SettingsRow>
        </SettingsSection>

        <!-- ─── Actions: things you do to the workspace itself ─── -->
        <SettingsSection :title="$t('bots.container.refactored.actionsTitle')">
          <SettingsRow
            :label="isContainerTaskRunning
              ? $t('bots.container.refactored.actionsStopTitle')
              : $t('bots.container.refactored.actionsStartTitle')"
          >
            <Button
              variant="outline"
              size="sm"
              :disabled="containerBusy || botLifecyclePending"
              :loading="containerAction === 'start' || containerAction === 'stop'"
              @click="isContainerTaskRunning ? (stopDialogOpen = true) : handleStartContainer()"
            >
              {{ isContainerTaskRunning ? $t('bots.container.actions.stop') : $t('bots.container.actions.start') }}
            </Button>
          </SettingsRow>

          <SettingsRow :label="$t('bots.container.refactored.dangerDeleteTitle')">
            <Button
              variant="destructive"
              size="sm"
              :disabled="containerBusy || botLifecyclePending"
              @click="openDeleteDialog"
            >
              {{ $t('bots.container.actions.delete') }}
            </Button>
          </SettingsRow>
        </SettingsSection>
      </div>
    </template>

    <!-- ════════════ Dialogs (mounted once, opened by the entry points) ════════════ -->

    <!-- Create workspace -->
    <Dialog v-model:open="createDialogOpen">
      <DialogScrollContent class="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{{ $t('bots.container.refactored.createDialogTitle') }}</DialogTitle>
        </DialogHeader>

        <form
          class="space-y-4"
          @submit.prevent="handleCreateContainer"
        >
          <!-- Base image -->
          <div class="space-y-1.5">
            <Label for="ws-image">
              {{ $t('bots.container.createImageLabel') }}
              <span class="ml-1 font-normal text-muted-foreground">({{ $t('common.optional') }})</span>
            </Label>
            <Input
              id="ws-image"
              v-model="createImage"
              placeholder="debian:bookworm-slim"
              :disabled="containerAction === 'create'"
            />
            <p class="text-xs text-muted-foreground">
              {{ $t('bots.container.createImageDescription') }}
            </p>
          </div>

          <!-- More options: restore + GPU, collapsed by default -->
          <div>
            <button
              type="button"
              class="flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
              @click="createMoreOptions = !createMoreOptions"
            >
              <ChevronRight
                class="size-3.5"
                :class="createMoreOptions ? 'rotate-90' : ''"
              />
              {{ $t('bots.schedule.moreOptions') }}
            </button>

            <div
              class="grid overflow-hidden transition-[grid-template-rows] duration-200 ease-out"
              :class="createMoreOptions ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]'"
            >
              <div class="min-h-0">
                <div class="mt-3 space-y-4">
                  <!-- Restore preserved data -->
                  <div class="flex items-center justify-between gap-4">
                    <div class="min-w-0">
                      <p class="text-sm font-medium text-foreground">
                        {{ $t('bots.container.refactored.createRestoreToggle') }}
                      </p>
                      <p class="mt-0.5 text-xs text-muted-foreground">
                        {{ $t('bots.container.createRestoreDataDescription') }}
                      </p>
                    </div>
                    <Switch
                      :model-value="createRestoreData"
                      :disabled="containerAction === 'create'"
                      @update:model-value="(value) => createRestoreData = !!value"
                    />
                  </div>

                  <!-- GPU -->
                  <div class="flex items-center justify-between gap-4">
                    <div class="min-w-0">
                      <p class="text-sm font-medium text-foreground">
                        {{ $t('bots.container.createGpuLabel') }}
                      </p>
                      <p class="mt-0.5 text-xs text-muted-foreground">
                        {{ $t('bots.container.createGpuDescription') }}
                      </p>
                    </div>
                    <Switch
                      :model-value="createGPUEnabled"
                      :disabled="containerAction === 'create'"
                      @update:model-value="(value) => createGPUEnabled = !!value"
                    />
                  </div>

                  <!-- GPU devices -->
                  <div
                    v-if="createGPUEnabled"
                    class="space-y-1.5"
                  >
                    <Label for="ws-gpu-devices">{{ $t('bots.container.createGpuDevicesLabel') }}</Label>
                    <Textarea
                      id="ws-gpu-devices"
                      v-model="createGPUDevices"
                      :placeholder="$t('bots.container.createGpuDevicesPlaceholder')"
                      :disabled="containerAction === 'create'"
                      class="min-h-20 font-mono text-xs"
                    />
                    <p class="text-xs text-muted-foreground">
                      {{ $t('bots.container.createGpuDevicesDescription') }}
                    </p>
                  </div>
                </div>
              </div>
            </div>
          </div>

          <!-- Create progress (replaces nothing; shown inline while creating) -->
          <ContainerCreateProgress
            v-if="createProgress && containerAction === 'create'"
            :phase="createProgress.phase"
            :percent="createProgressPercent"
            :error="createProgress.error"
          />

          <DialogFooter class="gap-2">
            <DialogClose as-child>
              <Button
                type="button"
                variant="ghost"
                :disabled="containerAction === 'create'"
              >
                {{ $t('common.cancel') }}
              </Button>
            </DialogClose>
            <Button
              type="submit"
              :disabled="botLifecyclePending"
              :loading="containerAction === 'create'"
            >
              {{ $t('bots.container.refactored.createSubmit') }}
            </Button>
          </DialogFooter>
        </form>
      </DialogScrollContent>
    </Dialog>

    <!-- Resource limits -->
    <Dialog v-model:open="limitsDialogOpen">
      <DialogContent class="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{{ $t('bots.container.resourceLimits.title') }}</DialogTitle>
        </DialogHeader>

        <form
          class="space-y-4"
          @submit.prevent="handleSaveResourceLimits"
        >
          <div class="space-y-1.5">
            <Label for="ws-cpu">{{ $t('bots.container.resourceLimits.cpuLabel') }}</Label>
            <Input
              id="ws-cpu"
              v-model="cpuLimitCores"
              inputmode="decimal"
              :placeholder="$t('bots.container.resourceLimits.unlimitedPlaceholder')"
              :disabled="resourceLimitsSaving"
            />
          </div>
          <div class="space-y-1.5">
            <Label for="ws-memory">{{ $t('bots.container.resourceLimits.memoryLabel') }}</Label>
            <Input
              id="ws-memory"
              v-model="memoryLimitGiB"
              inputmode="decimal"
              :placeholder="$t('bots.container.resourceLimits.unlimitedPlaceholder')"
              :disabled="resourceLimitsSaving"
            />
          </div>
          <div class="space-y-1.5">
            <Label for="ws-storage">{{ $t('bots.container.resourceLimits.storageLabel') }}</Label>
            <Input
              id="ws-storage"
              v-model="storageLimitGiB"
              inputmode="decimal"
              :placeholder="$t('bots.container.resourceLimits.unlimitedPlaceholder')"
              :disabled="resourceLimitsSaving"
            />
            <p
              v-if="!storageHardLimitSupported"
              class="text-xs text-muted-foreground"
            >
              {{ $t('bots.container.resourceLimits.storageHint') }}
            </p>
          </div>

          <DialogFooter class="gap-2">
            <DialogClose as-child>
              <Button
                type="button"
                variant="ghost"
                :disabled="resourceLimitsSaving"
              >
                {{ $t('common.cancel') }}
              </Button>
            </DialogClose>
            <Button
              type="submit"
              :disabled="botLifecyclePending"
              :loading="resourceLimitsSaving"
            >
              {{ $t('bots.container.resourceLimits.save') }}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>

    <!-- Snapshots & restore -->
    <Dialog v-model:open="snapshotsDialogOpen">
      <DialogScrollContent class="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{{ $t('bots.container.refactored.dataTitle') }}</DialogTitle>
        </DialogHeader>

        <div class="min-w-0 space-y-4">
          <!-- Restore preserved data -->
          <div
            v-if="hasPreservedData"
            class="flex items-center justify-between gap-4 rounded-[var(--radius-menu-shell)] border border-border bg-card px-4 py-3"
          >
            <div class="min-w-0">
              <p class="text-sm font-medium text-foreground">
                {{ $t('bots.container.preservedDataAvailable') }}
              </p>
              <p class="mt-0.5 text-xs text-muted-foreground">
                {{ $t('bots.container.dataSubtitle') }}
              </p>
            </div>
            <ConfirmPopover
              :message="$t('bots.container.restoreConfirm')"
              :cancel-text="$t('common.cancel')"
              :confirm-text="$t('common.confirm')"
              :loading="containerAction === 'restore'"
              @confirm="handleRestorePreservedData"
            >
              <template #trigger>
                <Button
                  variant="outline"
                  size="sm"
                  :disabled="containerBusy || botLifecyclePending"
                  :loading="containerAction === 'restore'"
                >
                  {{ $t('bots.container.actions.restoreData') }}
                </Button>
              </template>
            </ConfirmPopover>
          </div>

          <!-- Create snapshot -->
          <div
            v-if="capabilitiesStore.snapshotSupported"
            class="space-y-1.5"
          >
            <Label for="ws-snapshot-name">{{ $t('bots.container.refactored.snapshotNew') }}</Label>
            <div class="flex items-center gap-2">
              <Input
                id="ws-snapshot-name"
                v-model="newSnapshotName"
                :placeholder="$t('bots.container.snapshotNamePlaceholder')"
                :disabled="containerBusy || snapshotsLoading || botLifecyclePending"
                class="min-w-0 flex-1"
              />
              <Button
                :disabled="containerBusy || snapshotsLoading || botLifecyclePending"
                :loading="containerAction === 'snapshot'"
                @click="handleCreateSnapshot"
              >
                {{ $t('bots.container.actions.snapshot') }}
              </Button>
            </div>
          </div>

          <!-- Snapshot list -->
          <template v-if="capabilitiesStore.snapshotSupported">
            <div
              v-if="snapshotsLoading"
              class="flex min-h-[8rem] items-center justify-center gap-3 rounded-[var(--radius-menu-shell)] border border-border text-sm text-muted-foreground"
            >
              <Spinner class="size-4" />
              <span>{{ $t('common.loading') }}</span>
            </div>
            <div
              v-else-if="displayedSnapshots.length === 0"
              class="flex min-h-[8rem] items-center justify-center rounded-[var(--radius-menu-shell)] border border-border px-4 text-center text-sm text-muted-foreground"
            >
              {{ $t('bots.container.snapshotEmpty') }}
            </div>
            <div
              v-else
              class="overflow-hidden rounded-[var(--radius-menu-shell)] border border-border"
            >
              <!-- ui-allow-shape: a row INSIDE a self-contained bordered card
                   (px-4 within the card, not the section's mx-4). SettingsRow is a
                   direct child of a section surface; these snapshot rows live one
                   level in, inside their own rounded frame — a different container
                   relationship, so they stay hand-written. -->
              <div
                v-for="item in displayedSnapshots"
                :key="`${item.snapshotter}:${item.runtime_snapshot_name || item.name}`"
                class="flex min-h-[3.75rem] items-center justify-between gap-4 border-b border-border px-4 py-3 last:border-b-0"
              >
                <div class="min-w-0 flex-1">
                  <div class="truncate text-sm font-medium text-foreground">
                    {{ snapshotDisplayName(item) }}
                  </div>
                  <p class="mt-0.5 text-xs text-muted-foreground">
                    {{ snapshotSourceText(item) }} · {{ formatDate(item.created_at) }}
                  </p>
                </div>
                <ConfirmPopover
                  v-if="canRollbackSnapshot(item)"
                  :message="$t('bots.container.rollbackConfirm')"
                  :cancel-text="$t('common.cancel')"
                  :confirm-text="$t('common.confirm')"
                  :loading="containerAction === 'rollback' && rollbackVersion === item.version"
                  @confirm="handleRollbackSnapshot(item)"
                >
                  <template #trigger>
                    <Button
                      variant="ghost"
                      size="sm"
                      :disabled="containerBusy || botLifecyclePending"
                      :loading="containerAction === 'rollback' && rollbackVersion === item.version"
                    >
                      {{ $t('bots.container.actions.rollback') }}
                    </Button>
                  </template>
                </ConfirmPopover>
              </div>
            </div>
          </template>
        </div>
      </DialogScrollContent>
    </Dialog>

    <!-- Details (read-only diagnostics) -->
    <Dialog v-model:open="detailsDialogOpen">
      <DialogScrollContent class="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{{ $t('bots.container.refactored.detailsTitle') }}</DialogTitle>
        </DialogHeader>

        <div
          v-if="containerInfo"
          class="overflow-hidden rounded-[var(--radius-menu-shell)] border border-border"
        >
          <div class="mx-4 flex items-start justify-between gap-4 border-b border-border py-3 last:border-b-0">
            <span class="shrink-0 text-sm text-muted-foreground">{{ $t('bots.container.fields.image') }}</span>
            <span class="break-all text-right font-mono text-sm text-foreground">{{ displayedContainerImage }}</span>
          </div>
          <div class="mx-4 flex items-start justify-between gap-4 border-b border-border py-3 last:border-b-0">
            <span class="shrink-0 text-sm text-muted-foreground">{{ $t('bots.container.fields.status') }}</span>
            <span class="text-right text-sm text-foreground">{{ containerStatusText }}</span>
          </div>
          <div class="mx-4 flex items-start justify-between gap-4 border-b border-border py-3 last:border-b-0">
            <span class="shrink-0 text-sm text-muted-foreground">{{ $t('bots.container.fields.task') }}</span>
            <span class="text-right text-sm text-foreground">{{ containerTaskText }}</span>
          </div>
          <div class="mx-4 flex items-start justify-between gap-4 border-b border-border py-3 last:border-b-0">
            <span class="shrink-0 text-sm text-muted-foreground">{{ $t('bots.container.fields.id') }}</span>
            <span class="break-all text-right font-mono text-sm text-foreground">{{ containerInfo.container_id }}</span>
          </div>
          <div class="mx-4 flex items-start justify-between gap-4 border-b border-border py-3 last:border-b-0">
            <span class="shrink-0 text-sm text-muted-foreground">{{ $t('bots.container.fields.namespace') }}</span>
            <span class="break-all text-right font-mono text-sm text-foreground">{{ containerInfo.namespace }}</span>
          </div>
          <div class="mx-4 flex items-start justify-between gap-4 border-b border-border py-3 last:border-b-0">
            <span class="shrink-0 text-sm text-muted-foreground">{{ $t('bots.container.fields.containerPath') }}</span>
            <span class="break-all text-right font-mono text-sm text-foreground">{{ containerInfo.container_path }}</span>
          </div>
          <div class="mx-4 flex items-start justify-between gap-4 border-b border-border py-3 last:border-b-0">
            <span class="shrink-0 text-sm text-muted-foreground">{{ $t('bots.container.fields.cdiDevices') }}</span>
            <div class="min-w-0 text-right">
              <span
                v-if="displayedCDIDevices.length === 0"
                class="text-sm text-foreground"
              >
                {{ $t('bots.container.cdiDevicesEmpty') }}
              </span>
              <div
                v-for="device in displayedCDIDevices"
                v-else
                :key="device"
                class="break-all font-mono text-sm text-foreground"
              >
                {{ device }}
              </div>
            </div>
          </div>
          <div class="mx-4 flex items-start justify-between gap-4 border-b border-border py-3 last:border-b-0">
            <span class="shrink-0 text-sm text-muted-foreground">{{ $t('bots.container.fields.createdAt') }}</span>
            <span class="text-right text-sm text-foreground">{{ formatDate(containerInfo.created_at) }}</span>
          </div>
          <div class="mx-4 flex items-start justify-between gap-4 border-b border-border py-3 last:border-b-0">
            <span class="shrink-0 text-sm text-muted-foreground">{{ $t('bots.container.fields.updatedAt') }}</span>
            <span class="text-right text-sm text-foreground">{{ formatDate(containerInfo.updated_at) }}</span>
          </div>
        </div>

        <p
          v-if="displayedCDIDevices.length > 0"
          class="text-xs text-muted-foreground"
        >
          {{ $t('bots.container.gpuRecreateHint') }}
        </p>
      </DialogScrollContent>
    </Dialog>

    <!-- Stop workspace (confirm) -->
    <Dialog v-model:open="stopDialogOpen">
      <DialogContent class="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{{ $t('bots.container.refactored.stopDialogTitle') }}</DialogTitle>
        </DialogHeader>

        <p class="text-sm text-muted-foreground">
          {{ $t('bots.container.refactored.stopDialogBody') }}
        </p>

        <DialogFooter class="gap-2">
          <DialogClose as-child>
            <Button
              type="button"
              variant="ghost"
              :disabled="containerBusy"
            >
              {{ $t('common.cancel') }}
            </Button>
          </DialogClose>
          <Button
            :disabled="containerBusy || botLifecyclePending"
            :loading="containerAction === 'stop'"
            @click="confirmStopContainer"
          >
            {{ $t('bots.container.actions.stop') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <!-- Delete workspace -->
    <Dialog v-model:open="deleteDialogOpen">
      <DialogContent class="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{{ $t('bots.container.refactored.deleteDialogTitle') }}</DialogTitle>
        </DialogHeader>

        <div class="space-y-4">
          <p class="text-sm text-muted-foreground">
            {{ deleteKeepData
              ? $t('bots.container.refactored.deleteDialogBodyKeep')
              : $t('bots.container.refactored.deleteDialogBodyWipe') }}
          </p>
          <div class="flex items-center justify-between gap-4">
            <div class="min-w-0">
              <p class="text-sm font-medium text-foreground">
                {{ $t('bots.container.refactored.deleteKeepLabel') }}
              </p>
              <p class="mt-0.5 text-xs text-muted-foreground">
                {{ $t('bots.container.refactored.deleteKeepDesc') }}
              </p>
            </div>
            <Switch
              :model-value="deleteKeepData"
              :disabled="containerBusy"
              @update:model-value="(value) => deleteKeepData = !!value"
            />
          </div>
        </div>

        <DialogFooter class="gap-2">
          <DialogClose as-child>
            <Button
              type="button"
              variant="ghost"
              :disabled="containerBusy"
            >
              {{ $t('common.cancel') }}
            </Button>
          </DialogClose>
          <Button
            variant="destructive"
            :disabled="containerBusy || botLifecyclePending"
            :loading="containerAction === 'delete' || containerAction === 'delete-preserve'"
            @click="confirmDeleteContainer"
          >
            {{ $t('bots.container.actions.delete') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </PageShell>
</template>
