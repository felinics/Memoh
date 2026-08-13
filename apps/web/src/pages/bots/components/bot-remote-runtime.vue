<template>
  <PageShell
    variant="tab"
    :title="t('bots.remoteRuntime.title')"
  >
    <div class="space-y-8">
      <SettingsSection v-if="!loadFailed">
        <InlineLoadingRow
          v-if="initialLoading"
          surface="card-row"
        >
          {{ t('bots.remoteRuntime.loading') }}
        </InlineLoadingRow>

        <SettingsRow
          v-else
          stack="sm"
          :label="t('bots.remoteRuntime.primary')"
          :description="t('bots.remoteRuntime.primaryDescription')"
        >
          <Select
            :model-value="primaryTargetId"
            :disabled="primarySaving"
            @update:model-value="setPrimary"
          >
            <SelectTrigger class="w-full sm:w-64">
              <SelectValue />
            </SelectTrigger>
            <SelectContent align="end">
              <SelectItem
                v-for="target in validTargets"
                :key="target.target_id"
                :value="target.target_id"
                :disabled="!canSetPrimaryTarget(target)"
              >
                {{ targetName(target) }}
              </SelectItem>
            </SelectContent>
          </Select>
        </SettingsRow>
      </SettingsSection>

      <SettingsSection :title="t('bots.remoteRuntime.workspaceTitle')">
        <InlineLoadingRow
          v-if="initialLoading"
          surface="card-row"
        >
          {{ t('bots.remoteRuntime.loading') }}
        </InlineLoadingRow>

        <SettingsRow
          v-else-if="loadFailed"
          :label="t('bots.remoteRuntime.loadFailed')"
          :description="t('bots.remoteRuntime.loadFailedDescription')"
        >
          <Button
            variant="outline"
            size="sm"
            @click="retry"
          >
            {{ t('runtimes.retry') }}
          </Button>
        </SettingsRow>

        <template v-else>
          <SettingsRow
            v-if="showThisComputerSetup"
            stack="sm"
            :label="t('bots.remoteRuntime.thisComputerOff')"
            :description="t('bots.remoteRuntime.thisComputerOffDescription')"
          >
            <Button
              variant="outline"
              size="sm"
              @click="openComputers"
            >
              {{ t('bots.remoteRuntime.openComputerSettings') }}
            </Button>
          </SettingsRow>

          <!-- The native workspace is part of every bot — listed for the
               primary/status readout, never switchable. -->
          <SettingsRow
            v-if="nativeTarget"
            stack="sm"
            :label="t('bots.remoteRuntime.nativeWorkspace')"
            :description="t('bots.remoteRuntime.serverDescription')"
          >
            <div class="flex items-center gap-2">
              <Badge
                v-if="nativeTarget.primary"
                variant="secondary"
                size="sm"
              >
                {{ t('bots.remoteRuntime.defaultBadge') }}
              </Badge>
              <span class="flex items-center gap-1.5 text-xs text-muted-foreground">
                <span
                  class="size-1.5 rounded-full"
                  :class="statusDotClass(targetStatus(nativeTarget))"
                />
                {{ statusLabel(targetStatus(nativeTarget)) }}
              </span>
            </div>
          </SettingsRow>

          <!-- Every account computer gets a row; the switch IS the ACL. -->
          <SettingsRow
            v-for="row in computerRows"
            :key="row.key"
            stack="sm"
            :label="row.name"
          >
            <div class="flex items-center gap-2">
              <Badge
                v-if="row.primary"
                variant="secondary"
                size="sm"
              >
                {{ t('bots.remoteRuntime.defaultBadge') }}
              </Badge>
              <span class="flex items-center gap-1.5 text-xs text-muted-foreground">
                <span
                  class="size-1.5 rounded-full"
                  :class="statusDotClass(row.status)"
                />
                {{ statusLabel(row.status) }}
              </span>
              <Switch
                :model-value="accessOverrides.get(row.key) ?? row.enabled"
                :disabled="accessPending.has(row.key)"
                :aria-label="row.name"
                @update:model-value="toggleAccess(row, $event)"
              />
            </div>
          </SettingsRow>

          <SettingsRow
            v-if="computerRows.length === 0"
            stack="sm"
            :label="t('bots.remoteRuntime.noComputers')"
          />
        </template>
      </SettingsSection>

      <!-- Connect/disconnect is account-level work — its own card, not part of
           the per-bot workspace list above. -->
      <SettingsSection v-if="!initialLoading && !loadFailed">
        <SettingsRow
          stack="sm"
          :label="t('bots.remoteRuntime.connectManage')"
          :description="t('bots.remoteRuntime.connectManageDescription')"
        >
          <Button
            variant="outline"
            size="sm"
            @click="openComputers"
          >
            {{ computerRows.length ? t('bots.remoteRuntime.manageComputers') : t('runtimes.connect') }}
          </Button>
        </SettingsRow>
      </SettingsSection>
    </div>
  </PageShell>
</template>

<script setup lang="ts">
import { computed, inject, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useMutation, useQuery } from '@pinia/colada'
import {
  getBotsByBotIdWorkspaceTargets,
  putBotsByBotIdWorkspaceTargetsPrimary,
  type WorkspaceWorkspaceTarget,
} from '@memohai/sdk'
import {
  Badge,
  Button,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Switch,
  toast,
} from '@felinic/ui'
import {
  DesktopRuntimeKey,
  type DesktopRuntimeState,
} from '@/lib/desktop-shell'
import { InlineLoadingRow, PageShell, SettingsRow, SettingsSection } from '@felinic/ui'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useAccountRuntimes, useComputerAccessActions } from '@/components/computer/use-computer-access'

const props = defineProps<{
  botId: string
}>()

type ValidWorkspaceTarget = WorkspaceWorkspaceTarget & {
  target_id: string
  kind: string
}

// One row per account computer (mounted or not), plus stale mounts whose
// runtime was revoked — those stay listed so access can still be switched off.
type ComputerRow = {
  key: string
  runtimeId?: string
  targetId?: string
  name: string
  status: string
  primary: boolean
  enabled: boolean
}

const { t } = useI18n()
const router = useRouter()
const desktopRuntimeBridge = inject(DesktopRuntimeKey, undefined)
const desktopRuntimeState = ref<DesktopRuntimeState>()

const {
  data: workspaceTargetsResponse,
  error: workspaceTargetsError,
  isLoading: workspaceTargetsLoading,
  refetch: refetchWorkspaceTargets,
} = useQuery({
  key: () => ['bot-workspace-targets', props.botId],
  query: async () => {
    const { data } = await getBotsByBotIdWorkspaceTargets({
      path: { bot_id: props.botId },
      throwOnError: true,
    })
    return data
  },
  enabled: () => !!props.botId,
  refetchOnWindowFocus: true,
})

const {
  runtimes,
  isLoading: runtimesLoading,
  refetch: refetchRuntimes,
} = useAccountRuntimes()
const { grantAccess, revokeAccess } = useComputerAccessActions()

const targetItems = ref<WorkspaceWorkspaceTarget[]>([])

watch(workspaceTargetsResponse, (response) => {
  if (!response) return
  targetItems.value = (response.targets ?? []).map(target => ({
    ...target,
    tool_approval: target.tool_approval ? { ...target.tool_approval } : undefined,
  }))
}, { immediate: true })

const validTargets = computed<ValidWorkspaceTarget[]>(() => (
  targetItems.value.filter((target): target is ValidWorkspaceTarget => (
    typeof target.target_id === 'string'
    && target.target_id.length > 0
    && typeof target.kind === 'string'
    && target.kind.length > 0
  ))
))

const nativeTarget = computed(() => validTargets.value.find(target => target.kind === 'native'))

const runtimeItems = computed(() => runtimes.value ?? [])
const primaryTargetId = computed(() => (
  validTargets.value.find(target => target.primary)?.target_id ?? 'native'
))
const initialLoading = computed(() => (
  (workspaceTargetsLoading.value && !workspaceTargetsResponse.value)
  || (runtimesLoading.value && !runtimes.value)
))
const loadFailed = computed(() => !!workspaceTargetsError.value && !workspaceTargetsResponse.value)
const showThisComputerSetup = computed(() => (
  !!desktopRuntimeBridge
  && !!desktopRuntimeState.value
  && !desktopRuntimeState.value.enabled
))

const computerRows = computed<ComputerRow[]>(() => {
  const rows: ComputerRow[] = []
  const covered = new Set<string>()
  for (const runtime of runtimeItems.value) {
    if (!runtime.id) continue
    covered.add(runtime.id)
    const target = validTargets.value.find(item => item.runtime_id === runtime.id)
    rows.push({
      key: runtime.id,
      runtimeId: runtime.id,
      targetId: target?.target_id,
      name: runtimeName(runtime),
      status: target ? targetStatus(target) : (runtime.online ? 'online' : 'offline'),
      primary: target?.primary ?? false,
      enabled: !!target,
    })
  }
  for (const target of validTargets.value) {
    if (target.kind !== 'remote' || !target.runtime_id || covered.has(target.runtime_id)) continue
    rows.push({
      key: target.target_id,
      runtimeId: target.runtime_id,
      targetId: target.target_id,
      name: targetName(target),
      status: targetStatus(target),
      primary: target.primary ?? false,
      enabled: true,
    })
  }
  return rows
})

// Optimistic overlay: the switch flips immediately and holds until the
// workspace-targets refetch lands; an error clears the overlay and reverts.
const accessOverrides = ref(new Map<string, boolean>())
const accessPending = ref(new Set<string>())

const { mutateAsync: setPrimaryRequest, isLoading: primarySaving } = useMutation({
  mutation: async (targetId: string) => {
    await putBotsByBotIdWorkspaceTargetsPrimary({
      path: { bot_id: props.botId },
      body: { target_id: targetId },
      throwOnError: true,
    })
  },
})

function targetName(target: WorkspaceWorkspaceTarget): string {
  if (target.kind === 'native') return t('bots.remoteRuntime.nativeWorkspace')
  return target.name || t('bots.remoteRuntime.unknownComputer')
}

function runtimeName(runtime: { id?: string, name?: string, hostname?: string }): string {
  return runtime.name || runtime.hostname || t('bots.remoteRuntime.unknownComputer')
}

function targetStatus(target: WorkspaceWorkspaceTarget): string {
  return target.status || 'offline'
}

function statusLabel(status: string): string {
  switch (status) {
    case 'online':
    case 'offline':
    case 'revoked':
    case 'owner_mismatch':
    case 'client_update_required':
      return t('runtimes.status.' + status)
    default:
      return status
  }
}

function statusDotClass(status: string): string {
  switch (status) {
    case 'online':
      return 'bg-success'
    case 'offline':
      return 'bg-accent-gray-border'
    default:
      return 'bg-destructive'
  }
}

function canSetPrimaryTarget(target: WorkspaceWorkspaceTarget): boolean {
  return target.status !== 'owner_mismatch' && target.status !== 'revoked'
}

async function setPrimary(value: unknown): Promise<void> {
  const targetId = typeof value === 'string' ? value : ''
  if (!targetId || targetId === primaryTargetId.value || primarySaving.value) return
  const previous = primaryTargetId.value
  setLocalPrimary(targetId)
  try {
    await setPrimaryRequest(targetId)
    void refetchWorkspaceTargets()
  } catch (error) {
    setLocalPrimary(previous)
    toast.error(resolveApiErrorMessage(error, t('bots.remoteRuntime.primarySaveFailed')))
  }
}

function setLocalPrimary(targetId: string): void {
  targetItems.value = targetItems.value.map(target => ({
    ...target,
    primary: target.target_id === targetId,
  }))
}

async function toggleAccess(row: ComputerRow, enabled: boolean): Promise<void> {
  if (accessPending.value.has(row.key)) return
  if (!enabled && !row.targetId) return
  if (enabled && !row.runtimeId) return
  accessPending.value = new Set(accessPending.value).add(row.key)
  accessOverrides.value = new Map(accessOverrides.value).set(row.key, enabled)
  try {
    if (enabled) {
      await grantAccess({ botId: props.botId, runtimeId: row.runtimeId! })
    } else {
      await revokeAccess({ botId: props.botId, targetId: row.targetId! })
    }
    await refetchWorkspaceTargets()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('computerAccess.updateFailed')))
  } finally {
    const nextPending = new Set(accessPending.value)
    nextPending.delete(row.key)
    accessPending.value = nextPending
    const nextOverrides = new Map(accessOverrides.value)
    nextOverrides.delete(row.key)
    accessOverrides.value = nextOverrides
  }
}

async function retry(): Promise<void> {
  await Promise.all([refetchWorkspaceTargets(), refetchRuntimes()])
}

function openComputers(): void {
  void router.push({ name: 'runtimes' })
}

onMounted(async () => {
  if (!desktopRuntimeBridge) return
  try {
    desktopRuntimeState.value = await desktopRuntimeBridge.runtimeState()
  } catch {
    // The Computers page owns recovery UI for Desktop connection-state errors.
  }
})
</script>
