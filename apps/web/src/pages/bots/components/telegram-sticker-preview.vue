<template>
  <div
    ref="root"
    class="flex aspect-square items-center justify-center overflow-hidden rounded-md bg-muted"
  >
    <img
      v-if="imageUrl"
      :src="imageUrl"
      :alt="alt"
      class="size-full object-contain"
    >
    <Skeleton
      v-else-if="loading || !observed"
      class="size-full rounded-md"
    />
    <ImageOff
      v-else
      class="size-6 text-muted-foreground"
      aria-hidden="true"
    />
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, useTemplateRef } from 'vue'
import { useIntersectionObserver } from '@vueuse/core'
import { Skeleton } from '@felinic/ui'
import { ImageOff } from 'lucide-vue-next'
import { getBotsByBotIdTelegramStickersByStickerIdPreview } from '@memohai/sdk'

const props = defineProps<{
  botId: string
  stickerId: string
  alt: string
}>()

const root = useTemplateRef<HTMLElement>('root')
const observed = ref(false)
const loading = ref(false)
const imageUrl = ref('')

async function loadPreview() {
  if (observed.value || loading.value || imageUrl.value) return
  observed.value = true
  loading.value = true
  try {
    const response = await getBotsByBotIdTelegramStickersByStickerIdPreview({
      path: { bot_id: props.botId, sticker_id: props.stickerId },
      parseAs: 'blob',
      throwOnError: true,
    })
    imageUrl.value = URL.createObjectURL(response.data as Blob)
  } catch {
    imageUrl.value = ''
  } finally {
    loading.value = false
  }
}

useIntersectionObserver(root, ([entry]) => {
  if (entry?.isIntersecting) void loadPreview()
}, { rootMargin: '160px' })

onBeforeUnmount(() => {
  if (imageUrl.value) URL.revokeObjectURL(imageUrl.value)
})
</script>
