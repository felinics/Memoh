<script setup lang="ts">
import { computed, useAttrs } from 'vue'
import { useI18n } from 'vue-i18n'
import { ImageNode, type ImageNodeProps } from 'markstream-vue'
import { markdownAssetUrl } from './media-url'

defineOptions({ inheritAttrs: false })
const props = defineProps<{ node: ImageNodeProps['node'] }>()
const attrs = useAttrs()
const { t } = useI18n()
const assetUrl = computed(() => markdownAssetUrl(props.node.src))
const failed = computed(() => props.node.src.startsWith('memoh-media-error:'))
// Older messages may contain public image URLs without archived bindings.
const publicUrl = computed(() => /^https?:\/\//i.test(props.node.src) ? props.node.src : null)
const node = computed(() => ({ ...props.node, src: assetUrl.value ?? publicUrl.value ?? '' }))
</script>

<template>
  <ImageNode
    v-if="assetUrl || publicUrl"
    :node="node"
    v-bind="attrs"
  />
  <span
    v-else
    role="status"
  >{{ node.alt }} — {{ t(failed ? 'errors.media.reference_unavailable' : 'chat.mediaPreparing') }}</span>
</template>
