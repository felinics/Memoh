<template>
  <Dialog v-model:open="open">
    <DialogPanel>
      <ContextLifecycleContent />
    </DialogPanel>
  </Dialog>
</template>

<script setup lang="ts">
import { Dialog, DialogPanel } from '@felinic/ui'
import ContextLifecycleContent from './context-lifecycle-content.vue'

const open = defineModel<boolean>('open', { default: false })
</script>
