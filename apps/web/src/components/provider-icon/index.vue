<template>
  <component
    :is="iconComponent"
    v-if="iconComponent"
    :size="size ?? '1em'"
    v-bind="$attrs"
  />
  <!-- A monochrome logo drawn in currentColor cannot inherit the text color
       through an <img>, so it paints the text color through the logo's shape. -->
  <span
    v-else-if="monochrome && imageSource"
    role="img"
    aria-hidden="true"
    class="inline-block shrink-0 bg-current"
    :style="maskStyle"
    v-bind="$attrs"
  />
  <img
    v-else-if="imageSource && imageSource !== failedSource"
    :src="imageSource"
    decoding="sync"
    loading="eager"
    :width="numericSize"
    :height="numericSize"
    :style="cssSize ? { width: cssSize, height: cssSize } : undefined"
    alt=""
    class="[color-scheme:light] dark:[color-scheme:dark]"
    v-bind="$attrs"
    @error="onImageError"
  >
  <!-- URL icon still fetching: hold an empty, correctly-sized box instead of
       the fallback slot. The fallback would paint at the glyph's default size
       (it receives no $attrs) and then swap to the real image — a visible
       flash + size jump on every uncached mount. Unknown non-URL names still
       get the slot. -->
  <span
    v-else-if="isUrl"
    class="inline-block [&>svg]:size-full"
    v-bind="$attrs"
    aria-hidden="true"
  >
    <slot v-if="imageSource === null || (imageSource && imageSource === failedSource)" />
  </span>
  <slot v-else />
</template>

<script setup lang="ts">
import { computed, ref, type Component } from 'vue'
import { iconMap } from './icons.ts'
import { providerIconSource } from './preload'

const props = defineProps<{
  icon: string
  // Unset leaves an image to the caller's sizing classes; a glyph defaults to 1em.
  size?: string | number
  // Paint a URL logo in the current text color, for logos drawn in one color.
  monochrome?: boolean
}>()

defineOptions({ inheritAttrs: false })

const isUrl = computed(() =>
  props.icon.startsWith('http://') || props.icon.startsWith('https://'),
)

const source = computed(() => isUrl.value && typeof Image !== 'undefined'
  ? providerIconSource(props.icon)
  : undefined)
const imageSource = computed(() => source.value ? source.value.value : '')
const failedSource = ref('')

function onImageError(event: Event): void {
  // Preserve the caller's icon slot and sizing when the original URL also
  // fails. Record the failed node's source so a late event cannot hide a new URL.
  failedSource.value = (event.currentTarget as HTMLImageElement).getAttribute('src') ?? ''
}

// The width attribute only takes pixels, so an em size would be dropped and the
// image would render at the SVG's own size. The style carries any unit, and is
// left out when the caller sizes the image with classes instead.
// A bare number, given as a number or a string such as size="22", is pixels: a
// unitless CSS width is invalid and would collapse a masked logo to nothing.
const numericSize = computed(() => {
  if (typeof props.size === 'number') return props.size
  return props.size && /^\d+(\.\d+)?$/.test(props.size) ? Number(props.size) : undefined
})
const cssSize = computed(() => numericSize.value !== undefined ? `${numericSize.value}px` : props.size)

const maskStyle = computed(() => {
  const mask = `url("${imageSource.value}")`
  return {
    ...(cssSize.value ? { width: cssSize.value, height: cssSize.value } : {}),
    maskImage: mask,
    WebkitMaskImage: mask,
    maskSize: 'contain',
    WebkitMaskSize: 'contain',
    maskRepeat: 'no-repeat',
    WebkitMaskRepeat: 'no-repeat',
    maskPosition: 'center',
    WebkitMaskPosition: 'center',
  }
})

const iconComponent = computed<Component | undefined>(() => {
  if (isUrl.value) return undefined
  return iconMap[props.icon]
})
</script>
