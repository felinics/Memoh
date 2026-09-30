<template>
  <div class="flex items-start gap-2 px-1.5 py-1 text-body text-destructive">
    <!-- One text line tall, so the icon centres on the first line of the
         message however it wraps. -->
    <span class="flex h-[1lh] shrink-0 items-center">
      <CircleAlert class="size-3.5" />
    </span>
    <span class="min-w-0 break-words">{{ message }}</span>
  </div>
</template>

<script setup lang="ts">
// One error line inside the composer panel (send / model-switch / agent-switch
// failures). No shell of its own — the destructive tone is carried only by the
// icon + text color, since the panel's capsule already provides the surface.
import { CircleAlert } from 'lucide-vue-next'

defineProps<{
  message: string
}>()
</script>
