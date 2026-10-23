<template>
  <span
    :key="pulseKey"
    data-slot="attention-dot"
    :data-kind="kind"
    :data-cutout="cutout || undefined"
    class="attention-dot relative inline-block size-2 shrink-0 rounded-full"
    aria-hidden="true"
  />
</template>

<script setup lang="ts">
import type { AttentionKind } from '@/composables/api/attention-api'

/**
 * The one unread mark for sessions, the inbox and the bot switcher, so every surface
 * pops and pulses identically. Changing `pulseKey` (the attention timestamp) replays
 * the arrival motion, which is how a second reply on an already-unread session still
 * catches the eye. `cutout` rings the dot in the sidebar surface for avatar corners.
 */
defineProps<{
  kind: AttentionKind
  pulseKey?: string
  cutout?: boolean
}>()
</script>
