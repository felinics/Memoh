<template>
  <CalloutBanner
    :tone="exhausted ? 'destructive' : 'warning'"
    :description="message"
    :dismiss-label="exhausted ? undefined : $t('chat.codexUsage.dismiss')"
    @dismiss="emit('dismiss')"
  />
</template>

<script setup lang="ts">
// The account usage notice stacked above the composer. A nearing-limit warning
// can be dismissed; a reached limit cannot, since every send fails until the
// window resets.
import { CalloutBanner } from '@felinic/ui'

defineProps<{
  exhausted: boolean
  message: string
}>()
const emit = defineEmits<{ (e: 'dismiss'): void }>()
</script>
