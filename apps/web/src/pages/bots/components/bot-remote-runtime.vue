<template>
  <PageShell
    variant="tab"
    :title="t('bots.remoteRuntime.title')"
  >
    <div class="space-y-8">
      <SettingsSection
        v-if="!loadFailed"
        :title="t('bots.remoteRuntime.primary')"
      >
        <InlineLoadingRow
          v-if="initialLoading"
          surface="card-row"
        >
          {{ t('bots.remoteRuntime.loading') }}
        </InlineLoadingRow>

        <SettingsRow
          v-else
          stack="sm"
        >
          <template #content>
            <p class="text-xs text-muted-foreground">
              {{ t('bots.remoteRuntime.primaryDescription') }}
            </p>
          </template>
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
        <template #actions>
          <Button
            size="sm"
            @click="openAddDialog"
          >
            <Plus class="size-4" />
            {{ t('bots.remoteRuntime.addComputer') }}
          </Button>
        </template>

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

          <SettingsRow
            v-for="target in validTargets"
            :key="target.target_id"
            stack="sm"
            :label="targetName(target)"
            :description="target.kind === 'native' ? t('bots.remoteRuntime.serverDescription') : undefined"
          >
            <div class="flex items-center gap-2">
              <Badge
                v-if="target.primary"
                variant="secondary"
                size="sm"
              >
                {{ t('bots.remoteRuntime.defaultBadge') }}
              </Badge>
              <span class="flex items-center gap-1.5 text-xs text-muted-foreground">
                <span
                  class="size-1.5 rounded-full"
                  :class="statusDotClass(targetStatus(target))"
                />
                {{ statusLabel(targetStatus(target)) }}
              </span>

              <DropdownMenu v-if="target.kind === 'remote'">
                <DropdownMenuTrigger as-child>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    :aria-label="t('common.actions')"
                  >
                    <MoreHorizontal class="size-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem
                    variant="destructive"
                    @select="pendingDeleteTarget = target"
                  >
                    <Trash2 class="size-4" />
                    {{ t('bots.remoteRuntime.removeFromBot') }}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          </SettingsRow>
        </template>
      </SettingsSection>
    </div>
  </PageShell>

  <Dialog v-model:open="mountDialogOpen">
    <DialogContent>
      <form @submit.prevent="saveMount">
        <DialogHeader>
          <DialogTitle>
            {{ t('bots.remoteRuntime.addTitle') }}
          </DialogTitle>
          <DialogDescription>
            {{ t('bots.remoteRuntime.mountDescription') }}
          </DialogDescription>
        </DialogHeader>

        <FormStack class="mt-4">
          <FieldStack
            :label="t('bots.remoteRuntime.computer')"
            :help="mountComputerHelp"
          >
            <Select
              v-model="mountRuntimeId"
              :disabled="runtimesLoading || mountSaving"
            >
              <SelectTrigger class="w-full">
                <SelectValue :placeholder="t('bots.remoteRuntime.selectComputer')" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem
                  v-for="runtime in dialogRuntimeItems"
                  :key="runtime.id"
                  :value="runtime.id"
                >
                  {{ runtime.name }} · {{ runtime.online ? t('runtimes.status.online') : t('runtimes.status.offline') }}
                </SelectItem>
              </SelectContent>
            </Select>
          </FieldStack>
        </FormStack>

        <DialogFooter class="mt-4">
          <Button
            type="button"
            variant="outline"
            :disabled="mountSaving"
            @click="mountDialogOpen = false"
          >
            {{ t('common.cancel') }}
          </Button>
          <Button
            v-if="!runtimesLoading && availableRuntimeItems.length === 0"
            type="button"
            @click="openComputers"
          >
            {{ t('runtimes.connect') }}
          </Button>
          <Button
            v-else
            type="submit"
            :disabled="!canSaveMount"
            :loading="mountSaving"
          >
            {{ t('common.add') }}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>

  <ConfirmDeleteDialog
    :open="!!pendingDeleteTarget"
    :title="t('bots.remoteRuntime.deleteTitle')"
    :description="deleteDescription"
    :cancel-label="t('common.cancel')"
    :confirm-label="t('bots.remoteRuntime.removeFromBot')"
    :loading="deletingTarget"
    @update:open="(open) => { if (!open) pendingDeleteTarget = null }"
    @confirm="deleteTarget"
  />
</template>

<script setup lang="ts">
import { computed, inject, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useMutation, useQuery } from '@pinia/colada'
import {
  deleteBotsByBotIdWorkspaceTargetsByTargetId,
  getBotsByBotIdWorkspaceTargets,
  getUsersMeRuntimes,
  putBotsByBotIdWorkspaceTargetsPrimary,
  putBotsByBotIdWorkspaceTargetsRemotesByRuntimeId,
  type WorkspaceWorkspaceTarget,
} from '@memohai/sdk'
import {
  Badge,
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  toast,
} from '@felinic/ui'
import { MoreHorizontal, Plus, Trash2 } from 'lucide-vue-next'
import {
  DesktopRuntimeKey,
  type DesktopRuntimeState,
} from '@/lib/desktop-shell'
import { ConfirmDeleteDialog, FieldStack, FormStack, InlineLoadingRow, PageShell, SettingsRow, SettingsSection } from '@felinic/ui'
import { resolveApiErrorMessage } from '@/utils/api-error'

const props = defineProps<{
  botId: string
}>()

type ValidWorkspaceTarget = WorkspaceWorkspaceTarget & {
  target_id: string
  kind: string
}

type RuntimeOption = {
  id: string
  name: string
  online: boolean
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
  data: runtimes,
  error: runtimesError,
  isLoading: runtimesLoading,
  refetch: refetchRuntimes,
} = useQuery({
  key: () => ['remote-runtimes'],
  query: async () => {
    const { data } = await getUsersMeRuntimes({ throwOnError: true })
    return data
  },
  refetchOnWindowFocus: true,
})

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

const runtimeItems = computed(() => runtimes.value ?? [])
const mountedRuntimeIds = computed(() => new Set(
  validTargets.value.map(target => target.runtime_id).filter((value): value is string => !!value),
))
const availableRuntimeItems = computed(() => runtimeItems.value.filter(runtime => (
  !!runtime.id && !mountedRuntimeIds.value.has(runtime.id)
)))
const primaryTargetId = computed(() => (
  validTargets.value.find(target => target.primary)?.target_id ?? 'native'
))
const initialLoading = computed(() => workspaceTargetsLoading.value && !workspaceTargetsResponse.value)
const loadFailed = computed(() => !!workspaceTargetsError.value && !workspaceTargetsResponse.value)
const showThisComputerSetup = computed(() => (
  !!desktopRuntimeBridge
  && !!desktopRuntimeState.value
  && !desktopRuntimeState.value.enabled
))

const mountDialogOpen = ref(false)
const mountRuntimeId = ref('')
const pendingDeleteTarget = ref<ValidWorkspaceTarget | null>(null)

const dialogRuntimeItems = computed<RuntimeOption[]>(() => {
  return availableRuntimeItems.value.flatMap((runtime): RuntimeOption[] => (
    runtime.id
      ? [{
          id: runtime.id,
          name: runtimeName(runtime),
          online: runtime.online ?? false,
        }]
      : []
  ))
})

const mountComputerHelp = computed(() => {
  if (runtimesError.value && !runtimes.value) return t('runtimes.loadFailedDescription')
  if (!runtimesLoading.value && availableRuntimeItems.value.length === 0) {
    return t('bots.remoteRuntime.noAvailableComputers')
  }
  return t('bots.remoteRuntime.computerDescription')
})

const canSaveMount = computed(() => (
  !!mountRuntimeId.value
  && !runtimesLoading.value
  && !mountSaving.value
))

const deleteDescription = computed(() => {
  const target = pendingDeleteTarget.value
  if (!target) return ''
  return target.primary
    ? t('bots.remoteRuntime.deletePrimaryDescription', { name: targetName(target) })
    : t('bots.remoteRuntime.deleteDescription', { name: targetName(target) })
})

const { mutateAsync: setPrimaryRequest, isLoading: primarySaving } = useMutation({
  mutation: async (targetId: string) => {
    await putBotsByBotIdWorkspaceTargetsPrimary({
      path: { bot_id: props.botId },
      body: { target_id: targetId },
      throwOnError: true,
    })
  },
})

const { mutateAsync: saveMountRequest, isLoading: mountSaving } = useMutation({
  mutation: async (runtimeId: string) => {
    const { data } = await putBotsByBotIdWorkspaceTargetsRemotesByRuntimeId({
      path: {
        bot_id: props.botId,
        runtime_id: runtimeId,
      },
      throwOnError: true,
    })
    return data
  },
})

const { mutateAsync: deleteTargetRequest, isLoading: deletingTarget } = useMutation({
  mutation: async (targetId: string) => {
    await deleteBotsByBotIdWorkspaceTargetsByTargetId({
      path: {
        bot_id: props.botId,
        target_id: targetId,
      },
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

function openAddDialog(): void {
  mountRuntimeId.value = availableRuntimeItems.value[0]?.id ?? ''
  mountDialogOpen.value = true
}

async function saveMount(): Promise<void> {
  if (!canSaveMount.value) return
  try {
    const saved = await saveMountRequest(mountRuntimeId.value)
    if (saved?.target_id) {
      const index = targetItems.value.findIndex(target => target.target_id === saved.target_id)
      if (index >= 0) {
        targetItems.value = targetItems.value.map((target, itemIndex) => (
          itemIndex === index ? saved : target
        ))
      } else {
        targetItems.value = [...targetItems.value, saved]
      }
    }
    mountDialogOpen.value = false
    toast.success(t('bots.remoteRuntime.addSuccess'))
    void refetchWorkspaceTargets()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.remoteRuntime.saveFailed')))
  }
}

async function deleteTarget(): Promise<void> {
  const target = pendingDeleteTarget.value
  if (!target) return
  try {
    await deleteTargetRequest(target.target_id)
    targetItems.value = targetItems.value
      .filter(item => item.target_id !== target.target_id)
      .map(item => ({
        ...item,
        primary: target.primary ? item.target_id === 'native' : item.primary,
      }))
    pendingDeleteTarget.value = null
    toast.success(t('bots.remoteRuntime.deleteSuccess'))
    void refetchWorkspaceTargets()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('bots.remoteRuntime.deleteFailed')))
  }
}

async function retry(): Promise<void> {
  await Promise.all([refetchWorkspaceTargets(), refetchRuntimes()])
}

function openComputers(): void {
  mountDialogOpen.value = false
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
