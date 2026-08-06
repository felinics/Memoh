<template>
  <!-- The mobile bar's icon button: one owner for the ghost/circle chrome
       (muted at rest → foreground on hover, stay-lit while its menu is open)
       so the bars across shells read as one language. data-[state=open] is set
       by reka only when the button is a menu trigger. -->
  <Button
    variant="ghost"
    size="icon"
    shape="circle"
    :class="mobileBarIconButtonClass"
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
import { mobileBarIconButtonClass } from './icon-button-class'

defineProps<{
  icon: Component
  label: string
}>()
// WARNING: this component must NOT be used as the child of a reka
// `as-child` trigger (DropdownMenuTrigger/PopoverTrigger …). Declaring a
// `click` emit makes Vue consume the trigger's injected onClick as an emit
// listener instead of a fallthrough attr, and the ref lands on the component
// instance — together they break the trigger (menu never opens, reka throws
// on the missing anchor). For trigger usage compose `Button` from
// @felinic/ui with `mobileBarIconButtonClass` directly (see the mobile top
// bar's "+" menu).
const emit = defineEmits<{ click: [] }>()
</script>
