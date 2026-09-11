<template>
  <component
    :is="iconComponent"
    v-if="iconComponent"
    :size="size"
    v-bind="$attrs"
  />
  <img
    v-else-if="imageSource"
    :src="imageSource"
    decoding="sync"
    loading="eager"
    :width="size"
    :height="size"
    alt=""
    class="[color-scheme:light] dark:[color-scheme:dark]"
    v-bind="$attrs"
  >
  <!-- URL icon still fetching: hold an empty, correctly-sized box instead of
       the fallback slot. The fallback would paint at the glyph's default size
       (it receives no $attrs) and then swap to the real image — a visible
       flash + size jump on every uncached mount. Unknown non-URL names still
       get the slot. -->
  <span
    v-else-if="isUrl"
    class="inline-block"
    v-bind="$attrs"
    aria-hidden="true"
  />
  <slot v-else />
</template>

<script setup lang="ts">
import { computed, type Component } from 'vue'
import { iconMap } from './icons.ts'
import { providerIconSource } from './preload'

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

const source = computed(() => isUrl.value && typeof Image !== 'undefined'
  ? providerIconSource(props.icon)
  : undefined)
const imageSource = computed(() => source.value?.value || '')

const iconComponent = computed<Component | undefined>(() => {
  if (isUrl.value) return undefined
  return iconMap[props.icon]
})
</script>
