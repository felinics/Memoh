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
        />
      </DialogBody>

      <DialogFooter>
        <Button
          v-if="subject === 'bot'"
          variant="outline"
          @click="addComputer"
        >
          <Plus />
          {{ t('chat.continueOn.addComputer') }}
        </Button>
        <Button @click="open = false">
          {{ t('computerAccess.done') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { Plus } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
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

function addComputer(): void {
  open.value = false
  void router.push({ name: 'runtimes', query: { connect: '1' } })
}

const subject = computed<'runtime' | 'bot'>(() => (props.runtime ? 'runtime' : 'bot'))
const subjectName = computed(() => (
  subject.value === 'runtime' ? (props.runtime?.name ?? '') : (props.bot?.name ?? '')
))
</script>
