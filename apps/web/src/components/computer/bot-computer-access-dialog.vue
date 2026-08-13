<template>
  <Dialog v-model:open="open">
    <DialogContent>
      <DialogHeader class="pr-8">
        <DialogTitle class="break-words">
          {{ subjectName }}
        </DialogTitle>
        <DialogDescription>
          {{ subject === 'runtime' ? t('computerAccess.subtitleRuntime') : t('computerAccess.subtitleBot') }}
        </DialogDescription>
      </DialogHeader>

      <ComputerAccessList
        :runtime="runtime"
        :bot="bot"
      />

      <DialogFooter>
        <Button @click="open = false">
          {{ t('computerAccess.done') }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Button,
  Dialog,
  DialogContent,
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

const subject = computed<'runtime' | 'bot'>(() => (props.runtime ? 'runtime' : 'bot'))
const subjectName = computed(() => (
  subject.value === 'runtime' ? (props.runtime?.name ?? '') : (props.bot?.name ?? '')
))
</script>
