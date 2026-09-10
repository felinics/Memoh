<template>
  <picture
    v-if="activeImageUrl && !failed"
    :class="frameClass"
  >
    <img
      :src="activeImageUrl"
      alt=""
      :class="imageClass"
      @error="failed = true"
    >
  </picture>
  <Zap
    v-else
    :class="fallbackClass"
  />
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Zap } from 'lucide-vue-next'
import type { HandlersSupermarketSkillIcon, HandlersSupermarketSkillIconAsset } from '@memohai/sdk'
import { sdkApiUrl } from '@/lib/api-client'
import { useSettingsStore } from '@/store/settings'

const props = withDefaults(defineProps<{
  icon?: HandlersSupermarketSkillIcon
  variant?: 'card' | 'detail'
}>(), {
  variant: 'card',
})

const failed = ref(false)
const settings = useSettingsStore()
const image = computed(() => props.variant === 'detail'
  ? props.icon?.detail || props.icon?.card
  : props.icon?.card || props.icon?.detail)

function imageURL(value?: HandlersSupermarketSkillIconAsset) {
  const digest = value?.digest?.trim()
  if (!digest || !/^[a-f0-9]{64}$/.test(digest)) return ''
  return sdkApiUrl({ url: '/supermarket/artifacts/icon/{digest}', path: { digest } })
}

const imageUrl = computed(() => imageURL(image.value))
const darkImageUrl = computed(() => imageURL(props.icon?.dark))
const activeImageUrl = computed(() => settings.resolvedColorMode === 'dark' && darkImageUrl.value
  ? darkImageUrl.value
  : imageUrl.value)
// One glyph size per context, whatever the SVG's own padding: 20px in the
// 36px list box (same as dependency rows), 32px in the 48px detail box.
const frameClass = computed(() => props.variant === 'detail' ? 'block size-8' : 'block size-5')
const imageClass = 'size-full object-contain'
const fallbackClass = computed(() => props.variant === 'detail'
  ? 'size-8 text-muted-foreground'
  : 'size-4 text-muted-foreground')

watch(activeImageUrl, () => { failed.value = false })
</script>
