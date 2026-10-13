<template>
  <Dialog
    :open="open"
    @update:open="onOpenChange"
  >
    <DialogScrollContent>
      <!-- Step 1: copy and run the command; replacing a binding is opt-in. -->
      <template v-if="step === 'command'">
        <DialogHeader class="pr-8">
          <DialogTitle>{{ existing ? t('computerConnect.restore') : t('computerConnect.title') }}</DialogTitle>
          <DialogDescription>
            {{ existing
              ? t('computerConnect.existingDescription', { name: credential?.name })
              : t('computerConnect.commandDescription') }}
          </DialogDescription>
        </DialogHeader>

        <SettingsSection bordered>
          <SettingsRow
            :label="t('computerConnect.replaceAction')"
            :description="t('computerConnect.replaceDescription')"
          >
            <Switch
              v-model="replaceBinding"
              :aria-label="t('computerConnect.replaceAction')"
            />
          </SettingsRow>
        </SettingsSection>

        <!-- wrap: the command is one long line with an unbroken key; it must
             be readable in full without widening the dialog. -->
        <CodePanel
          wrap
          :code="command"
          :copy-label="t('common.copy')"
          :copied-label="t('common.copied')"
          :copy-failed-message="t('common.copyFailed')"
          :aria-label="t('computerConnect.commandLabel')"
        />

        <CalloutBanner
          tone="neutral"
          size="sm"
          :description="t('computerConnect.waiting')"
        />

        <DialogFooter>
          <Button
            variant="outline"
            @click="close"
          >
            {{ t('common.cancel') }}
          </Button>
        </DialogFooter>
      </template>

      <!-- Step 2: permissions. Every bot starts granted; the user only trims.
           Reached automatically the moment the machine comes online — a bare
           "connected" confirmation step earns nothing. -->
      <template v-if="step === 'access'">
        <DialogHeader class="pr-8">
          <DialogTitle class="break-words">
            {{ adoptedName }}
          </DialogTitle>
          <DialogDescription>
            {{ t('computerAccess.subtitleRuntime') }}
          </DialogDescription>
        </DialogHeader>

        <ComputerAccessList :runtime="{ id: runtimeId, name: adoptedName }" />

        <DialogFooter>
          <Button @click="close">
            {{ t('computerConnect.finish') }}
          </Button>
        </DialogFooter>
      </template>
    </DialogScrollContent>
  </Dialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery } from '@pinia/colada'
import { deleteUsersMeRuntimesById, type UserruntimeRuntime } from '@memohai/sdk'
import { getBotsQuery } from '@memohai/sdk/colada'
import {
  Button,
  CalloutBanner,
  CodePanel,
  Dialog,
  DialogScrollContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  SettingsRow,
  SettingsSection,
  Switch,
  toast,
} from '@felinic/ui'
import { sdkApiBaseUrl } from '@/lib/api-client'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { buildRuntimeConnectCommand } from '@/pages/runtimes/command'
import ComputerAccessList from './computer-access-list.vue'
import { useAccountRuntimes, useComputerAccessActions } from './use-computer-access'

// The connect stepper, one continuous dialog: command (+ waiting) → connected
// → bot permissions. Only new credentials are revoked on cancellation;
// reconnecting a historical computer keeps its identity and permissions.
const props = defineProps<{
  credential: UserruntimeRuntime | null
  existing?: boolean
}>()

const open = defineModel<boolean>('open', { default: false })

const { t } = useI18n()

const step = ref<'command' | 'access'>('command')
const replaceBinding = ref(false)
const runtimeId = computed(() => props.credential?.id ?? '')
const command = computed(() => buildRuntimeConnectCommand(
  sdkApiBaseUrl(), props.credential, replaceBinding.value ? 'replace' : 'connect',
))

const { runtimes, refetch: refetchRuntimes } = useAccountRuntimes()
const { data: botsData } = useQuery(getBotsQuery())
const { grantBots } = useComputerAccessActions()

const adoptedName = ref('')

// Every attempt starts without replacement, including reopening the same computer.
watch([runtimeId, open], ([id, isOpen]) => {
  if (!id || !isOpen) return
  step.value = 'command'
  replaceBinding.value = false
  adoptedName.value = ''
}, { immediate: true })

// New computers proceed to permissions; restored computers return to the list
// without changing their grants.
const connectedRuntime = computed(() => (
  (runtimes.value ?? []).find(runtime => runtime.id === runtimeId.value && runtime.online)
))
// Own connection refresh here rather than relying on polling in the caller.
// Refetch resolves with Colada's error state on failure, so the next tick can
// recover without overlapping requests or depending on browser focus events.
watch([open, runtimeId, step, () => !!connectedRuntime.value], ([isOpen, id, currentStep, online], _previous, onCleanup) => {
  if (!isOpen || !id || currentStep !== 'command' || online) return
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
}, { immediate: true })

const granting = ref(false)
// A newly opened connection may become online while the previous grant is
// still running. Re-check it when that grant releases the shared busy state.
watch([connectedRuntime, granting, open], async ([runtime]) => {
  if (!open.value || !runtime || step.value !== 'command' || granting.value) return
  adoptedName.value = runtime.name || runtime.hostname || runtimeId.value
  toast.success(t('runtimes.computerOnline', { name: adoptedName.value }))
  if (props.existing) {
    close()
    return
  }
  const connectedId = runtimeId.value
  granting.value = true
  try {
    await grantAllBots(connectedId)
    if (open.value && runtimeId.value === connectedId) step.value = 'access'
  } finally {
    granting.value = false
  }
})

async function grantAllBots(connectedId: string): Promise<void> {
  const botIds = (botsData.value?.items ?? []).flatMap(bot => (bot.id ? [bot.id] : []))
  const failures = await grantBots(connectedId, botIds)
  if (failures.length > 0) {
    toast.error(t('computerAccess.updateFailed'))
  }
}

function close(): void {
  // Closing at the command step abandons the attempt: the key was already
  // shown (maybe copied), and a never-connected placeholder is filtered out of
  // the computers list — left alone it would be an active credential the user
  // can neither see nor revoke. Capture the id BEFORE open=false: the parent
  // clears the credential on close and runtimeId would recompute to ''.
  const abandonedId = step.value === 'command' && !props.existing ? runtimeId.value : ''
  open.value = false
  if (abandonedId) void revokeAbandonedCredential(abandonedId)
}

const revoking = ref(false)
async function revokeAbandonedCredential(abandonedId: string): Promise<void> {
  if (revoking.value) return
  revoking.value = true
  try {
    // Re-check before deleting: if the machine came online in the same instant
    // the user cancelled, it is a real (visible, manageable) computer now and
    // must not be kicked.
    await refetchRuntimes()
    const cameOnline = (runtimes.value ?? []).some(runtime => runtime.id === abandonedId && runtime.online)
    if (cameOnline) return
    await deleteUsersMeRuntimesById({ path: { id: abandonedId }, throwOnError: true })
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('runtimes.revokeFailed')))
  } finally {
    revoking.value = false
    void refetchRuntimes()
  }
}

function onOpenChange(next: boolean): void {
  if (!next) close()
}
</script>
