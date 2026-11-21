<template>
  <CalloutBanner
    tone="destructive"
    :description="message"
  />
</template>

<script setup lang="ts">
// The account quota exhausted notice stacked above the composer. A reached
// limit cannot be dismissed, since every send fails until the window resets.
import { CalloutBanner } from '@felinic/ui'

defineProps<{
  message: string
}>()
</script>
