<template>
  <CalloutBanner
    tone="destructive"
    :description="message"
    :dismiss-label="$t('chat.slash.dismiss')"
    @dismiss="emit('dismiss')"
  />
</template>

<script setup lang="ts">
// One error line inside the composer panel (send / model-switch / agent-switch
// failures, and standing "this mode can't run" reasons). The page-level
// destructive banner — solid fill, no border — standing on its own above the
// composer; several notices stack upward, never wrapped in a shared shell. Every error line is
// dismissible — even a standing reason the user already understands should
// not be forced to stay on screen; the host decides what dismissing means for
// its source.
import { CalloutBanner } from '@felinic/ui'

defineProps<{
  message: string
}>()

const emit = defineEmits<{
  (e: 'dismiss'): void
}>()
</script>
