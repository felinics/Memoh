<script setup lang="ts">
import { Hand, Shield, ShieldAlert, ShieldCheck, Terminal } from 'lucide-vue-next'

defineProps<{
  icon?: string
  warning?: boolean
}>()
</script>

<template>
  <span
    class="relative inline-flex size-4 shrink-0 items-center justify-center"
    :class="warning ? 'text-warning-foreground' : undefined"
    aria-hidden="true"
  >
    <Hand
      v-if="icon === 'hand'"
      class="size-4 text-inherit"
    />
    <template v-else-if="icon === 'shield-terminal'">
      <Shield class="size-4 text-inherit" />
      <Terminal class="absolute size-2.5 text-inherit" />
    </template>
    <ShieldAlert
      v-else-if="icon === 'shield-alert' || warning"
      class="size-4 text-inherit"
    />
    <ShieldCheck
      v-else
      class="size-4 text-inherit"
    />
  </span>
</template>
