<template>
  <CalloutBanner
    bare
    size="sm"
    tone="destructive"
    :description="message"
    class="px-(--composer-text-inset) py-1"
  >
    <template #icon>
      <AlertGlyph class="size-3.5" />
    </template>
  </CalloutBanner>
</template>

<script setup lang="ts">
// One error line inside the composer panel (send / model-switch / agent-switch
// failures). `bare` because the panel's capsule already provides the surface;
// the --composer-text-inset indent puts the icon on the composer's text
// column, shared with the sibling panel sections.
import { CalloutBanner } from '@felinic/ui'
import AlertGlyph from '@/components/alert-glyph/index.vue'

defineProps<{
  message: string
}>()
</script>
