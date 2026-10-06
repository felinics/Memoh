<template>
  <div>
    <PageShell
      v-if="!selected"
      variant="tab"
      :title="t('apps.title')"
    >
      <template #actions>
        <Button
          variant="outline"
          :loading="checking"
          :disabled="running || !items.length"
          @click="checkUpdates"
        >
          <RefreshCw />
          {{ t('apps.checkUpdates') }}
        </Button>
        <Button @click="goToSupermarket">
          <Plus />
          {{ t('apps.browse') }}
        </Button>
      </template>

      <div class="space-y-8">
        <CalloutBanner
          v-if="data?.dependency_catalog_stale"
          :title="t('bots.dependencies.catalogStaleTitle')"
          :description="t('bots.dependencies.catalogStaleDescription')"
        >
          <Button
            variant="outline"
            size="sm"
            :loading="retrying"
            @click="retryDiscovery"
          >
            {{ t('common.retry') }}
          </Button>
        </CalloutBanner>

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

        <SettingsSection v-if="loading">
          <SettingsRow
            v-for="n in 3"
            :key="n"
          >
            <template #leading>
              <Skeleton class="size-9 rounded-md" />
            </template>
            <template #content>
              <div class="space-y-2">
                <Skeleton class="h-4 w-40" />
                <Skeleton class="h-3 w-56" />
              </div>
            </template>
            <Skeleton class="h-5 w-16 rounded-full" />
          </SettingsRow>
        </SettingsSection>

        <SettingsSection v-else-if="loadFailed">
          <SettingsRow
            :label="t('apps.loadFailed')"
            :description="resolveApiErrorMessage(error, t('common.loadFailed'))"
          >
            <Button
              variant="outline"
              size="sm"
              @click="refetchApps()"
            >
              {{ t('common.retry') }}
            </Button>
          </SettingsRow>
        </SettingsSection>

        <SettingsSection v-else-if="items.length === 0">
          <Empty class="py-12">
            <EmptyHeader>
              <EmptyTitle>{{ t('apps.emptyTitle') }}</EmptyTitle>
              <EmptyDescription>{{ t('apps.emptyDescription') }}</EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button
                variant="outline"
                @click="goToSupermarket"
              >
                {{ t('apps.emptyAction') }}
                <ArrowRight />
              </Button>
            </EmptyContent>
          </Empty>
        </SettingsSection>

        <div
          v-else
          class="grid grid-cols-1 gap-4 sm:grid-cols-2"
        >
          <BotAppCard
            v-for="item in items"
            :key="appKey(item)"
            :item="item"
            :workspace-state="workspaceState"
            :busy="running || dependencyRunning"
            :owns-stream="ownsAppStream(item.registry_id, item.app_id)"
            @action="onAppAction(item, $event)"
          />
        </div>
      </div>
    </PageShell>

    <DetailPane
      v-else
      width="narrow"
      :back-label="t('apps.title')"
      @back="closeDetail"
    >
      <SettingsShell width="narrow">
        <AppDetailPanel
          :item="selected"
          :workspace-state="workspaceState"
          :busy="running || dependencyRunning"
          :owns-stream="ownsAppStream(selected.registry_id, selected.app_id)"
          :dependency-owns-stream="dependencyOwnsStream"
          :connector-catalog="connectorCatalog"
          :connectors-enabled="capabilitiesStore.connectors"
          :connector-pending="connectorPending"
          @action="onAppAction(selected, $event)"
          @dependency-primary="onDependencyPrimary"
          @dependency-menu="onDependencyMenu"
          @connector="onSelectedConnector"
          @connector-enabled="setConnectorEnabled"
        />
      </SettingsShell>
    </DetailPane>

    <AppProgressDialog
      :open="appProgressOpen"
      :name="activeApp?.name ?? ''"
      :action="activeApp?.action ?? 'install'"
      :steps="activeApp?.steps ?? []"
      :lines="activeApp?.lines ?? []"
      :status="activeApp?.status ?? 'running'"
      :result="activeApp?.result"
      :error="activeApp?.error"
      @update:open="setAppProgressOpen"
      @retry="retryApp"
    />

    <AppRemoveDialog
      :open="!!removeTarget"
      :name="removeTarget ? appDisplayName(removeTarget, locale) : ''"
      :preview="removePreview"
      :loading="removePreviewLoading"
      :error="removePreviewError"
      @update:open="(value) => { if (!value) removeTarget = null }"
      @confirm="onRemoveConfirmed"
    />

    <AppUpdateDialog
      :open="!!updateTarget"
      :item="updateTarget"
      @update:open="(value) => { if (!value) updateTarget = null }"
      @confirm="onUpdateConfirmed"
    />

    <AppConnectorAuthDialog
      :open="!!authTarget"
      :bot-id="botId"
      :installation-id="authTarget?.installationId ?? ''"
      :app-name="authTarget?.appName ?? ''"
      :connector="authTarget?.connector ?? null"
      :catalog="authTarget?.connector.type ? connectorCatalog.get(authTarget.connector.type) : undefined"
      @update:open="(value) => { if (!value) authTarget = null }"
      @authorized="onConnectorAuthorized"
    />

    <ConfirmDeleteDialog
      :open="!!disconnectTarget"
      :title="t('connectors.disconnectTitle')"
      :description="t('connectors.disconnectDescription', { name: disconnectTarget?.name ?? '' })"
      :confirm-label="t('connectors.disconnect')"
      :cancel-label="t('common.cancel')"
      :loading="connectorPending.has(disconnectTarget?.connector.connection_id ?? '')"
      @update:open="(value) => { if (!value) disconnectTarget = null }"
      @confirm="disconnectConnector"
    />

    <DependencyConfirmDialog
      :open="confirm.open"
      :mode="confirm.mode"
      :item="confirm.item"
      :prerequisites="confirmPrerequisites"
      @update:open="(value) => { confirm.open = value }"
      @confirm="onDependencyConfirmed"
    />

    <DependencyProgressDialog
      :open="dependencyProgressOpen"
      :name="activeDependency ? dependencyName(activeDependency.item) : ''"
      :action="activeDependency?.action ?? 'update'"
      :lines="activeDependency?.lines ?? []"
      :status="activeDependency?.status ?? 'running'"
      :error="activeDependency?.error"
      :result-version="activeDependency?.resultVersion"
      :entrypoint="activeDependency?.entrypoint"
      @update:open="setDependencyProgressOpen"
      @retry="retryDependency"
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

    <DependencyRollbackDialog
      :open="!!rollbackTarget"
      :item="rollbackTarget"
      :loading="rollingBack"
      @update:open="(value) => { if (!value && !rollingBack) rollbackTarget = null }"
      @confirm="onRollbackConfirmed"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, reactive, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
  Button,
  CalloutBanner,
  ConfirmDeleteDialog,
  DetailPane,
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
  PageShell,
  SettingsRow,
  SettingsSection,
  SettingsShell,
  Skeleton,
  toast,
} from '@felinic/ui'
import { ArrowRight, Plus, RefreshCw } from 'lucide-vue-next'
import {
  deleteBotsByBotIdConnectorsByConnectionId,
  getConnectorsCatalog,
  patchBotsByBotIdConnectorsByConnectionId,
  postBotsByBotIdContainerStart,
  type ConnectorsConnector,
} from '@memohai/sdk'
import DependencyConfirmDialog from './dependency-confirm-dialog.vue'
import DependencyProgressDialog from './dependency-progress-dialog.vue'
import DependencyRollbackDialog from './dependency-rollback-dialog.vue'
import DependencyScriptDialog from './dependency-script-dialog.vue'
import AppConnectorAuthDialog from './app-connector-auth-dialog.vue'
import AppProgressDialog from './app-progress-dialog.vue'
import AppRemoveDialog from './app-remove-dialog.vue'
import AppDetailPanel, { type AppConnectorAction } from './app-detail-panel.vue'
import BotAppCard from './bot-app-card.vue'
import type { AppRowAction } from './app-actions'
import AppUpdateDialog, { type AppUpdateChoice } from './app-update-dialog.vue'
import { useDependencyOperation } from '../composables/useDependencyOperation'
import { useAppOperation } from '../composables/useAppOperation'
import {
  checkAppUpdates,
  fetchAppRemovalPreview,
  invalidateBotApps,
  appDisplayName,
  appInProgress,
  appKey,
  useBotAppsQuery,
  type AppConnectorItem,
  type AppItem,
  type AppRemovalPreview,
} from '@/composables/api/useApps'
import {
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
import {
  prepareConnectorOAuthPopup,
  reauthorizeConnector,
  reauthorizeFailureNotice,
} from '@/composables/useConnectorOAuth'
import { useDialogMutation } from '@/composables/useDialogMutation'
import { useWorkspaceDependencyText } from '@/composables/useWorkspaceDependencyText'
import { useCapabilitiesStore } from '@/store/capabilities'
import { useUserStore } from '@/store/user'
import { isApiErrorCode, resolveApiErrorMessage } from '@/utils/api-error'
import {
  dependencyAllows,
  formatDependencyVersion,
  missingPrerequisites,
  prerequisiteRevisions,
  type DependencyConfirmMode,
  type DependencyMenuAction,
  type DependencyPrimaryAction,
} from '@/utils/workspace-dependency'

const props = defineProps<{ botId: string }>()

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const queryCache = useQueryCache()
const capabilitiesStore = useCapabilitiesStore()
const userStore = useUserStore()
const { run: runMutation } = useDialogMutation()
const { dependencyName } = useWorkspaceDependencyText()
const botIdRef = computed(() => props.botId) as Ref<string>

onMounted(() => {
  void capabilitiesStore.load()
})

// ---- App list -----------------------------------------------------------

const forceRefresh = ref(false)
const { data, error, isLoading, refetch } = useBotAppsQuery(botIdRef, forceRefresh)

// Pinia Colada's refetch ignores `enabled`, so manual refreshes must skip the
// window before the bot id is known; otherwise they hit `/bots//apps`.
function refetchApps(): Promise<unknown> {
  if (!botIdRef.value) return Promise.resolve()
  return refetch()
}
const items = computed<AppItem[]>(() => data.value?.items ?? [])
const loading = computed(() => (isLoading.value || !botIdRef.value) && !data.value && !error.value)

const workspaceState = computed<DependencyWorkspaceState | undefined>(() => {
  if (data.value?.workspace_state) return data.value.workspace_state
  if (isApiErrorCode(error.value, 'workspace_dependency.workspace_not_running')) return 'not_running'
  if (isApiErrorCode(error.value, 'workspace_dependency.workspace_missing')) return 'missing'
  return undefined
})
const loadFailed = computed(() => !!error.value && !data.value && !workspaceState.value)

// ---- App page (second level) --------------------------------------------
// The open App lives in the route query so a reload or the browser's
// back button land where the user was.

const selectedKey = computed(() => (typeof route.query.app === 'string' ? route.query.app : ''))
const selected = computed<AppItem | null>(() => items.value.find(item => appKey(item) === selectedKey.value) ?? null)

function openDetail(item: AppItem) {
  void router.replace({ query: { ...route.query, app: appKey(item) } }).catch(() => {})
}

function closeDetail() {
  const { app: _drop, ...rest } = route.query
  void router.replace({ query: rest }).catch(() => {})
}

const retrying = ref(false)
async function retryDiscovery() {
  if (retrying.value) return
  retrying.value = true
  try {
    forceRefresh.value = true
    await refetchApps()
  } finally {
    retrying.value = false
  }
}

const banner = computed(() => {
  switch (workspaceState.value) {
    case 'not_running':
      return {
        title: t('bots.dependencies.workspace.notRunningTitle'),
        description: t('apps.workspaceNotRunningDescription'),
        action: 'start',
      }
    case 'missing':
      return { title: t('bots.dependencies.workspace.missingTitle'), description: '', action: 'container' }
    default:
      return null
  }
})

// ---- Connect-It catalog -----------------------------------------------------

const catalogQuery = useQuery({
  key: () => ['connectors-catalog'],
  query: async () => {
    const { data } = await getConnectorsCatalog({ throwOnError: true })
    return data
  },
  enabled: () => capabilitiesStore.connectors,
})
const connectorCatalog = computed(() => new Map(
  (catalogQuery.data.value ?? [])
    .filter((item): item is typeof item & { type: string } => !!item.type)
    .map(item => [item.type, item]),
))

// ---- App operations (streamed) -----------------------------------------

const {
  active: activeApp,
  progressOpen: appProgressOpen,
  running,
  ownsStream: ownsAppStream,
  start: startApp,
  retry: retryApp,
  viewProgress: viewAppProgress,
  setProgressOpen: setAppProgressOpen,
} = useAppOperation(botIdRef, 'bot-apps')

function onAppAction(item: AppItem, action: AppRowAction) {
  switch (action) {
    case 'open':
      openDetail(item)
      return
    case 'openSupermarket':
      void router.push({
        name: 'supermarket-app-detail',
        params: { registryId: item.registry_id ?? '', appId: item.app_id ?? '' },
        query: { botId: props.botId },
      })
      return
    case 'viewProgress':
      viewAppProgress(item.registry_id, item.app_id)
      return
    case 'resume':
    case 'retry':
      if (!item.installation_id) return
      startApp({
        registryId: item.registry_id ?? '',
        appId: item.app_id ?? '',
        installationId: item.installation_id,
        name: appDisplayName(item, locale.value),
        action: 'resume',
      })
      return
    case 'update':
      updateTarget.value = item
      return
    case 'remove':
      void openRemove(item)
      return
    default:
      break
  }
}

// ---- Update -----------------------------------------------------------------

const updateTarget = ref<AppItem | null>(null)

function onUpdateConfirmed(choice: AppUpdateChoice) {
  const item = updateTarget.value
  updateTarget.value = null
  if (!item) return
  startApp({
    registryId: item.registry_id ?? '',
    appId: item.app_id ?? '',
    installationId: item.installation_id ?? undefined,
    name: appDisplayName(item, locale.value),
    action: 'update',
    update: { ...choice },
  })
}

// ---- Removal ----------------------------------------------------------------

const removeTarget = ref<AppItem | null>(null)
const removePreview = ref<AppRemovalPreview | null>(null)
const removePreviewLoading = ref(false)
const removePreviewError = ref('')

async function openRemove(item: AppItem) {
  if (!item.installation_id) return
  removeTarget.value = item
  removePreview.value = null
  removePreviewError.value = ''
  removePreviewLoading.value = true
  try {
    removePreview.value = await fetchAppRemovalPreview(props.botId, item.installation_id)
  } catch (err) {
    removePreviewError.value = resolveApiErrorMessage(err, t('common.loadFailed'))
  } finally {
    removePreviewLoading.value = false
  }
}

function onRemoveConfirmed(options: { removeUnreferencedRequired: boolean }) {
  const item = removeTarget.value
  removeTarget.value = null
  if (!item?.installation_id) return
  startApp({
    registryId: item.registry_id ?? '',
    appId: item.app_id ?? '',
    installationId: item.installation_id,
    name: appDisplayName(item, locale.value),
    action: 'remove',
    removeUnreferencedRequired: options.removeUnreferencedRequired,
  })
}

// ---- Connectors -------------------------------------------------------------

const authTarget = ref<{ installationId: string; appName: string; connector: AppConnectorItem } | null>(null)
const connectorPending = ref(new Set<string>())
const disconnectTarget = ref<{ connector: AppConnectorItem; name: string } | null>(null)

function onSelectedConnector(connector: AppConnectorItem, action: AppConnectorAction) {
  if (selected.value) void onConnector(selected.value, connector, action)
}

async function onConnector(item: AppItem, connector: AppConnectorItem, action: AppConnectorAction) {
  if (!item.installation_id) return
  if (action === 'authorize') {
    authTarget.value = { installationId: item.installation_id, appName: appDisplayName(item, locale.value), connector }
    return
  }
  if (action === 'disconnect') {
    const name = connectorCatalog.value.get(connector.type ?? '')?.name || connector.type || t('connectors.unknown')
    disconnectTarget.value = { connector, name }
    return
  }
  await reauthorize(connector)
}

/**
 * Deletes the bot-level connection after confirmation. App updates only
 * unlink connections, so this is the one place a stored credential is
 * revoked outside of uninstalling an App.
 */
async function disconnectConnector() {
  const target = disconnectTarget.value
  const connectionId = target?.connector.connection_id
  if (!target || !connectionId) return
  if (connectorPending.value.has(connectionId)) return
  connectorPending.value.add(connectionId)
  try {
    await deleteBotsByBotIdConnectorsByConnectionId({
      path: { bot_id: props.botId, connection_id: connectionId },
      throwOnError: true,
    })
    disconnectTarget.value = null
    await onConnectorAuthorized()
    toast.success(t('connectors.disconnected'))
  } catch (err) {
    toast.error(resolveApiErrorMessage(err, t('connectors.disconnectFailed')))
  } finally {
    connectorPending.value.delete(connectionId)
  }
}

async function onConnectorAuthorized() {
  await Promise.all([
    invalidateBotApps(queryCache, props.botId),
    queryCache.invalidateQueries({ key: ['bot-connectors', props.botId] }),
  ])
}

async function reauthorize(connector: AppConnectorItem) {
  const connectionId = connector.connection_id
  if (!connectionId) return
  const key = `${connectionId}:reauth`
  if (connectorPending.value.has(key)) return
  const popup = prepareConnectorOAuthPopup(t('common.loading'))
  connectorPending.value.add(key)
  try {
    await reauthorizeConnector(props.botId, connectionId, popup)
    await onConnectorAuthorized()
    toast.success(t('connectors.oauthSuccess'))
  } catch (err) {
    popup?.close()
    const notice = reauthorizeFailureNotice(err, t, userStore.userInfo.role, connector, connectorCatalog.value)
    toast.error(notice.message, { duration: notice.duration })
  } finally {
    connectorPending.value.delete(key)
  }
}

async function setConnectorEnabled(connection: ConnectorsConnector, enabled: boolean) {
  const connectionId = connection.connection_id
  if (!connectionId || connection.enabled === enabled) return
  if (connectorPending.value.has(connectionId)) return
  connectorPending.value.add(connectionId)
  try {
    await patchBotsByBotIdConnectorsByConnectionId({
      path: { bot_id: props.botId, connection_id: connectionId },
      body: { enabled },
      throwOnError: true,
    })
    await onConnectorAuthorized()
  } catch (err) {
    toast.error(resolveApiErrorMessage(err, t('connectors.updateFailed')))
  } finally {
    connectorPending.value.delete(connectionId)
  }
}

// ---- Dependency sub-item operations -----------------------------------------

const {
  active: activeDependency,
  progressOpen: dependencyProgressOpen,
  running: dependencyRunning,
  ownsStream: dependencyOwnsStream,
  start: startDependency,
  retry: retryDependency,
  viewProgress: viewDependencyProgress,
  setProgressOpen: setDependencyProgressOpen,
} = useDependencyOperation(botIdRef)

const confirm = reactive<{
  open: boolean
  mode: DependencyConfirmMode
  item: DependencyItem | null
  operation: DependencyOperationAction
  definitionRevision: string
}>({ open: false, mode: 'update', item: null, operation: 'update', definitionRevision: '' })

// The full list includes prerequisites no App references directly.
const { data: botDependencies } = useBotDependenciesQuery(botIdRef, undefined, () => confirm.open)
const confirmPrerequisites = computed(() => {
  if (!confirm.item || !botDependencies.value) return []
  return missingPrerequisites(confirm.item, botDependencies.value.items ?? []).map(entry => dependencyName(entry))
})

function openConfirm(item: DependencyItem, mode: DependencyConfirmMode, operation: DependencyOperationAction) {
  confirm.item = item
  confirm.definitionRevision = item.definition_revision ?? ''
  confirm.mode = mode
  confirm.operation = operation
  confirm.open = true
}

function onDependencyConfirmed(version: string) {
  const item = confirm.item
  confirm.open = false
  // Bind the prerequisites the dialog showed; before the list loads that is
  // none, and the Server asks for a new review if one turns out missing.
  const prerequisites = item ? prerequisiteRevisions(item, botDependencies.value?.items ?? []) : {}
  if (item) startDependency(item, confirm.operation, { version, definitionRevision: confirm.definitionRevision, prerequisiteRevisions: prerequisites })
}

function onDependencyPrimary(item: DependencyItem, action: DependencyPrimaryAction) {
  switch (action.kind) {
    case 'viewProgress':
      viewDependencyProgress(item)
      return
    case 'update':
      openConfirm(item, 'update', 'update')
      return
    case 'retry':
      openConfirm(item, 'reinstall', 'reinstall')
      return
    default:
      break
  }
}

const rollbackTarget = ref<DependencyItem | null>(null)
const rollingBack = ref(false)

function onDependencyMenu(item: DependencyItem, action: DependencyMenuAction) {
  switch (action.kind) {
    case 'reinstall':
      openConfirm(item, 'reinstall', 'reinstall')
      return
    case 'rollback':
      rollbackTarget.value = item
      return
    case 'viewScript':
      void openScript(item, dependencyAllows(item, 'update') ? 'update' : 'install')
      return
    default:
      break
  }
}

async function onRollbackConfirmed() {
  const item = rollbackTarget.value
  if (!item?.id || rollingBack.value) return
  const to = formatDependencyVersion(item.previous_version)
  rollingBack.value = true
  try {
    await runMutation(() => rollbackDependency(props.botId, item.id ?? ''), {
      fallbackMessage: t('bots.dependencies.rollback.failed'),
      onSuccess: async () => {
        rollbackTarget.value = null
        toast.success(t('bots.dependencies.rollback.success', { to }))
        await Promise.all([invalidateBotApps(queryCache, props.botId), invalidateBotDependencies(queryCache, props.botId)])
        if (item.category === 'agent') void queryCache.invalidateQueries({ key: ['bot-agents', props.botId] })
      },
    })
  } finally {
    rollingBack.value = false
  }
}

const script = reactive<{
  open: boolean
  item: DependencyItem | null
  action: ScriptAction
  actions: ScriptAction[]
  data: ScriptResponse | null
  loading: boolean
  error: string
  definitionRevision: string
}>({ open: false, item: null, action: 'install', actions: [], data: null, loading: false, error: '', definitionRevision: '' })
let scriptSequence = 0

function scriptActionsFor(item: DependencyItem): ScriptAction[] {
  const actions: ScriptAction[] = ['install']
  if (dependencyAllows(item, 'update')) actions.push('update')
  if (dependencyAllows(item, 'remove')) actions.push('remove')
  return actions
}

async function openScript(item: DependencyItem, action: ScriptAction) {
  script.definitionRevision = item.definition_revision ?? ''
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
    const response = await fetchDependencyScript(props.botId, item.id, action, script.definitionRevision)
    if (sequence === scriptSequence) {
      script.data = response
      script.definitionRevision = response.definition_revision ?? ''
    }
  } catch (err) {
    if (sequence === scriptSequence) script.error = resolveApiErrorMessage(err, t('common.loadFailed'))
  } finally {
    if (sequence === scriptSequence) script.loading = false
  }
}

function switchScriptAction(action: ScriptAction) {
  void loadScript(action)
}

watch(botIdRef, () => {
  confirm.open = false
  removeTarget.value = null
  updateTarget.value = null
  authTarget.value = null
  script.open = false
  scriptSequence++
})

// ---- Manual refresh, workspace start & navigation ---------------------------

const checking = ref(false)
async function checkUpdates() {
  if (checking.value) return
  checking.value = true
  try {
    await runMutation(() => checkAppUpdates(props.botId), {
      fallbackMessage: t('apps.checkUpdatesFailed'),
      onSuccess: async (refreshed) => {
        queryCache.setQueryData(['bot-apps', props.botId], refreshed)
        await Promise.all([invalidateBotApps(queryCache, props.botId), invalidateBotDependencies(queryCache, props.botId)])
        toast.success(t('apps.checkUpdatesDone'))
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
          await Promise.all([invalidateBotApps(queryCache, props.botId), invalidateBotDependencies(queryCache, props.botId)])
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

function goToSupermarket() {
  void router.push({ name: 'supermarket', query: { botId: props.botId } }).catch(() => {})
}

// ---- Polling for operations this client does not own ------------------------

const POLL_MS = 5_000
let pollTimer: ReturnType<typeof setInterval> | null = null
let tabActive = true

const hasForeignProgress = computed(() => items.value.some(item => appInProgress(item) && !ownsAppStream(item.registry_id, item.app_id)))

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
      if (typeof document === 'undefined' || document.visibilityState === 'visible') void refetchApps()
    }, POLL_MS)
  } else if (!shouldPoll) {
    stopPolling()
  }
}

watch(hasForeignProgress, syncPolling, { immediate: true })

onActivated(() => {
  tabActive = true
  syncPolling()
  void refetchApps()
})

onDeactivated(() => {
  tabActive = false
  stopPolling()
})

onBeforeUnmount(stopPolling)
</script>
