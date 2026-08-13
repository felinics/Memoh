<template>
  <PageShell :title="t('runtimes.title')">
    <template #actions>
      <Button
        :loading="creatingRuntime"
        @click="startConnect"
      >
        <Plus class="size-4" />
        {{ desktopRuntimeBridge ? t('runtimes.connectOther') : t('runtimes.connect') }}
      </Button>
    </template>
    <div class="space-y-8">
      <SettingsSection
        v-if="desktopRuntimeBridge"
        :title="t('runtimes.thisComputer.title')"
      >
        <InlineLoadingRow
          v-if="desktopRuntimeLoading"
          surface="card-row"
        >
          {{ t('runtimes.thisComputer.loading') }}
        </InlineLoadingRow>

        <SettingsRow
          v-else-if="desktopRuntimeLoadFailed"
          :label="t('runtimes.thisComputer.loadFailed')"
          :description="t('runtimes.thisComputer.loadFailedDescription')"
        >
          <Button
            variant="outline"
            size="sm"
            @click="loadDesktopRuntimeState"
          >
            {{ t('runtimes.retry') }}
          </Button>
        </SettingsRow>

        <SettingsRow
          v-else-if="desktopRuntimeState"
          stack="sm"
          :label="desktopRuntimeLabel"
          :description="desktopRuntimeDescription"
        >
          <div class="flex items-center gap-3">
            <span class="flex items-center gap-1.5 text-xs text-muted-foreground">
              <span
                class="size-1.5 rounded-full"
                :class="desktopRuntimeStatusDot"
              />
              {{ desktopRuntimeStatusLabel }}
            </span>
            <Switch
              :model-value="desktopRuntimeState.enabled"
              :disabled="desktopRuntimeSaving"
              :aria-label="t('runtimes.thisComputer.allow')"
              @update:model-value="toggleDesktopRuntime"
            />
          </div>
        </SettingsRow>
      </SettingsSection>

      <!-- Single-group page: PageShell owns the title + action, so this
           section carries no label of its own (only the desktop split case
           names it "Other computers"). -->
      <SettingsSection :title="desktopRuntimeBridge ? t('runtimes.otherComputers') : ''">
        <InlineLoadingRow
          v-if="runtimesLoading && runtimes === undefined"
          surface="card-row"
        >
          {{ t('runtimes.loadingComputers') }}
        </InlineLoadingRow>

        <SettingsRow
          v-else-if="runtimesError && runtimes === undefined"
          :label="t('runtimes.loadFailed')"
          :description="t('runtimes.loadFailedDescription')"
        >
          <Button
            variant="outline"
            size="sm"
            @click="refetchRuntimes()"
          >
            {{ t('runtimes.retry') }}
          </Button>
        </SettingsRow>

        <div
          v-else-if="runtimeItems.length === 0"
          class="px-6 py-8 text-center"
        >
          <p class="text-sm font-medium text-foreground">
            {{ desktopRuntimeBridge ? t('runtimes.emptyOtherTitle') : t('runtimes.emptyTitle') }}
          </p>
          <p class="mx-auto mt-1 max-w-md text-xs leading-relaxed text-muted-foreground">
            {{ desktopRuntimeBridge ? t('runtimes.emptyOtherDescription') : t('runtimes.emptyDescription') }}
          </p>
        </div>

        <SettingsRow
          v-for="runtime in runtimeItems"
          v-else
          :key="runtime.id"
          stack="sm"
        >
          <template #content>
            <div class="flex items-center justify-between gap-3">
              <p class="flex min-w-0 items-center gap-2 text-sm">
                <span class="truncate font-medium text-foreground">{{ runtime.name }}</span>
                <Badge
                  v-if="accessCount(runtime) > 0"
                  variant="secondary"
                  size="sm"
                  class="shrink-0"
                >
                  {{ t('runtimes.botAccess', { count: accessCount(runtime), total: botAccessTotal }) }}
                </Badge>
                <span
                  v-else
                  class="shrink-0 text-xs text-muted-foreground"
                >
                  {{ t('runtimes.noBotAccess') }}
                </span>
              </p>
              <div class="flex shrink-0 items-center gap-2">
                <span class="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <span
                    class="size-1.5 rounded-full"
                    :class="runtime.online ? 'bg-success' : 'bg-accent-gray-border'"
                  />
                  {{ runtime.online ? t('runtimes.status.online') : t('runtimes.status.offline') }}
                </span>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  :aria-label="t('runtimes.manageAccess')"
                  :title="t('runtimes.manageAccess')"
                  @click="openAccessDialog(runtime)"
                >
                  <Settings class="size-4" />
                </Button>
                <ConfirmPopover
                  :title="t('runtimes.revokeTitle')"
                  :message="revokeMessage(runtime)"
                  :cancel-text="t('common.cancel')"
                  :confirm-text="t('runtimes.revoke')"
                  variant="destructive"
                  @confirm="revokeRuntime(runtime)"
                >
                  <template #trigger>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      :aria-label="t('runtimes.revoke')"
                    >
                      <Trash2 class="size-4" />
                    </Button>
                  </template>
                </ConfirmPopover>
              </div>
            </div>
          </template>
        </SettingsRow>
      </SettingsSection>
    </div>
  </PageShell>

  <Dialog v-model:open="desktopRuntimeDialogOpen">
    <DialogContent>
      <form @submit.prevent="enableDesktopRuntime">
        <DialogHeader>
          <DialogTitle>{{ t('runtimes.thisComputer.dialogTitle') }}</DialogTitle>
          <DialogDescription>
            {{ t('runtimes.thisComputer.dialogDescription') }}
          </DialogDescription>
        </DialogHeader>

        <FormStack class="mt-4">
          <FormField
            v-slot="{ componentField }"
            name="name"
          >
            <FieldStack
              :label="t('runtimes.connectDialog.name')"
              :help="t('runtimes.thisComputer.nameHelp')"
            >
              <FormControl>
                <Input
                  v-bind="componentField"
                  autofocus
                  autocomplete="off"
                  :placeholder="t('runtimes.connectDialog.namePlaceholder')"
                />
              </FormControl>
            </FieldStack>
          </FormField>
        </FormStack>

        <DialogFooter class="mt-4">
          <Button
            type="button"
            variant="outline"
            :disabled="desktopRuntimeSaving"
            @click="desktopRuntimeDialogOpen = false"
          >
            {{ t('common.cancel') }}
          </Button>
          <Button
            type="submit"
            :loading="desktopRuntimeSaving"
          >
            {{ t('runtimes.thisComputer.confirm') }}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>

  <ConnectComputerDialog
    v-model:open="connectDialogOpen"
    :credential="createdCredential"
  />

  <BotComputerAccessDialog
    v-model:open="accessDialogOpen"
    :runtime="accessRuntime"
  />
</template>

<script setup lang="ts">
import { computed, inject, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useForm } from 'vee-validate'
import { toTypedSchema } from '@vee-validate/zod'
import z from 'zod'
import { useMutation, useQuery } from '@pinia/colada'
import {
  deleteUsersMeRuntimesById,
  postUsersMeRuntimes,
  type UserruntimeRuntime,
} from '@memohai/sdk'
import { getBotsQuery } from '@memohai/sdk/colada'
import {
  Badge,
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  FormControl,
  FormField,
  Input,
  Switch,
  toast,
} from '@felinic/ui'
import { Plus, Settings, Trash2 } from 'lucide-vue-next'
import { ConfirmPopover, FieldStack, FormStack, InlineLoadingRow, PageShell, SettingsRow, SettingsSection } from '@felinic/ui'
import BotComputerAccessDialog from '@/components/computer/bot-computer-access-dialog.vue'
import ConnectComputerDialog from '@/components/computer/connect-computer-dialog.vue'
import { useAccountRuntimes, useComputerAccessGrants } from '@/components/computer/use-computer-access'
import {
  DesktopRuntimeKey,
  type DesktopRuntimeState,
} from '@/lib/desktop-shell'
import { resolveApiErrorMessage } from '@/utils/api-error'

const { t } = useI18n()
const desktopRuntimeBridge = inject(DesktopRuntimeKey, undefined)

const {
  runtimes,
  error: runtimesError,
  isLoading: runtimesLoading,
  refetch: refetchRuntimes,
} = useAccountRuntimes()

const { grants } = useComputerAccessGrants()
const { data: botsData } = useQuery(getBotsQuery())
const botAccessTotal = computed(() => botsData.value?.items?.length ?? 0)

function accessCount(runtime: UserruntimeRuntime): number {
  return grants.value.filter(grant => grant.runtime_id === runtime.id).length
}

const accessDialogOpen = ref(false)
// The dialog's subject is just an id: the display name resolves live from the
// runtimes list, so a handshake backfill (default name → device hostname)
// re-titles the dialog while it is open.
const accessRuntimeId = ref('')
const accessRuntime = computed<{ id: string, name: string } | null>(() => {
  const id = accessRuntimeId.value
  if (!id) return null
  const runtime = (runtimes.value ?? []).find(item => item.id === id)
  return { id, name: runtime?.name || runtime?.hostname || id }
})

function openAccessDialog(runtime: UserruntimeRuntime): void {
  if (!runtime.id) return
  accessRuntimeId.value = runtime.id
  accessDialogOpen.value = true
}

function revokeMessage(runtime: UserruntimeRuntime): string {
  const base = t('runtimes.revokeDescription', { name: runtime.name })
  const count = accessCount(runtime)
  return count > 0 ? `${base} ${t('runtimes.revokeAccessWarning', { count })}` : base
}

const desktopRuntimeState = ref<DesktopRuntimeState>()
const desktopRuntimeLoading = ref(!!desktopRuntimeBridge)
const desktopRuntimeLoadFailed = ref(false)
const desktopRuntimeSaving = ref(false)
const desktopRuntimeDialogOpen = ref(false)
const runtimeItems = computed(() => (runtimes.value ?? []).filter(runtime => (
  !desktopRuntimeState.value?.runtimeId
  || runtime.id !== desktopRuntimeState.value.runtimeId
)))

const desktopRuntimeName = computed(() => {
  const state = desktopRuntimeState.value
  if (!state) return ''
  if (state.runtimeName) return state.runtimeName
  const registered = (runtimes.value ?? []).find(runtime => runtime.id === state.runtimeId)
  return registered?.name || state.deviceName || t('runtimes.thisComputer.fallbackName')
})

const desktopRuntimeLabel = computed(() => (
  desktopRuntimeState.value?.enabled
    ? desktopRuntimeName.value
    : t('runtimes.thisComputer.allow')
))

const desktopRuntimeDescription = computed(() => (
  desktopRuntimeState.value?.error
  || (desktopRuntimeState.value?.enabled
    ? t('runtimes.thisComputer.enabledDescription', { name: desktopRuntimeName.value })
    : t('runtimes.thisComputer.description'))
))

const desktopRuntimeStatusLabel = computed(() => {
  switch (desktopRuntimeState.value?.status) {
    case 'connected':
      return t('runtimes.thisComputer.status.connected')
    case 'connecting':
      return t('runtimes.thisComputer.status.connecting')
    case 'disconnected':
      return t('runtimes.thisComputer.status.disconnected')
    case 'stopped':
      return t('runtimes.thisComputer.status.stopped')
    case 'error':
      return t('runtimes.thisComputer.status.error')
    default:
      return t('runtimes.thisComputer.status.disabled')
  }
})

const desktopRuntimeStatusDot = computed(() => {
  switch (desktopRuntimeState.value?.status) {
    case 'connected':
      return 'bg-success'
    case 'error':
      return 'bg-destructive'
    default:
      return 'bg-accent-gray-border'
  }
})

const connectDialogOpen = ref(false)
const createdCredential = ref<UserruntimeRuntime | null>(null)

const connectSchema = toTypedSchema(z.object({
  name: z.string().trim().min(1, t('runtimes.connectDialog.nameRequired')),
}))
const connectForm = useForm({ validationSchema: connectSchema, initialValues: { name: '' } })

const { mutateAsync: createRuntime, isLoading: creatingRuntime } = useMutation({
  mutation: async (name: string) => {
    const { data } = await postUsersMeRuntimes({
      body: { name },
      throwOnError: true,
    })
    return data
  },
})

const { mutateAsync: revokeRuntimeCredential } = useMutation({
  mutation: async (runtimeID: string) => {
    await deleteUsersMeRuntimesById({ path: { id: runtimeID }, throwOnError: true })
  },
})

async function loadDesktopRuntimeState(): Promise<void> {
  if (!desktopRuntimeBridge) return
  desktopRuntimeLoading.value = true
  desktopRuntimeLoadFailed.value = false
  try {
    desktopRuntimeState.value = await desktopRuntimeBridge.runtimeState()
  } catch {
    desktopRuntimeLoadFailed.value = true
  } finally {
    desktopRuntimeLoading.value = false
  }
}

async function toggleDesktopRuntime(enabled: boolean): Promise<void> {
  const bridge = desktopRuntimeBridge
  const current = desktopRuntimeState.value
  if (!bridge || !current || desktopRuntimeSaving.value || enabled === current.enabled) return

  if (enabled) {
    connectForm.resetForm({ values: { name: current.deviceName } })
    desktopRuntimeDialogOpen.value = true
    return
  }

  desktopRuntimeSaving.value = true
  const runtimeId = current.runtimeId
  try {
    desktopRuntimeState.value = await bridge.configureRuntime(null)
    if (runtimeId) {
      await revokeRuntimeCredential(runtimeId)
    }
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('runtimes.thisComputer.disableFailed')))
  } finally {
    void refetchRuntimes()
    desktopRuntimeSaving.value = false
  }
}

const enableDesktopRuntime = connectForm.handleSubmit(async (values) => {
  const bridge = desktopRuntimeBridge
  if (!bridge || desktopRuntimeSaving.value) return

  const name = values.name.trim()
  let created: UserruntimeRuntime | undefined
  desktopRuntimeSaving.value = true
  try {
    created = await createRuntime(name)
    if (!created.id || !created.key) {
      throw new Error(t('runtimes.thisComputer.invalidCredential'))
    }
    desktopRuntimeState.value = await bridge.configureRuntime({
      runtimeId: created.id,
      name,
      key: created.key,
      teamId: created.team_id?.trim() || undefined,
    })
    created = undefined
    desktopRuntimeDialogOpen.value = false
    void refetchRuntimes()
  } catch (error) {
    if (created?.id) {
      try {
        await revokeRuntimeCredential(created.id)
      } catch {
        // Best effort: a failed cleanup remains visible under Other computers
        // so the user can disconnect it explicitly.
      }
    }
    void refetchRuntimes()
    toast.error(resolveApiErrorMessage(error, t('runtimes.thisComputer.enableFailed')))
  } finally {
    desktopRuntimeSaving.value = false
  }
})

// One click creates the credential and the stepper takes over (command →
// connected → permissions). The computer adopts its machine hostname on
// first connect (handshake backfill).
async function startConnect(): Promise<void> {
  if (creatingRuntime.value) return
  try {
    createdCredential.value = await createRuntime('')
    connectDialogOpen.value = true
    void refetchRuntimes()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('runtimes.connectDialog.createFailed')))
  }
}

async function revokeRuntime(runtime: UserruntimeRuntime): Promise<void> {
  if (!runtime.id) return
  try {
    await revokeRuntimeCredential(runtime.id)
    await refetchRuntimes()
    toast.success(t('runtimes.revokeSuccess'))
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('runtimes.revokeFailed')))
  }
}

watch(connectDialogOpen, (open) => {
  if (open) return
  createdCredential.value = null
  connectForm.resetForm({ values: { name: '' } })
})

watch(desktopRuntimeDialogOpen, (open) => {
  if (open) return
  connectForm.resetForm({ values: { name: '' } })
})

let pollTimer: number | undefined
let unsubscribeDesktopRuntime: (() => void) | undefined
function refreshVisibleRuntimes(): void {
  if (document.visibilityState === 'visible') void refetchRuntimes()
}

onMounted(() => {
  if (desktopRuntimeBridge) {
    unsubscribeDesktopRuntime = desktopRuntimeBridge.onRuntimeStateChanged((state) => {
      desktopRuntimeState.value = state
      desktopRuntimeLoading.value = false
      desktopRuntimeLoadFailed.value = false
    })
    void loadDesktopRuntimeState()
  }
  pollTimer = window.setInterval(refreshVisibleRuntimes, 5000)
  document.addEventListener('visibilitychange', refreshVisibleRuntimes)
})

onBeforeUnmount(() => {
  unsubscribeDesktopRuntime?.()
  if (pollTimer !== undefined) window.clearInterval(pollTimer)
  document.removeEventListener('visibilitychange', refreshVisibleRuntimes)
})
</script>
