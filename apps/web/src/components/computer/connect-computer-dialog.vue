<template>
  <Dialog
    :open="open"
    @update:open="onOpenChange"
  >
    <DialogContent>
      <!-- Step 1: the command. One action — copy and run it. -->
      <template v-if="step === 'command'">
        <DialogHeader class="pr-8">
          <DialogTitle>{{ t('computerConnect.title') }}</DialogTitle>
        </DialogHeader>

        <div>
          <p class="text-sm text-foreground">
            {{ t('computerConnect.commandDescription') }}
          </p>
          <p class="mt-1.5 text-xs leading-relaxed text-muted-foreground">
            {{ t('computerConnect.trustNote') }}
          </p>
          <!-- Same surface as the chat code block: white card + one hairline,
               not the beige muted-soft fill. -->
          <div class="relative mt-2.5 min-w-0 overflow-hidden rounded-lg border border-border-soft bg-card">
            <pre
              class="max-h-40 min-w-0 overflow-auto whitespace-pre-wrap break-all py-2 pl-3 pr-12 font-mono text-xs leading-relaxed text-foreground"
              :aria-label="t('computerConnect.commandLabel')"
            ><code>{{ command }}</code></pre>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              class="absolute right-1.5 top-1.5"
              :aria-label="t('common.copy')"
              @click="copyCommand"
            >
              <Copy class="size-4" />
            </Button>
          </div>
        </div>

        <!-- The waiting banner is a static card — no spinner chrome. -->
        <div class="flex items-center gap-2 rounded-lg border border-border-soft px-3 py-2.5">
          <span class="size-1.5 shrink-0 rounded-full bg-accent-gray-border" />
          <p class="text-xs text-muted-foreground">
            {{ t('computerConnect.waiting') }}
          </p>
        </div>

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
    </DialogContent>
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
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  toast,
  useClipboard,
} from '@felinic/ui'
import { Copy } from 'lucide-vue-next'
import { sdkApiBaseUrl } from '@/lib/api-client'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { buildRuntimeConnectCommand } from '@/pages/runtimes/command'
import ComputerAccessList from './computer-access-list.vue'
import { useAccountRuntimes, useComputerAccessActions } from './use-computer-access'

// The connect stepper, one continuous dialog: command (+ waiting) → connected
// → bot permissions. Closing at the command step revokes the credential so
// abandoned attempts leave nothing behind.
const props = defineProps<{
  credential: UserruntimeRuntime | null
}>()

const open = defineModel<boolean>('open', { default: false })

const { t } = useI18n()
const { copyText } = useClipboard()

const step = ref<'command' | 'access'>('command')
const runtimeId = computed(() => props.credential?.id ?? '')
const command = computed(() => buildRuntimeConnectCommand(sdkApiBaseUrl(), props.credential))

const { runtimes, refetch: refetchRuntimes } = useAccountRuntimes()
const { data: botsData } = useQuery(getBotsQuery())
const { grantAccess } = useComputerAccessActions()

const adoptedName = ref('')

// Auto-advance the moment the machine shows up online: grant every bot by
// default and land straight on the permissions step.
const connectedRuntime = computed(() => (
  (runtimes.value ?? []).find(runtime => runtime.id === runtimeId.value && runtime.online)
))
watch(connectedRuntime, async (runtime) => {
  if (!runtime || step.value !== 'command') return
  adoptedName.value = runtime.name || runtime.hostname || runtimeId.value
  toast.success(t('runtimes.computerOnline', { name: adoptedName.value }))
  await grantAllBots()
  step.value = 'access'
})

const granting = ref(false)
async function grantAllBots(): Promise<void> {
  if (granting.value) return
  granting.value = true
  let failed = 0
  for (const bot of botsData.value?.items ?? []) {
    if (!bot.id) continue
    try {
      await grantAccess({ botId: bot.id, runtimeId: runtimeId.value })
    } catch {
      failed += 1
    }
  }
  granting.value = false
  if (failed > 0) {
    toast.error(t('computerAccess.updateFailed'))
  }
}

function close(): void {
  // Closing at the command step abandons the attempt: the key was already
  // shown (maybe copied), and a never-connected placeholder is filtered out of
  // the computers list — left alone it would be an active credential the user
  // can neither see nor revoke. Capture the id BEFORE open=false: the parent
  // clears the credential on close and runtimeId would recompute to ''.
  const abandonedId = step.value === 'command' ? runtimeId.value : ''
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

async function copyCommand(): Promise<void> {
  const copied = await copyText(command.value)
  if (copied) {
    toast.success(t('common.copied'))
  } else {
    toast.error(t('common.copyFailed'))
  }
}

// A fresh credential starts the flow from the top.
watch(() => props.credential?.id, (id) => {
  if (!id) return
  step.value = 'command'
  adoptedName.value = ''
})
</script>
