<template>
  <div class="space-y-4">
    <div class="space-y-8">
      <SettingsSection
        v-if="props.botId"
        :title="$t('bots.settings.networkPageTitle')"
      >
        <template
          v-if="props.botId && settings && hasChanges"
          #actions
        >
          <span class="text-xs text-muted-foreground">
            {{ $t('common.unsaved') }}
          </span>
        </template>
        <SettingsRow
          :label="$t('bots.settings.networkSDWANSectionTitle')"
          :description="$t('bots.settings.networkSDWANSectionHint')"
        >
          <Switch
            :model-value="form.overlay_enabled"
            @update:model-value="(val) => form.overlay_enabled = val"
          />
        </SettingsRow>

        <template v-if="form.overlay_enabled">
          <SettingsRow :label="$t('bots.settings.overlayProviderFieldLabel')">
            <div class="w-72 shrink-0">
              <OverlayProviderSelect
                v-model="form.overlay_provider"
                :providers="overlayProviderMeta"
                :placeholder="$t('bots.settings.overlayProviderPlaceholder')"
              />
            </div>
          </SettingsRow>

          <!-- One distilled status line; raw diagnostics live behind Details.
               Three mutually-exclusive states of one logical row (pending-save
               / loading / resolved) share SettingsRow's geometry via #content;
               only the resolved state has trailing action buttons. -->
          <SettingsRow v-if="needsSaveForStatus">
            <template #content>
              <div class="flex items-center gap-2.5">
                <span class="size-1.5 shrink-0 rounded-sm bg-warning" />
                <p class="text-sm text-muted-foreground">
                  {{ $t('bots.settings.networkStatusPendingSave') }}
                </p>
              </div>
            </template>
          </SettingsRow>
          <SettingsRow v-else-if="isNetworkStatusLoading && !networkStatusData">
            <template #content>
              <InlineLoadingRow size="md">
                {{ $t('common.loading') }}
              </InlineLoadingRow>
            </template>
          </SettingsRow>
          <SettingsRow v-else-if="networkStatusLine">
            <template #content>
              <div class="flex min-w-0 items-center gap-2.5">
                <span
                  class="size-1.5 shrink-0 rounded-sm"
                  :class="statusDotClass"
                />
                <div class="min-w-0">
                  <div class="truncate text-sm font-medium text-foreground">
                    {{ networkStatusLine.label }}
                  </div>
                  <div
                    v-if="networkStatusLine.detail"
                    class="truncate font-mono text-xs text-muted-foreground"
                  >
                    {{ networkStatusLine.detail }}
                  </div>
                </div>
              </div>
            </template>
            <div class="flex shrink-0 items-center gap-2">
              <Button
                v-if="overlayState === 'needs_login' && overlayAuthURL"
                size="sm"
                variant="outline"
                @click="openAuthURL"
              >
                {{ $t('bots.settings.networkOpenLoginPage') }}
              </Button>
              <Button
                size="sm"
                variant="outline"
                @click="isDetailsDialogOpen = true"
              >
                {{ $t('common.details') }}
              </Button>
            </div>
          </SettingsRow>

          <div v-if="showOverlayConfig">
            <SchemaField
              v-for="field in primaryFields"
              :key="field.key"
              :field="field"
              :field-key="field.key"
              id-prefix="bot-network-config"
              :model-value="getFieldValue(field)"
              @update:model-value="(value) => updateValue(field.key, value)"
            />

            <ExpandableSettingsRow
              v-if="advancedFields.length"
              v-model:open="showAdvancedConfig"
              :label="$t('common.advanced')"
              :description="$t('common.allOptional')"
            >
              <template #expanded>
                <SchemaField
                  v-for="field in advancedFields"
                  :key="field.key"
                  :field="field"
                  :field-key="field.key"
                  id-prefix="bot-network-config"
                  :model-value="getFieldValue(field)"
                  @update:model-value="(value) => updateValue(field.key, value)"
                />
                <div class="flex justify-end pt-3">
                  <Button
                    variant="outline"
                    size="sm"
                    @click="isEditorDialogOpen = true"
                  >
                    <SquarePen class="size-4" />
                    {{ $t('common.editJson') }}
                  </Button>
                </div>
              </template>
            </ExpandableSettingsRow>
          </div>
        </template>
      </SettingsSection>

      <SettingsSection
        v-if="showExitNodeSelector"
        :title="$t('bots.settings.networkExitNode')"
      >
        <SettingsRow
          :label="$t('bots.settings.networkExitNode')"
          :description="nodeListHint"
        >
          <div class="flex w-80 items-center gap-2">
            <NetworkNodeSelect
              v-model="exitNodeValue"
              :nodes="exitNodeOptions"
              :placeholder="$t('bots.settings.networkExitNodePlaceholder')"
            />
            <Button
              variant="outline"
              size="icon-sm"
              :aria-label="$t('common.refresh')"
              loading-mode="icon"
              :loading="isNodeListLoading"
              :disabled="!shouldLoadNodeOptions"
              @click="handleRefreshNodes"
            >
              <RefreshCw class="size-3.5" />
            </Button>
          </div>
        </SettingsRow>
      </SettingsSection>

      <div
        v-if="props.botId && settings && hasChanges"
        class="flex items-center justify-end gap-2"
      >
        <Button
          variant="outline"
          size="sm"
          @click="handleCancel"
        >
          {{ $t('common.cancel') }}
        </Button>
        <Button
          size="sm"
          :loading="isSaving"
          @click="handleSave"
        >
          {{ $t('common.save') }}
        </Button>
      </div>
    </div>

    <Dialog v-model:open="isEditorDialogOpen">
      <DialogContent class="flex max-h-[calc(100vh-2rem)] flex-col overflow-hidden p-0 sm:h-[70vh] sm:max-w-3xl">
        <DialogHeader class="shrink-0 border-b border-border p-4">
          <DialogTitle class="text-sm font-semibold">
            {{ $t('mcp.editValue') }}
          </DialogTitle>
          <DialogDescription class="text-xs leading-snug">
            {{ $t('mcp.editLongTextHint') }}
          </DialogDescription>
        </DialogHeader>
        
        <div class="flex min-h-0 flex-1 p-4">
          <MonacoEditor
            v-model="editorDraftRaw"
            language="json"
            class="min-h-0 flex-1 rounded-md border border-border"
            :options="{
              automaticLayout: true,
              fixedOverflowWidgets: true,
              minimap: { enabled: false },
              scrollBeyondLastLine: false,
              formatOnPaste: true,
              formatOnType: true
            }"
          />
        </div>

        <DialogFooter class="flex shrink-0 items-center justify-between gap-2 border-t border-border p-4">
          <p
            v-if="editorError"
            class="text-xs text-warning"
          >
            {{ editorError }}
          </p>
          <div v-else />
          <div class="flex items-center gap-2">
            <DialogClose as-child>
              <Button
                variant="ghost"
                size="sm"
              >
                {{ $t('common.cancel') }}
              </Button>
            </DialogClose>
            <Button
              size="sm"
              class="min-w-24"
              :disabled="!!editorError"
              @click="handleEditorSave"
            >
              {{ $t('common.confirm') }}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <Dialog v-model:open="isDetailsDialogOpen">
      <DialogContent class="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle class="text-sm font-semibold">
            {{ $t('bots.settings.networkDetailsTitle') }}
          </DialogTitle>
        </DialogHeader>

        <div v-if="overlayNetworkStatusFields.length">
          <div
            v-for="item in overlayNetworkStatusFields"
            :key="`d-ov-${item.label}`"
            class="flex items-start justify-between gap-4 border-b border-border py-2.5 last:border-b-0"
          >
            <span class="shrink-0 text-sm text-muted-foreground">{{ item.label }}</span>
            <span
              class="max-w-[60%] break-all text-right text-sm text-foreground"
              :class="{ 'font-mono': item.mono }"
            >{{ item.value }}</span>
          </div>
        </div>

        <div v-if="workspaceStatusFields.length">
          <div class="mb-1 text-xs font-medium text-muted-foreground">
            {{ $t('bots.settings.networkDetailsWorkspaceGroup') }}
          </div>
          <div
            v-for="item in workspaceStatusFields"
            :key="`d-ws-${item.label}`"
            class="flex items-start justify-between gap-4 border-b border-border py-2.5 last:border-b-0"
          >
            <span class="shrink-0 text-sm text-muted-foreground">{{ item.label }}</span>
            <span
              class="max-w-[60%] break-all text-right text-sm text-foreground"
              :class="{ 'font-mono': item.mono }"
            >{{ item.value }}</span>
          </div>
        </div>

        <DialogFooter
          v-if="showLogoutButton"
          class="border-t border-border pt-4"
        >
          <ConfirmPopover
            variant="destructive"
            :message="$t('bots.settings.networkLogoutConfirm')"
            :cancel-text="$t('common.cancel')"
            :confirm-text="$t('bots.settings.networkLogout')"
            :loading="isLoggingOut"
            @confirm="handleLogout"
          >
            <template #trigger>
              <Button
                variant="destructive"
                size="sm"
                :loading="isLoggingOut"
              >
                {{ $t('bots.settings.networkLogout') }}
              </Button>
            </template>
          </ConfirmPopover>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>

<script setup lang="ts">
import {
  ExpandableSettingsRow,
  Switch,
  Button,
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, DialogClose,
} from '@felinic/ui'
import { SquarePen, RefreshCw } from 'lucide-vue-next'
import { reactive, computed, watch, nextTick, onBeforeUnmount, ref } from 'vue'
import { ConfirmPopover, InlineLoadingRow, SettingsRow, SettingsSection, toast } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import { useMutation, useQuery, useQueryCache } from '@pinia/colada'
import {
  getBotsByBotIdNetworkNodes,
  getBotsByBotIdNetworkStatus,
  getBotsByBotIdSettings,
  getNetworkMeta,
  postBotsByBotIdNetworkActionsByActionId,
  putBotsByBotIdSettings,
} from '@memohai/sdk'
import type { NetworkConfigField, NetworkProviderDescriptor, SettingsUpsertRequest } from '@memohai/sdk'
import { cloneConfig } from '@/components/config-schema-form/utils'
import SchemaField from '@/components/config-schema-form/schema-field.vue'
import { resolveApiErrorMessage } from '@/utils/api-error'
import OverlayProviderSelect from './network-provider-select.vue'
import NetworkNodeSelect from './network-node-select.vue'
import MonacoEditor from '@/components/monaco-editor/index.vue'

const props = defineProps<{
  botId: string
}>()

const { t } = useI18n()
const queryCache = useQueryCache()

const { data: settings } = useQuery({
  key: () => ['bot-settings', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdSettings({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!props.botId,
})

const { data: overlayProviderMetaData } = useQuery({
  key: ['network-providers-meta'],
  query: async () => {
    const { data } = await getNetworkMeta({ throwOnError: true })
    return data
  },
})

const overlayProviderMeta = computed(() => overlayProviderMetaData.value ?? [])

const form = reactive({
  overlay_enabled: false,
  overlay_provider: '',
  overlay_config: {} as Record<string, unknown>,
})

const selectedOverlayProviderMeta = computed(() =>
  overlayProviderMeta.value.find((meta: NetworkProviderDescriptor) => meta.kind === form.overlay_provider),
)
const selectedNetworkCapabilities = computed(() =>
  selectedOverlayProviderMeta.value?.capabilities ?? null,
)

// The provider schema decides which fields show on the card; the rest go to
// the Advanced row. exit_node has its own selector below.
const overlayConfigFields = computed<NetworkConfigField[]>(() =>
  [...(selectedOverlayProviderMeta.value?.config_schema?.fields ?? [])]
    .filter(field => field.key !== 'exit_node')
    .sort((a, b) => (a.order ?? 0) - (b.order ?? 0)),
)
const primaryFields = computed(() => overlayConfigFields.value.filter(field => !field.collapsed))
const advancedFields = computed(() => overlayConfigFields.value.filter(field => field.collapsed))

const showOverlayConfig = computed(() =>
  !!form.overlay_enabled
  && !!form.overlay_provider
  && overlayConfigFields.value.length > 0,
)

const showAdvancedConfig = ref(false)

function getFieldValue(field: NetworkConfigField) {
  return form.overlay_config[field.key] ?? field.default
}

function updateValue(key: string, value: unknown) {
  form.overlay_config = { ...form.overlay_config, [key]: value }
}

// Exit node selection only makes sense after the sidecar is authenticated and connected.
const showExitNodeSelector = computed(() =>
  !!form.overlay_enabled
  && !!form.overlay_provider
  && !!selectedNetworkCapabilities.value?.exit_node
  && isConnected.value,
)

const persistedOverlayProvider = computed(() => settings.value?.overlay_provider ?? '')
const persistedOverlayEnabled = computed(() => settings.value?.overlay_enabled ?? false)
const persistedOverlayConfig = computed(() =>
  JSON.stringify((settings.value?.overlay_config as Record<string, unknown> | undefined) ?? {}),
)
const isSelectedNetworkPersisted = computed(() =>
  form.overlay_enabled === persistedOverlayEnabled.value
  && form.overlay_provider === persistedOverlayProvider.value
  && JSON.stringify(form.overlay_config ?? {}) === persistedOverlayConfig.value,
)
const shouldLoadNetworkStatus = computed(() =>
  !!props.botId
  && persistedOverlayEnabled.value
  && !!persistedOverlayProvider.value,
)
const shouldLoadNodeOptions = computed(() =>
  !!props.botId
  && shouldLoadNetworkStatus.value
  && !!selectedNetworkCapabilities.value?.exit_node,
)

// Transient states that should trigger automatic polling until resolved.
const TRANSIENT_STATES = ['starting', 'needs_login', 'stopped']

const isTransientState = computed(() =>
  TRANSIENT_STATES.includes(overlayState.value),
)

const {
  data: networkStatusData,
  refetch: refetchNetworkStatus,
  // Pinia Colada's isLoading tracks every in-flight fetch, refetches included
  // (it is asyncStatus === 'loading'), so it also gates the poll below.
  isLoading: isNetworkStatusLoading,
} = useQuery({
  key: () => ['bot-network-status', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdNetworkStatus({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!props.botId,
  refetchOnWindowFocus: true,
})

const {
  data: nodeListData,
  isLoading: isNodeListLoading,
  refetch: refetchNodeList,
} = useQuery({
  key: () => ['bot-network-nodes', props.botId, persistedOverlayProvider.value],
  query: async () => {
    const { data } = await getBotsByBotIdNetworkNodes({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data
  },
  enabled: () => shouldLoadNodeOptions.value,
})

const { mutateAsync: updateSettings, isLoading: isSaving } = useMutation({
  mutation: async (body: SettingsUpsertRequest) => {
    const { data } = await putBotsByBotIdSettings({
      path: { bot_id: props.botId },
      body,
      throwOnError: true,
    })
    return data
  },
  onSettled: () => {
    queryCache.invalidateQueries({ key: ['bot-settings', props.botId] })
    queryCache.invalidateQueries({ key: ['bot-network-status', props.botId] })
    queryCache.invalidateQueries({ key: ['bot-network-nodes', props.botId] })
  },
})

const { mutateAsync: runNetworkAction, isLoading: isLoggingOut } = useMutation({
  mutation: async (actionID: string) => {
    const { data } = await postBotsByBotIdNetworkActionsByActionId({
      path: { bot_id: props.botId, action_id: actionID },
      body: {},
      throwOnError: true,
    })
    return data
  },
  onSettled: () => {
    queryCache.invalidateQueries({ key: ['bot-network-status', props.botId] })
  },
})

// ---------------------------------------------------------------------------
// Editor state
// ---------------------------------------------------------------------------
const isEditorDialogOpen = ref(false)
const isDetailsDialogOpen = ref(false)
const editorDraftRaw = ref('')
const editorError = ref('')

watch(isEditorDialogOpen, (open) => {
  if (open) {
    editorDraftRaw.value = JSON.stringify(form.overlay_config, null, 2)
    editorError.value = ''
  }
})

watch(editorDraftRaw, (val) => {
  try {
    JSON.parse(val)
    editorError.value = ''
  } catch {
    editorError.value = t('mcp.importErrorJson')
  }
})

function handleEditorSave() {
  try {
    const parsed = JSON.parse(editorDraftRaw.value)
    form.overlay_config = cloneConfig(parsed)
    isEditorDialogOpen.value = false
  } catch {
    // Should be caught by watcher, but just in case
    editorError.value = t('mcp.importErrorJson')
  }
}

// ---------------------------------------------------------------------------
// Overlay state helpers
// ---------------------------------------------------------------------------

const overlayState = computed(() => {
  const status = networkStatusData.value
  return status?.state ?? ''
})

const overlayAuthURL = computed(() => {
  const status = networkStatusData.value
  return (status?.details?.auth_url as string | undefined) ?? ''
})

// "Connected" means sidecar is fully running and authenticated.
const isConnected = computed(() =>
  ['ready', 'degraded'].includes(overlayState.value),
)

// Show logout when the sidecar is alive (connected or waiting for login).
const showLogoutButton = computed(() =>
  shouldLoadNetworkStatus.value
  && !needsSaveForStatus.value
  && ['ready', 'degraded', 'needs_login', 'starting', 'stopped'].includes(overlayState.value),
)

// ---------------------------------------------------------------------------

const exitNodeOptions = computed(() =>
  (nodeListData.value?.items ?? []).filter(node => node.can_exit_node !== false),
)
const nodeListHint = computed(() => {
  if (!isSelectedNetworkPersisted.value) return t('bots.settings.networkNodesPendingSave')
  if (nodeListData.value?.message) return nodeListData.value.message
  if (!exitNodeOptions.value.length) return t('bots.settings.networkNodesEmpty')
  return t('bots.settings.networkExitNodeDescription')
})
const exitNodeValue = computed({
  get: () => String(form.overlay_config.exit_node ?? ''),
  set: (value: string) => {
    form.overlay_config = {
      ...form.overlay_config,
      exit_node: value || undefined,
    }
  },
})

const needsSaveForStatus = computed(() =>
  form.overlay_enabled
  && !!form.overlay_provider
  && !isSelectedNetworkPersisted.value,
)

const showOverlayStatusInNetworkCard = computed(() =>
  shouldLoadNetworkStatus.value
  && !!networkStatusData.value,
)

type NetworkStatusTone = 'success' | 'warning' | 'muted' | 'destructive'

// Distill the backend's many states into one human line + a colored dot; the raw
// fields live behind Details (§13: let one status carry the surface, hide diagnostics).
const networkStatusLine = computed<{ tone: NetworkStatusTone; label: string; detail?: string } | null>(() => {
  if (!showOverlayStatusInNetworkCard.value) return null
  const status = networkStatusData.value
  const details = status?.details ?? {}
  const address = status?.network_ip || (details.dns_name as string | undefined) || ''
  switch (overlayState.value) {
    case 'ready':
      return { tone: 'success', label: t('bots.settings.networkState.connected'), detail: address || undefined }
    case 'degraded':
      return { tone: 'warning', label: t('bots.settings.networkState.degraded'), detail: address || undefined }
    case 'needs_login':
      return { tone: 'warning', label: t('bots.settings.networkState.needsLogin') }
    case 'starting':
      return { tone: 'muted', label: t('bots.settings.networkState.connecting') }
    case 'stopped':
      return { tone: 'muted', label: t('bots.settings.networkState.stopped') }
    case 'missing':
      return { tone: 'muted', label: t('bots.settings.networkState.notAttached') }
    case 'workspace_missing':
      return { tone: 'muted', label: t('bots.settings.networkState.workspaceMissing') }
    case 'needs_config':
      return { tone: 'warning', label: t('bots.settings.networkState.needsConfig') }
    case 'unauthorized':
      return { tone: 'destructive', label: t('bots.settings.networkState.unauthorized') }
    case 'unsupported':
      return { tone: 'muted', label: t('bots.settings.networkState.unsupported') }
    case 'disabled':
      return { tone: 'muted', label: t('bots.settings.networkState.disabled') }
    default:
      return { tone: 'muted', label: t('bots.settings.networkState.unknown') }
  }
})

const statusDotClass = computed(() => {
  switch (networkStatusLine.value?.tone) {
    case 'success': return 'bg-success'
    case 'warning': return 'bg-warning'
    case 'destructive': return 'bg-destructive'
    default: return 'bg-muted-foreground'
  }
})

function workspaceStateDisplay(state: string) {
  const key = `bots.settings.networkWorkspaceState.${state}`
  const translated = t(key)
  return translated === key ? t('bots.settings.networkWorkspaceState.unknown') : translated
}

const workspaceStatusFields = computed(() => {
  const status = networkStatusData.value
  if (!status || !status.workspace) return []
  const ws = status.workspace
  const items: { label: string; value: string; mono: boolean }[] = [
    { label: t('bots.settings.networkWorkspaceStateLabel'), value: workspaceStateDisplay(ws.state), mono: false },
  ]
  if (ws.container_id) items.push({ label: t('bots.settings.networkWorkspaceContainerID'), value: ws.container_id, mono: true })
  if (ws.task_status) items.push({ label: t('bots.settings.networkWorkspaceTaskStatus'), value: ws.task_status, mono: false })
  if (ws.pid != null && ws.pid > 0) {
    items.push({ label: t('bots.settings.networkWorkspaceTaskPID'), value: String(ws.pid), mono: true })
  }
  if (ws.network_target) items.push({ label: t('bots.settings.networkWorkspaceTarget'), value: ws.network_target, mono: true })
  if (ws.message) items.push({ label: t('bots.settings.networkWorkspaceMessage'), value: ws.message, mono: false })
  return items.filter(item => item.value)
})

const overlayNetworkStatusFields = computed(() => {
  const status = networkStatusData.value
  if (!status) return []
  const details = status.details ?? {}
  const items: { label: string; value: string | undefined; mono: boolean }[] = [
    { label: t('bots.settings.networkStatusState'), value: status.state, mono: false },
    { label: t('bots.settings.networkStatusMessage'), value: status.message, mono: false },
    { label: t('bots.settings.networkStatusIP'), value: status.network_ip, mono: true },
    { label: t('bots.settings.networkStatusProxy'), value: status.proxy_address, mono: true },
    { label: t('bots.settings.networkStatusPID'), value: details.pid == null ? undefined : String(details.pid), mono: true },
    { label: t('bots.settings.networkStatusDNSName'), value: details.dns_name as string | undefined, mono: true },
    { label: t('bots.settings.networkStatusBackendState'), value: details.backend_state as string | undefined, mono: false },
    { label: t('bots.settings.networkStatusHealth'), value: Array.isArray(details.health) ? details.health.join('; ') : undefined, mono: false },
    { label: t('bots.settings.networkStatusSocket'), value: details.localapi_socket_host_path as string | undefined, mono: true },
    { label: t('bots.settings.networkStatusExitNode'), value: details.configured_exit_node as string | undefined, mono: true },
  ]
  return items.filter(item => item.value)
})

const hasChanges = computed(() => {
  if (!settings.value) return true
  const s = settings.value
  return form.overlay_enabled !== (s.overlay_enabled ?? false)
    || form.overlay_provider !== (s.overlay_provider ?? '')
    || JSON.stringify(form.overlay_config ?? {}) !== JSON.stringify((s.overlay_config as Record<string, unknown> | undefined) ?? {})
})

// When settings load from API, overlay_provider goes from '' to the saved value in the
// same flush as configs are written. A separate watcher on overlay_provider must not
// wipe those configs (it would leave the UI empty after refresh).
let skipProviderChangeReset = false

watch(() => form.overlay_provider, (next, prev) => {
  if (next === prev || skipProviderChangeReset) return
  form.overlay_config = {}
})

watch(settings, (val) => {
  if (!val) return
  skipProviderChangeReset = true
  form.overlay_enabled = val.overlay_enabled ?? false
  form.overlay_provider = val.overlay_provider ?? ''
  form.overlay_config = cloneConfig((val.overlay_config as Record<string, unknown> | undefined) ?? {})
  void nextTick(() => {
    skipProviderChangeReset = false
  })
}, { immediate: true })

// Poll network status every 5s while in a transient state (starting, needs_login, etc.)
let pollTimer: ReturnType<typeof setInterval> | null = null

watch(isTransientState, (shouldPoll) => {
  if (shouldPoll && !pollTimer) {
    pollTimer = setInterval(() => {
      if (isTransientState.value && !isNetworkStatusLoading.value) {
        refetchNetworkStatus()
      }
    }, 5000)
  } else if (!shouldPoll && pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}, { immediate: true })

onBeforeUnmount(() => {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
})

async function handleSave() {
  if (form.overlay_enabled && !form.overlay_provider) {
    toast.error(t('bots.settings.overlayProviderRequired'))
    return
  }
  try {
    await updateSettings({
      overlay_enabled: form.overlay_enabled,
      overlay_provider: form.overlay_provider,
      overlay_config: form.overlay_config,
    })
    toast.success(t('bots.settings.saveSuccess'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.settings.networkActionFailed')))
  }
}

function handleCancel() {
  if (settings.value) {
    skipProviderChangeReset = true
    form.overlay_enabled = settings.value.overlay_enabled ?? false
    form.overlay_provider = settings.value.overlay_provider ?? ''
    form.overlay_config = cloneConfig((settings.value.overlay_config as Record<string, unknown> | undefined) ?? {})
    void nextTick(() => {
      skipProviderChangeReset = false
    })
  }
}

async function handleRefreshNodes() {
  try {
    await refetchNodeList()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.settings.networkNodesRefreshFailed')))
  }
}

function openAuthURL() {
  if (overlayAuthURL.value) {
    window.open(overlayAuthURL.value, '_blank', 'noopener,noreferrer')
  }
}

async function handleLogout() {
  try {
    await runNetworkAction('logout')
    toast.success(t('bots.settings.networkLogoutSuccess'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.settings.networkLogoutFailed')))
  }
}
</script>

<style scoped>
</style>
