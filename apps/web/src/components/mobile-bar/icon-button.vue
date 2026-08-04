<template>
  <!-- The mobile bar's icon button: one owner for the ghost/circle chrome
       (muted at rest → foreground on hover, stay-lit while its menu is open)
       so the bars across shells read as one language. data-[state=open] is set
       by reka only when the button is a menu trigger. -->
  <Button
    variant="ghost"
    size="icon"
    shape="circle"
    :class="iconButtonClass"
    :title="label"
    :aria-label="label"
    @click="emit('click')"
  >
    <component
      :is="icon"
      :stroke-width="1.75"
    />
  </Button>
</template>

<script setup lang="ts">
import type { Component } from 'vue'
import { Button } from '@felinic/ui'

defineProps<{
  icon: Component
  label: string
}>()
const emit = defineEmits<{ click: [] }>()

// Chrome lives here (not at call sites) so the bars can't drift: muted at rest,
// foreground on hover, stay-lit while a menu it triggers is open.
const iconButtonClass = 'shrink-0 text-muted-foreground hover:text-foreground data-[state=open]:text-foreground' /* ui-allow-style */
</script>
