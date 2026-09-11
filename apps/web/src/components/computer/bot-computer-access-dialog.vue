<template>
  <Dialog v-model:open="open">
    <DialogPanel
      width="xl"
      footer
    >
      <DialogHeader class="pr-8">
        <DialogTitle class="break-words">
          {{ subjectName }}
        </DialogTitle>
        <DialogDescription>
          {{ subject === 'runtime' ? t('computerAccess.subtitleRuntime') : t('computerAccess.subtitleBot') }}
        </DialogDescription>
      </DialogHeader>

      <DialogBody>
        <ComputerAccessList
          :runtime="runtime"
          :bot="bot"
          @add-computer="onAddComputer"
        />
      </DialogBody>

      <DialogFooter>
        <!-- This dialog only grants access; computer lifecycle (connect,
             delete) lives on the Computers settings page — the footer offers
             the explicit exit instead of leaving users stranded. -->
        <Button
          v-if="subject === 'bot'"
          variant="outline"
          @click="goToManage"
        >
          <SettingsIcon />
          {{ t('chat.continueOn.manageComputers') }}
        </Button>
        <Button @click="open = false">
          {{ t('computerAccess.done') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>

  <ConnectComputerDialog
    v-model:open="connectOpen"
    :credential="connectCredential"
  />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { SettingsIcon } from '@memohai/icon/ui'
import {
  Button,
  Dialog,
  DialogPanel,
  DialogBody,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@felinic/ui'
import ComputerAccessList from './computer-access-list.vue'
import ConnectComputerDialog from './connect-computer-dialog.vue'
import { useConnectComputer } from './use-connect-computer'

// The standalone Computer ACL dialog (gear on the Computers page, composer
// empty states). Exactly one subject prop is set: runtime shows bots, bot
// shows computers. The list itself is shared with the connect stepper.
const props = defineProps<{
  runtime?: { id: string, name: string } | null
  bot?: { id: string, name: string } | null
}>()

const open = defineModel<boolean>('open', { default: false })

const { t } = useI18n()
const router = useRouter()
const { connectOpen, connectCredential, startConnect } = useConnectComputer()

// The zero-state ghost row adds in place: this dialog steps aside while the
// wizard runs on the same surface, no route change.
function onAddComputer(): void {
  open.value = false
  void startConnect()
}

function goToManage(): void {
  open.value = false
  void router.push({ name: 'runtimes' })
}

const subject = computed<'runtime' | 'bot'>(() => (props.runtime ? 'runtime' : 'bot'))
const subjectName = computed(() => (
  subject.value === 'runtime' ? (props.runtime?.name ?? '') : (props.bot?.name ?? '')
))
</script>
