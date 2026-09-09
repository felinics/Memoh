<template>
  <Dialog v-model:open="open">
    <DialogPanel @close-auto-focus="emit('closeAutoFocus', $event)">
      <ContextLifecycleContent />
    </DialogPanel>
  </Dialog>
</template>

<script setup lang="ts">
import { Dialog, DialogPanel } from '@felinic/ui'
import ContextLifecycleContent from './context-lifecycle-content.vue'

const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ closeAutoFocus: [event: Event] }>()
</script>
