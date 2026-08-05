<template>
  <component
    :is="iconComponent"
    v-if="iconComponent"
    :size="size"
    v-bind="$attrs"
  />
  <img
    v-else-if="isUrl"
    :src="icon"
    :width="size"
    :height="size"
    alt=""
    class="[color-scheme:light] dark:[color-scheme:dark]"
    v-bind="$attrs"
  >
  <slot v-else />
</template>

<script setup lang="ts">
import { computed, type Component } from 'vue'
import { iconMap } from './icons.ts'

const props = withDefaults(defineProps<{
  icon: string
  size?: string | number
}>(), {
  size: '1em',
})

defineOptions({ inheritAttrs: false })

const isUrl = computed(() =>
  props.icon.startsWith('http://') || props.icon.startsWith('https://'),
)

const iconComponent = computed<Component | undefined>(() => {
  if (isUrl.value) return undefined
  return iconMap[props.icon]
})
</script>
