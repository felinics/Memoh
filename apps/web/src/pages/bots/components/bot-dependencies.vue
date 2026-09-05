<template>
  <PageShell
    variant="tab"
    :title="t('bots.dependencies.title')"
    :description="t('bots.dependencies.intro')"
  >
    <template #actions>
      <!-- One selector only once there is a choice: a bot with a single
           workspace target would otherwise carry a control with one option. -->
      <Select
        v-if="targets.length > 1"
        :model-value="displayTargetId"
        :disabled="running"
        @update:model-value="onTargetChange"
      >
        <SelectTrigger class="w-44">
          <SelectValue />
        </SelectTrigger>
        <SelectContent align="end">
          <SelectItem
            v-for="target in targets"
            :key="target.target_id"
            :value="target.target_id"
            :disabled="!workspaceTargetAvailable(target)"
          >
            {{ workspaceTargetName(target, t) }}
            <span
              v-if="!workspaceTargetAvailable(target)"
              class="text-caption text-muted-foreground"
            >
              {{ workspaceTargetStatusLabel(target, t) }}
            </span>
          </SelectItem>
        </SelectContent>
      </Select>

      <!-- "Check for updates", not "Refresh": the list keeps itself current;
           this button pays for the upstream version check the user asks for. -->
      <Button
        variant="outline"
        :loading="checking"
        :disabled="workspaceState !== 'running' || running"
        @click="checkUpdates"
      >
        <RefreshCw />
        {{ t('bots.dependencies.checkUpdates') }}
      </Button>
    </template>

    <div class="space-y-8">
      <!-- A healthy workspace says nothing; the banner exists only while the
           rows are read-only and names the one move that unlocks them. -->
      <CalloutBanner
        v-if="banner"
        tone="warning"
        :title="banner.title"
        :description="banner.description"
      >
        <Button
          v-if="banner.action === 'start'"
          size="sm"
          :loading="starting"
          @click="startWorkspace"
        >
          {{ t('bots.dependencies.workspace.start') }}
        </Button>
        <Button
          v-else-if="banner.action === 'container'"
          size="sm"
          variant="outline"
          @click="goToContainer"
        >
          {{ t('bots.dependencies.workspace.goToContainer') }}
        </Button>
      </CalloutBanner>

      <!-- The workspace is up but its probe script failed (an OOM-killed
           discovery, say): the Server answers from its records alone, with no
           actions, and names the cause here. Retry simply asks again. -->
      <CalloutBanner
        v-if="discoveryError"
        tone="warning"
        :title="t('bots.dependencies.workspace.discoveryErrorTitle')"
        :description="discoveryError"
      >
        <Button
          size="sm"
          variant="outline"
          :loading="retryingDiscovery"
          @click="retryDiscovery"
        >
          {{ t('bots.dependencies.workspace.discoveryErrorRetry') }}
        </Button>
      </CalloutBanner>

      <!-- Placeholders in the shape of the rows they become, so the list
           does not jump when the first answer lands. -->
      <SettingsSection v-if="loading">
        <SettingsRow
          v-for="n in 4"
          :key="n"
        >
          <template #leading>
            <Skeleton class="size-8 rounded-full" />
          </template>
          <template #content>
            <div class="space-y-2">
              <Skeleton class="h-4 w-32" />
              <Skeleton class="h-3 w-48" />
            </div>
          </template>
          <Skeleton class="h-5 w-16 rounded-full" />
        </SettingsRow>
      </SettingsSection>

      <SettingsSection v-else-if="loadFailed">
        <SettingsRow
          :label="t('bots.dependencies.loadFailed')"
          :description="resolveApiErrorMessage(error, t('common.loadFailed'))"
        >
          <Button
            variant="outline"
            size="sm"
            @click="refetch()"
          >
            {{ t('common.retry') }}
          </Button>
        </SettingsRow>
      </SettingsSection>

      <!-- Nothing installed yet: the same frame the populated list draws, with
           the one move that fills it. -->
      <SettingsSection v-else-if="rows.length === 0">
        <Empty class="py-12">
          <EmptyHeader>
            <EmptyTitle>{{ t('bots.dependencies.emptyTitle') }}</EmptyTitle>
          </EmptyHeader>
          <EmptyContent>
            <Button
              variant="outline"
              @click="goToSupermarket"
            >
              {{ t('bots.dependencies.emptyAction') }}
              <ArrowRight />
            </Button>
          </EmptyContent>
        </Empty>
      </SettingsSection>

      <!-- One flat list: an agent CLI, a runtime, and a tool are the same kind
           of thing to the user. Rows that need a hand come first. -->
      <SettingsSection v-else>
        <DependencyRow
          v-for="item in rows"
          :key="item.id"
          :item="item"
          :workspace-state="workspaceState"
          :busy="running"
          :owns-stream="ownsStream(item.id)"
          @primary="onPrimary(item, $event)"
          @menu="onMenu(item, $event)"
        />
        <template
          v-if="lastChecked"
          #footer
        >
          <span class="text-body text-muted-foreground">
            {{ t('bots.dependencies.lastChecked', { time: lastChecked }) }}
          </span>
        </template>
      </SettingsSection>
    </div>

    <DependencyConfirmDialog
      :open="confirm.open"
      :mode="confirm.mode"
      :item="confirm.item"
      :target-kind="targetKind"
      :target-name="targetName"
      @update:open="(value) => { confirm.open = value }"
      @confirm="onConfirmed"
      @view-script="openScriptFromConfirm"
    />

    <DependencyProgressDialog
      :open="progressOpen"
      :name="active ? dependencyName(active.item) : ''"
      :action="active?.action ?? 'install'"
      :lines="active?.lines ?? []"
      :status="active?.status ?? 'running'"
      :error="active?.error"
      :result-version="active?.resultVersion"
      :entrypoint="active?.entrypoint"
      @update:open="setProgressOpen"
      @retry="retry"
    />

    <DependencyScriptDialog
      :open="script.open"
      :script="script.data"
      :loading="script.loading"
      :error="script.error"
      :dependency-name="script.item ? dependencyName(script.item) : ''"
      :action="script.action"
      :actions="script.actions"
      @update:open="(value) => { script.open = value }"
      @update:action="switchScriptAction"
    />

    <ConfirmDeleteDialog
      :open="!!removeTarget"
      :title="t('bots.dependencies.remove.title', { name: removeTarget ? dependencyName(removeTarget) : '' })"
      :description="removeDescription"
      :cancel-label="t('common.cancel')"
      :confirm-label="t('bots.dependencies.action.remove')"
      @update:open="(value) => { if (!value) removeTarget = null }"
      @confirm="onRemoveConfirmed"
    />

    <DependencyRollbackDialog
      :open="!!rollbackTarget"
      :item="rollbackTarget"
      :loading="rollingBack"
      @update:open="(value) => { if (!value && !rollingBack) rollbackTarget = null }"
      @confirm="onRollbackConfirmed"
    />
  </PageShell>
</template>

<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, reactive, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
  Button,
  CalloutBanner,
  ConfirmDeleteDialog,
  Empty,
  EmptyContent,
  EmptyHeader,
  EmptyTitle,
  PageShell,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SettingsRow,
  SettingsSection,
  Skeleton,
  toast,
} from '@felinic/ui'
import { ArrowRight, RefreshCw } from 'lucide-vue-next'
import {
  getBotsByBotIdWorkspaceTargets,
  postBotsByBotIdContainerStart,
  type WorkspaceWorkspaceTarget,
} from '@memohai/sdk'
import DependencyConfirmDialog from './dependency-confirm-dialog.vue'
import DependencyProgressDialog from './dependency-progress-dialog.vue'
import DependencyRollbackDialog from './dependency-rollback-dialog.vue'
import DependencyRow from './dependency-row.vue'
import DependencyScriptDialog from './dependency-script-dialog.vue'
import { useDependencyOperation } from '../composables/useDependencyOperation'
import {
  checkDependencyUpdates,
  fetchDependencyScript,
  invalidateBotDependencies,
  rollbackDependency,
  useBotDependenciesQuery,
  type DependencyItem,
  type DependencyOperationAction,
  type DependencyWorkspaceState,
  type ScriptAction,
  type ScriptResponse,
} from '@/composables/api/useWorkspaceDependencies'
import { useDialogMutation } from '@/composables/useDialogMutation'
import { useWorkspaceDependencyText } from '@/composables/useWorkspaceDependencyText'
import { isApiErrorCode, resolveApiErrorMessage } from '@/utils/api-error'
import { formatRelativeTime } from '@/utils/date-time'
import {
  dependencyAllows,
  dependencyInProgress,
  dependencyIsInstalled,
  formatDependencyVersion,
  sortDependencies,
  type DependencyConfirmMode,
  type DependencyMenuAction,
  type DependencyPrimaryAction,
} from '@/utils/workspace-dependency'
import {
  workspaceTargetAvailable,
  workspaceTargetName,
  workspaceTargetStatusLabel,
} from '@/utils/workspace-target'

type ValidWorkspaceTarget = WorkspaceWorkspaceTarget & { target_id: string }

const props = defineProps<{ botId: string }>()

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const queryCache = useQueryCache()
const { run: runMutation } = useDialogMutation()
const { dependencyName } = useWorkspaceDependencyText()
const botIdRef = computed(() => props.botId) as Ref<string>

// ---- Workspace target -------------------------------------------------------

const { data: targetsResponse } = useQuery({
  key: () => ['bot-workspace-targets', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdWorkspaceTargets({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!props.botId,
})

const targets = computed<ValidWorkspaceTarget[]>(() => (
  (targetsResponse.value?.targets ?? []).filter(
    (target): target is ValidWorkspaceTarget => typeof target.target_id === 'string' && target.target_id.length > 0,
  )
))
const primaryTargetId = computed(() => targets.value.find(target => target.primary)?.target_id ?? targets.value[0]?.target_id ?? '')

// '' means "the bot's current target": the Server resolves it, and the
// detail-page badge queries the same key, so the two share one cache entry
// instead of fetching the primary target twice under different names.
const selectedTargetId = ref('')
const displayTargetId = computed(() => selectedTargetId.value || primaryTargetId.value)
const selectedTarget = computed(() => targets.value.find(target => target.target_id === displayTargetId.value))
const targetKind = computed<'native' | 'remote'>(() => (
  !selectedTarget.value || selectedTarget.value.kind === 'native' ? 'native' : 'remote'
))
const targetName = computed(() => (selectedTarget.value ? workspaceTargetName(selectedTarget.value, t) : ''))

function onTargetChange(value: unknown) {
  const next = typeof value === 'string' ? value : ''
  selectedTargetId.value = next === primaryTargetId.value ? '' : next
}

watch(() => props.botId, () => {
  selectedTargetId.value = ''
})

// ---- Dependency list --------------------------------------------------------

const { data, error, isLoading, refetch } = useBotDependenciesQuery(botIdRef, selectedTargetId)

const items = computed<DependencyItem[]>(() => data.value?.items ?? [])
// The tab lists what is in the workspace; the Supermarket lists what could be.
const rows = computed(() => sortDependencies(items.value.filter(dependencyIsInstalled), dependencyName))
// Skeleton until the first answer lands — including the moment before the
// bot id resolves and the query is still disabled, which is not "empty".
const loading = computed(() => (isLoading.value || !botIdRef.value) && !data.value && !error.value)

// A Server that answers the whole list with a 409 still tells us why; the
// banner then carries the same message the list body would have.
const workspaceState = computed<DependencyWorkspaceState | undefined>(() => {
  if (data.value?.workspace_state) return data.value.workspace_state
  if (isApiErrorCode(error.value, 'workspace_dependency.workspace_not_running')) return 'not_running'
  if (isApiErrorCode(error.value, 'workspace_dependency.workspace_missing')) return 'missing'
  if (isApiErrorCode(error.value, 'workspace_dependency.remote_offline')) return 'remote_offline'
  return undefined
})
const loadFailed = computed(() => !!error.value && !data.value && !workspaceState.value)

// `discovery_error` is newer than this SDK build: read it loosely until the
// generated types catch up. Present only when the probe inside the workspace
// failed and the list is the recorded state without `actions`.
const discoveryError = computed(() => (
  ((data.value as { discovery_error?: string } | undefined)?.discovery_error ?? '').trim()
))
const retryingDiscovery = ref(false)
async function retryDiscovery() {
  if (retryingDiscovery.value) return
  retryingDiscovery.value = true
  try {
    await refetch()
  } finally {
    retryingDiscovery.value = false
  }
}

const banner = computed(() => {
  switch (workspaceState.value) {
    case 'not_running':
      return {
        title: t('bots.dependencies.workspace.notRunningTitle'),
        description: t('bots.dependencies.workspace.notRunningDescription'),
        action: targetKind.value === 'native' ? 'start' : '',
      }
    case 'missing':
      return {
        title: t('bots.dependencies.workspace.missingTitle'),
        description: '',
        action: targetKind.value === 'native' ? 'container' : '',
      }
    case 'remote_offline':
      return {
        title: t('bots.dependencies.workspace.remoteOfflineTitle', { name: targetName.value }),
        description: t('bots.dependencies.workspace.remoteOfflineDescription'),
        action: '',
      }
    default:
      return null
  }
})

const lastChecked = computed(() => {
  const latest = rows.value
    .map(item => item.last_checked_at ?? '')
    .filter(Boolean)
    .sort()
    .at(-1)
  return latest ? formatRelativeTime(latest, { locale: locale.value }) : ''
})

// ---- Operations (streamed) --------------------------------------------------

const {
  active,
  progressOpen,
  running,
  ownsStream,
  start,
  retry,
  viewProgress,
  setProgressOpen,
} = useDependencyOperation(botIdRef, selectedTargetId)

const confirm = reactive<{
  open: boolean
  mode: DependencyConfirmMode
  item: DependencyItem | null
  operation: DependencyOperationAction
}>({ open: false, mode: 'install', item: null, operation: 'install' })

function openConfirm(item: DependencyItem, mode: DependencyConfirmMode, operation: DependencyOperationAction) {
  confirm.item = item
  confirm.mode = mode
  confirm.operation = operation
  confirm.open = true
}

function onConfirmed(version: string) {
  const item = confirm.item
  confirm.open = false
  if (item) start(item, confirm.operation, { version })
}

function onPrimary(item: DependencyItem, action: DependencyPrimaryAction) {
  switch (action.kind) {
    case 'viewProgress':
      viewProgress(item)
      return
    case 'retry':
      // The user already confirmed this operation once; a retry replays it.
      start(item, action.operation ?? 'install')
      return
    case 'update':
      // Reads as an update either way; the Server decides whether that is an
      // in-place update or an install laid over the image copy.
      openConfirm(item, 'update', action.operation ?? 'update')
      return
    default:
      // "Install" and the missing row's "Reinstall" both run the install
      // script (nothing is left to remove), so both read as an install.
      openConfirm(item, 'install', 'install')
  }
}

const removeTarget = ref<DependencyItem | null>(null)
const rollbackTarget = ref<DependencyItem | null>(null)
const rollingBack = ref(false)

// Removing an overlay uncovers the image copy; removing anything else leaves
// nothing behind. The dialog states the result and nothing more.
const removeDescription = computed(() => {
  const item = removeTarget.value
  if (!item) return ''
  const imageVersion = formatDependencyVersion(item.image_version)
  if (item.overlay && imageVersion) {
    return t('bots.dependencies.remove.overlayDescription', {
      installed: formatDependencyVersion(item.installed_version),
      image_version: imageVersion,
    })
  }
  return t('bots.dependencies.remove.description', { path: item.install_path ?? '' })
})

function onMenu(item: DependencyItem, action: DependencyMenuAction) {
  switch (action.kind) {
    case 'install':
      openConfirm(item, 'install', 'install')
      return
    case 'reinstall':
      openConfirm(item, 'reinstall', 'reinstall')
      return
    case 'rollback':
      rollbackTarget.value = item
      return
    case 'remove':
      removeTarget.value = item
      return
    default:
      void openScript(item, defaultScriptAction(item))
  }
}

function onRemoveConfirmed() {
  const item = removeTarget.value
  removeTarget.value = null
  if (item) start(item, 'remove')
}

async function onRollbackConfirmed() {
  const item = rollbackTarget.value
  if (!item?.id || rollingBack.value) return
  const to = formatDependencyVersion(item.previous_version)
  rollingBack.value = true
  try {
    await runMutation(() => rollbackDependency(props.botId, selectedTargetId.value, item.id ?? ''), {
      fallbackMessage: t('bots.dependencies.rollback.failed'),
      onSuccess: async () => {
        rollbackTarget.value = null
        toast.success(t('bots.dependencies.rollback.success', { to }))
        await invalidateBotDependencies(queryCache, props.botId)
        if (item.category === 'agent') void queryCache.invalidateQueries({ key: ['bot-agents', props.botId] })
      },
    })
  } finally {
    rollingBack.value = false
  }
}

// ---- Script preview ---------------------------------------------------------

const script = reactive<{
  open: boolean
  item: DependencyItem | null
  action: ScriptAction
  actions: ScriptAction[]
  data: ScriptResponse | null
  loading: boolean
  error: string
}>({ open: false, item: null, action: 'install', actions: [], data: null, loading: false, error: '' })
let scriptSequence = 0

// Which scripts make sense to read for this row: the ones the Server would
// run now, plus install (always previewable — it is what a fresh row gets).
function scriptActionsFor(item: DependencyItem): ScriptAction[] {
  const actions: ScriptAction[] = ['install']
  if (dependencyAllows(item, 'update')) actions.push('update')
  if (dependencyAllows(item, 'remove')) actions.push('remove')
  return actions
}

function defaultScriptAction(item: DependencyItem): ScriptAction {
  return dependencyAllows(item, 'update') ? 'update' : 'install'
}

async function openScript(item: DependencyItem, action: ScriptAction) {
  script.item = item
  script.actions = scriptActionsFor(item)
  script.open = true
  await loadScript(action)
}

async function loadScript(action: ScriptAction) {
  const item = script.item
  if (!item?.id) return
  const sequence = ++scriptSequence
  script.action = action
  script.loading = true
  script.error = ''
  script.data = null
  try {
    const response = await fetchDependencyScript(props.botId, selectedTargetId.value, item.id, action)
    if (sequence === scriptSequence) script.data = response
  } catch (err) {
    if (sequence === scriptSequence) script.error = resolveApiErrorMessage(err, t('common.loadFailed'))
  } finally {
    if (sequence === scriptSequence) script.loading = false
  }
}

function switchScriptAction(action: ScriptAction) {
  void loadScript(action)
}

// The confirm dialog's "View script" shows the script the confirmed button
// would run; a reinstall reads as the install script since nothing else is
// left to inspect once the current copy is gone.
function openScriptFromConfirm() {
  const item = confirm.item
  if (!item) return
  void openScript(item, confirm.operation === 'update' ? 'update' : 'install')
}

// ---- Manual refresh, workspace start & navigation ---------------------------

const checking = ref(false)
async function checkUpdates() {
  if (checking.value) return
  checking.value = true
  try {
    await runMutation(() => checkDependencyUpdates(props.botId, selectedTargetId.value), {
      fallbackMessage: t('bots.dependencies.checkUpdatesFailed'),
      onSuccess: async (refreshed) => {
        queryCache.setQueryData(['bot-dependencies', props.botId, selectedTargetId.value], refreshed)
        await invalidateBotDependencies(queryCache, props.botId)
        toast.success(t('bots.dependencies.checkUpdatesDone'))
      },
    })
  } finally {
    checking.value = false
  }
}

const starting = ref(false)
async function startWorkspace() {
  if (starting.value) return
  starting.value = true
  try {
    await runMutation(
      () => postBotsByBotIdContainerStart({ path: { bot_id: props.botId }, throwOnError: true }),
      {
        fallbackMessage: t('bots.container.startFailed'),
        onSuccess: async () => {
          await invalidateBotDependencies(queryCache, props.botId)
        },
      },
    )
  } finally {
    starting.value = false
  }
}

function goToContainer() {
  void router.replace({ query: { ...route.query, tab: 'container' } }).catch(() => {})
}

// The Supermarket's Dependencies tab lists the whole catalog; carrying the bot
// id preselects this bot in its install dialog.
function goToSupermarket() {
  void router.push({ name: 'supermarket', query: { tab: 'dependencies', botId: props.botId } }).catch(() => {})
}

// ---- Polling for operations this client does not own ------------------------

// An install started elsewhere (the chat's background install, another tab,
// the Supermarket) shows up as an in-progress row with no stream here; poll
// until it settles. KeepAlive wraps this tab, so the poll pauses on deactivate
// rather than unmount.
const POLL_MS = 5_000
let pollTimer: ReturnType<typeof setInterval> | null = null
let tabActive = true

const hasForeignProgress = computed(() => items.value.some(item => dependencyInProgress(item) && !ownsStream(item.id)))

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

function syncPolling() {
  const shouldPoll = tabActive && hasForeignProgress.value
  if (shouldPoll && !pollTimer) {
    pollTimer = setInterval(() => {
      if (typeof document === 'undefined' || document.visibilityState === 'visible') void refetch()
    }, POLL_MS)
  } else if (!shouldPoll) {
    stopPolling()
  }
}

watch(hasForeignProgress, syncPolling, { immediate: true })

onActivated(() => {
  tabActive = true
  syncPolling()
})

onDeactivated(() => {
  tabActive = false
  stopPolling()
})

onBeforeUnmount(stopPolling)
</script>
