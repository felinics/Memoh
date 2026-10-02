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
        >
          <!-- Bot access sits beside the name, as in the Other computers rows,
               so the trailing controls leave the description room for one line. -->
          <template #content>
            <p class="flex min-w-0 items-center gap-2">
              <span class="truncate text-control font-medium text-foreground">{{ desktopRuntimeLabel }}</span>
              <template v-if="desktopRuntimeRegistered">
                <Badge
                  v-if="accessCount(desktopRuntimeRegistered) > 0"
                  variant="secondary"
                  size="sm"
                  class="shrink-0"
                >
                  {{ t('runtimes.botAccess', { count: accessCount(desktopRuntimeRegistered), total: botAccessTotal }) }}
                </Badge>
                <span
                  v-else
                  class="shrink-0 text-xs text-muted-foreground"
                >
                  {{ t('runtimes.noBotAccess') }}
                </span>
              </template>
            </p>
            <p class="mt-0.5 text-body text-muted-foreground">
              {{ desktopRuntimeDescription }}
            </p>
          </template>
          <div class="flex items-center gap-2">
            <span class="flex items-center gap-1.5 text-xs text-muted-foreground">
              <span
                class="size-1.5 rounded-full"
                :class="desktopRuntimeStatusDot"
              />
              {{ desktopRuntimeStatusLabel }}
            </span>
            <Button
              v-if="desktopRuntimeRegistered"
              variant="ghost"
              size="icon-sm"
              :aria-label="t('runtimes.manageAccess')"
              :title="t('runtimes.manageAccess')"
              @click="openAccessDialog(desktopRuntimeRegistered)"
            >
              <Settings class="size-4" />
            </Button>
            <ConfirmPopover
              v-if="desktopRuntimeState.enabled"
              :title="t('runtimes.revokeTitle')"
              :message="desktopRuntimeRemoveMessage"
              :cancel-text="t('common.cancel')"
              :confirm-text="t('runtimes.revoke')"
              variant="destructive"
              @confirm="removeDesktopRuntime"
            >
              <template #trigger>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  :disabled="desktopRuntimeSaving"
                  :aria-label="t('runtimes.revoke')"
                >
                  <Trash2 class="size-4" />
                </Button>
              </template>
            </ConfirmPopover>
            <!-- Not disabled while saving: Desktop reports the new state within
                 milliseconds, so disabling only flashes a dimmed switch and a
                 not-allowed cursor. Clicks in that window are ignored by
                 toggleDesktopRuntime, and Desktop serializes the operations. -->
            <Switch
              :model-value="desktopRuntimeActive"
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
            <div class="flex flex-wrap items-center justify-between gap-3">
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
                  v-if="!runtime.online && runtime.key"
                  variant="outline"
                  size="sm"
                  @click="restoreComputer(runtime)"
                >
                  {{ t('computerConnect.restore') }}
                </Button>
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
    <DialogPanel
      :width="desktopRuntimeStep === 'name' ? 'lg' : 'xl'"
      footer
    >
      <!-- `contents` keeps header / body / footer as the panel's grid rows. -->
      <form
        v-if="desktopRuntimeStep === 'name'"
        class="contents"
        @submit.prevent="submitDesktopRuntimeName"
      >
        <DialogHeader>
          <DialogTitle>{{ t('runtimes.thisComputer.dialogTitle') }}</DialogTitle>
          <DialogDescription>
            {{ t('runtimes.thisComputer.dialogDescription') }}
          </DialogDescription>
        </DialogHeader>

        <FormStack>
          <FormField
            v-slot="{ componentField }"
            name="name"
          >
            <FieldStack :label="t('runtimes.connectDialog.name')">
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

        <DialogFooter>
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
            :loading="desktopRuntimeSaving || !!desktopRuntimeAwaitingId || !!desktopRuntimeGrantingId"
          >
            {{ t('runtimes.thisComputer.confirm') }}
          </Button>
        </DialogFooter>
      </form>

      <!-- Same permissions step as the connect stepper, but every bot starts
           off: this computer exposes the user's own home folder and shell. -->
      <template v-else-if="desktopRuntimeRegistered?.id">
        <DialogHeader class="pr-8">
          <DialogTitle class="break-words">
            {{ desktopRuntimeName }}
          </DialogTitle>
          <DialogDescription>
            {{ t('computerAccess.subtitleRuntime') }}
          </DialogDescription>
        </DialogHeader>

        <DialogBody>
          <ComputerAccessList :runtime="{ id: desktopRuntimeRegistered.id, name: desktopRuntimeName }" />
        </DialogBody>

        <DialogFooter>
          <Button @click="desktopRuntimeDialogOpen = false">
            {{ t('computerConnect.finish') }}
          </Button>
        </DialogFooter>
      </template>
    </DialogPanel>
  </Dialog>

  <ConnectComputerDialog
    v-model:open="connectDialogOpen"
    :credential="connectCredential"
    :existing="reconnectingExisting"
  />

  <BotComputerAccessDialog
    v-model:open="accessDialogOpen"
    :runtime="accessRuntime"
  />
</template>

<script setup lang="ts">
import { computed, inject, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
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
  DialogBody,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
  FormControl,
  FormField,
  Input,
  Switch,
  toast,
} from '@felinic/ui'
import { Plus, Trash2 } from 'lucide-vue-next'
import { SettingsIcon as Settings } from '@memohai/icon/ui'
import { ConfirmPopover, FieldStack, FormStack, InlineLoadingRow, PageShell, SettingsRow, SettingsSection } from '@felinic/ui'
import BotComputerAccessDialog from '@/components/computer/bot-computer-access-dialog.vue'
import ComputerAccessList from '@/components/computer/computer-access-list.vue'
import ConnectComputerDialog from '@/components/computer/connect-computer-dialog.vue'
import { useAccountRuntimes, useComputerAccessActions, useComputerAccessGrants } from '@/components/computer/use-computer-access'
import {
  DesktopRuntimeKey,
  type DesktopRuntimeState,
} from '@/lib/desktop-shell'
import { UserFacingError, resolveApiErrorMessage } from '@/utils/api-error'

const { t } = useI18n()
const desktopRuntimeBridge = inject(DesktopRuntimeKey, undefined)

const {
  runtimes,
  error: runtimesError,
  isLoading: runtimesLoading,
  refetch: refetchRuntimes,
} = useAccountRuntimes()

const { grants } = useComputerAccessGrants()
const { grantBots } = useComputerAccessActions()
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
// Removing clears the bridge's runtimeId before the server revoke and refetch
// finish; until then the old credential is still listed and must not flash
// under Other computers.
const retiringDesktopRuntimeId = ref('')
const runtimeItems = computed(() => (runtimes.value ?? []).filter(runtime => (
  runtime.id !== desktopRuntimeState.value?.runtimeId
  && runtime.id !== retiringDesktopRuntimeId.value
)))

// The server lists a credential only once its first ready connection
// activates it, and grants require that activation, so the access controls for
// this computer appear only when it is in the account list.
const desktopRuntimeRegistered = computed(() => {
  const state = desktopRuntimeState.value
  if (!state?.enabled || !state.runtimeId) return undefined
  return (runtimes.value ?? []).find(runtime => runtime.id === state.runtimeId)
})

// Enabling this computer continues, in the same dialog, to the bot
// permissions step once the desktop daemon's first connection activates the
// credential. As in the connect stepper, every bot starts granted and the
// user only trims.
const desktopRuntimeStep = ref<'name' | 'access'>('name')
const desktopRuntimeAwaitingId = ref('')
// Set while the default grants run; the polling above has stopped by then, so
// repeated runtime list updates cannot start the grants twice.
const desktopRuntimeGrantingId = ref('')

// Poll while waiting instead of relying on the page's visibility-gated
// refresh: the daemon reports "connected" slightly before the server commits
// the activation, and the window may be in the background meanwhile.
watch(desktopRuntimeAwaitingId, (id, _previous, onCleanup) => {
  if (!id) return
  let stopped = false
  let timer: ReturnType<typeof setTimeout> | undefined
  onCleanup(() => {
    stopped = true
    clearTimeout(timer)
  })
  async function poll(): Promise<void> {
    await refetchRuntimes()
    if (!stopped) timer = setTimeout(() => { void poll() }, 1000)
  }
  void poll()
})
watch(() => desktopRuntimeState.value?.status, (status) => {
  if (!desktopRuntimeAwaitingId.value) return
  if (status === 'error' || status === 'disabled') {
    // The card shows the connection error; there is nothing to grant yet.
    desktopRuntimeAwaitingId.value = ''
    desktopRuntimeDialogOpen.value = false
  }
})
watch(desktopRuntimeRegistered, async (runtime) => {
  const runtimeId = runtime?.id
  if (!runtimeId || runtimeId !== desktopRuntimeAwaitingId.value) return
  desktopRuntimeAwaitingId.value = ''
  desktopRuntimeGrantingId.value = runtimeId
  // The submit button keeps spinning until the grants finish, so the access
  // step opens with every switch already on.
  const botIds = (botsData.value?.items ?? []).flatMap(bot => (bot.id ? [bot.id] : []))
  const failures = await grantBots(runtimeId, botIds)
  if (failures.length > 0) toast.error(t('computerAccess.updateFailed'))
  // Closing the dialog meanwhile clears the granting id; stay closed then.
  if (desktopRuntimeGrantingId.value !== runtimeId) return
  desktopRuntimeGrantingId.value = ''
  desktopRuntimeStep.value = 'access'
  desktopRuntimeDialogOpen.value = true
})

const desktopRuntimeName = computed(() => {
  const state = desktopRuntimeState.value
  if (!state) return ''
  if (state.runtimeName) return state.runtimeName
  const registered = (runtimes.value ?? []).find(runtime => runtime.id === state.runtimeId)
  return registered?.name || state.deviceName || t('runtimes.thisComputer.fallbackName')
})

// Paused keeps the credential and Bot grants; only the session is stopped.
// Desktop reports a paused computer as enabled with status "stopped".
const desktopRuntimeActive = computed(() => (
  !!desktopRuntimeState.value?.enabled && desktopRuntimeState.value.status !== 'stopped'
))

const desktopRuntimeLabel = computed(() => (
  desktopRuntimeState.value?.enabled
    ? desktopRuntimeName.value
    : t('runtimes.thisComputer.allow')
))

const desktopRuntimeDescription = computed(() => (
  desktopRuntimeState.value?.error
  || (!desktopRuntimeState.value?.enabled
    ? t('runtimes.thisComputer.description')
    : desktopRuntimeActive.value
      ? t('runtimes.thisComputer.enabledDescription')
      : t('runtimes.thisComputer.pausedDescription'))
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
const connectCredential = ref<UserruntimeRuntime | null>(null)
const reconnectingExisting = ref(false)

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
  if (!bridge || !current || desktopRuntimeSaving.value || enabled === desktopRuntimeActive.value) return

  // Only a computer that was never set up (or was removed) needs a name and
  // fresh Bot permissions; a paused one resumes with everything it had.
  if (enabled && !current.enabled) {
    connectForm.resetForm({ values: { name: current.deviceName } })
    desktopRuntimeDialogOpen.value = true
    return
  }
  // Desktop builds without pause support can only turn this computer off by
  // removing it.
  if (!bridge.setRuntimePaused) {
    if (!enabled) await removeDesktopRuntime()
    return
  }

  desktopRuntimeSaving.value = true
  try {
    desktopRuntimeState.value = await bridge.setRuntimePaused(!enabled)
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t(enabled
      ? 'runtimes.thisComputer.enableFailed'
      : 'runtimes.thisComputer.disableFailed')))
  } finally {
    desktopRuntimeSaving.value = false
  }
}

// Removing deletes the stored credential and revokes it on the server, which
// also drops every Bot's access to this computer.
async function removeDesktopRuntime(): Promise<void> {
  const bridge = desktopRuntimeBridge
  const current = desktopRuntimeState.value
  if (!bridge || !current || desktopRuntimeSaving.value) return

  desktopRuntimeSaving.value = true
  const runtimeId = current.runtimeId
  retiringDesktopRuntimeId.value = runtimeId ?? ''
  try {
    desktopRuntimeState.value = await bridge.configureRuntime(null)
    if (runtimeId) {
      await revokeRuntimeCredential(runtimeId)
    }
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('runtimes.revokeFailed')))
  } finally {
    await refetchRuntimes()
    retiringDesktopRuntimeId.value = ''
    desktopRuntimeSaving.value = false
  }
}

const desktopRuntimeRemoveMessage = computed(() => {
  const registered = desktopRuntimeRegistered.value
  if (registered) return revokeMessage(registered)
  return t('runtimes.revokeDescription', { name: desktopRuntimeName.value })
})

const submitDesktopRuntimeName = connectForm.handleSubmit(values => enableDesktopRuntime(values.name.trim()))

async function enableDesktopRuntime(name: string): Promise<void> {
  const bridge = desktopRuntimeBridge
  if (!bridge || desktopRuntimeSaving.value) return

  let created: UserruntimeRuntime | undefined
  desktopRuntimeSaving.value = true
  try {
    created = await createRuntime(name)
    if (!created.id || !created.key) {
      throw new UserFacingError(t('runtimes.thisComputer.invalidCredential'))
    }
    // Armed before the bridge call: the daemon can connect and a poll can list
    // the runtime before configureRuntime resolves.
    desktopRuntimeAwaitingId.value = created.id
    desktopRuntimeState.value = await bridge.configureRuntime({
      runtimeId: created.id,
      name,
      key: created.key,
      teamId: created.team_id?.trim() || undefined,
    })
    created = undefined
    void refetchRuntimes()
  } catch (error) {
    desktopRuntimeAwaitingId.value = ''
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
}

// One click creates the credential and the stepper takes over (command →
// connected → permissions). The computer adopts its machine hostname on
// first connect (handshake backfill).
async function startConnect(): Promise<void> {
  if (creatingRuntime.value) return
  try {
    connectCredential.value = await createRuntime('')
    reconnectingExisting.value = false
    connectDialogOpen.value = true
    void refetchRuntimes()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('runtimes.connectDialog.createFailed')))
  }
}

function restoreComputer(runtime: UserruntimeRuntime): void {
  connectCredential.value = runtime
  reconnectingExisting.value = true
  connectDialogOpen.value = true
}

// Consume the add entry from chat once; reloading the page must not create
// another credential. The existing dialog owns cancellation cleanup.
const route = useRoute()
const router = useRouter()
watch(() => route.query.connect, async (connect) => {
  if (route.name !== 'runtimes' || connect !== '1') return
  const query = { ...route.query }
  delete query.connect
  await router.replace({ query })
  await startConnect()
}, { immediate: true })

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
  connectCredential.value = null
  connectForm.resetForm({ values: { name: '' } })
})

watch(desktopRuntimeDialogOpen, (open) => {
  if (open) return
  // Closing while still waiting keeps this computer enabled; its permissions
  // stay reachable from the card's gear once it connects.
  desktopRuntimeAwaitingId.value = ''
  desktopRuntimeGrantingId.value = ''
  desktopRuntimeStep.value = 'name'
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
