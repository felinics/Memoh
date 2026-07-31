<template>
  <PageShell :title="t('runtimes.title')">
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

      <SettingsSection :title="desktopRuntimeBridge ? t('runtimes.otherComputers') : t('runtimes.computers')">
        <template #actions>
          <Button
            size="sm"
            @click="connectDialogOpen = true"
          >
            <Plus class="size-4" />
            {{ desktopRuntimeBridge ? t('runtimes.connectOther') : t('runtimes.connect') }}
          </Button>
        </template>

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
          align="start"
          stack="sm"
        >
          <template #leading>
            <Laptop class="size-4 text-muted-foreground" />
          </template>
          <template #content>
            <p class="truncate text-sm font-medium text-foreground">
              {{ runtime.name }}
            </p>
            <p class="mt-0.5 truncate text-xs text-muted-foreground">
              {{ runtimeSummary(runtime) }}
            </p>
            <div
              v-if="!runtime.online && runtime.key"
              class="mt-3 flex min-w-0 items-start gap-2 rounded-md border border-border bg-muted-soft p-2"
            >
              <code class="min-w-0 flex-1 break-all font-mono text-xs leading-relaxed text-foreground">{{ runtimeCommand(runtime) }}</code>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                class="shrink-0"
                :aria-label="t('runtimes.copyCommand')"
                @click="copyCommand(runtimeCommand(runtime))"
              >
                <Copy class="size-4" />
              </Button>
            </div>
          </template>
          <div class="flex items-center gap-2">
            <span class="flex items-center gap-1.5 text-xs text-muted-foreground">
              <span
                class="size-1.5 rounded-full"
                :class="runtime.online ? 'bg-success' : 'bg-accent-gray-border'"
              />
              {{ runtime.online ? t('runtimes.status.online') : t('runtimes.status.offline') }}
            </span>
            <ConfirmPopover
              :title="t('runtimes.revokeTitle')"
              :message="t('runtimes.revokeDescription', { name: runtime.name })"
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

  <Dialog v-model:open="connectDialogOpen">
    <DialogContent>
      <form
        v-if="!createdCredential"
        @submit.prevent="createRuntimeCredential"
      >
        <DialogHeader>
          <DialogTitle>{{ t('runtimes.connectDialog.title') }}</DialogTitle>
          <DialogDescription>
            {{ t('runtimes.connectDialog.description') }}
          </DialogDescription>
        </DialogHeader>

        <FormStack class="mt-4">
          <FormField
            v-slot="{ componentField }"
            name="name"
          >
            <FieldStack
              :label="t('runtimes.connectDialog.name')"
              :help="t('runtimes.connectDialog.nameHelp')"
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
            :disabled="creatingRuntime"
            @click="connectDialogOpen = false"
          >
            {{ t('common.cancel') }}
          </Button>
          <Button
            type="submit"
            :loading="creatingRuntime"
          >
            {{ t('runtimes.connectDialog.create') }}
          </Button>
        </DialogFooter>
      </form>

      <template v-else>
        <DialogHeader>
          <DialogTitle>{{ t('runtimes.connectDialog.runTitle') }}</DialogTitle>
          <DialogDescription>
            {{ t('runtimes.connectDialog.runDescription') }}
          </DialogDescription>
        </DialogHeader>

        <div class="mt-4 space-y-3">
          <div class="relative min-w-0 overflow-hidden rounded-md border border-border bg-muted-soft">
            <pre
              class="max-h-40 min-w-0 overflow-auto whitespace-pre-wrap break-all p-3 pr-12 font-mono text-xs leading-relaxed text-foreground"
              :aria-label="t('runtimes.connectDialog.command')"
            ><code>{{ connectCommand }}</code></pre>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              class="absolute right-2 top-2"
              :aria-label="t('common.copy')"
              @click="copyCommand(connectCommand)"
            >
              <Copy class="size-4" />
            </Button>
          </div>
          <p class="text-xs leading-relaxed text-muted-foreground">
            {{ t('runtimes.connectDialog.keyOnce') }}
          </p>
        </div>

        <DialogFooter class="mt-4">
          <Button @click="connectDialogOpen = false">
            {{ t('runtimes.connectDialog.done') }}
          </Button>
        </DialogFooter>
      </template>
    </DialogContent>
  </Dialog>
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
  getUsersMeRuntimes,
  postUsersMeRuntimes,
  type UserruntimeRuntime,
} from '@memohai/sdk'
import {
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
import { Copy, Laptop, Plus, Trash2 } from 'lucide-vue-next'
import { ConfirmPopover, FieldStack, FormStack, InlineLoadingRow, PageShell, SettingsRow, SettingsSection, useClipboard } from '@felinic/ui'
import { sdkApiBaseUrl } from '@/lib/api-client'
import {
  DesktopRuntimeKey,
  type DesktopRuntimeState,
} from '@/lib/desktop-shell'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { buildRuntimeConnectCommand } from './command'

const { t } = useI18n()
const { copyText } = useClipboard()
const desktopRuntimeBridge = inject(DesktopRuntimeKey, undefined)

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
})

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

function commandForCredential(
  credential: Pick<UserruntimeRuntime, 'key' | 'team_id'> | null | undefined,
): string {
  return buildRuntimeConnectCommand(sdkApiBaseUrl(), credential)
}

const connectCommand = computed(() => commandForCredential(createdCredential.value))

function runtimeCommand(runtime: UserruntimeRuntime): string {
  return commandForCredential(runtime)
}

const createRuntimeCredential = connectForm.handleSubmit(async (values) => {
  try {
    createdCredential.value = await createRuntime(values.name.trim())
    void refetchRuntimes()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('runtimes.connectDialog.createFailed')))
  }
})

async function copyCommand(command: string): Promise<void> {
  const copied = await copyText(command)
  if (copied) {
    toast.success(t('common.copied'))
  } else {
    toast.error(t('common.copyFailed'))
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

function runtimeSummary(runtime: UserruntimeRuntime): string {
  if (!runtime.online) return t('runtimes.waitingForConnection')
  const machine = [runtime.hostname, runtime.os, runtime.arch].filter(Boolean).join(' · ')
  return machine || t('runtimes.connected')
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
