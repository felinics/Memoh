<script setup lang="ts">
import { computed, useAttrs } from 'vue'
import { useI18n } from 'vue-i18n'
import { Image as ImageIcon } from 'lucide-vue-next'
import { ImageNode, type ImageNodeProps } from 'markstream-vue'
import { useWorkspaceLink } from '@/composables/useWorkspaceLink'
import { markdownAssetUrl } from './media-url'

defineOptions({ inheritAttrs: false })
const props = defineProps<{ node: ImageNodeProps['node'] }>()
const attrs = useAttrs()
const { t } = useI18n()
const openWorkspaceLink = useWorkspaceLink()
const assetUrl = computed(() => markdownAssetUrl(props.node.src))
const failed = computed(() => props.node.src.startsWith('memoh-media-error:'))
// An image that has no archived copy yet (still streaming) or never gets one
// (an older or non-chat message) is still a workspace file: open it there,
// like a file link, instead of requesting /data from the app origin.
const workspacePath = computed(() => !assetUrl.value && /^\/data\//.test(props.node.src) ? props.node.src : null)
const label = computed(() => props.node.alt || props.node.src.split('/').pop() || props.node.src)
const node = computed(() => assetUrl.value ? { ...props.node, src: assetUrl.value } : props.node)
function onOpen(event: MouseEvent) {
  if (workspacePath.value) openWorkspaceLink(event, workspacePath.value)
}
</script>

<template>
  <span
    v-if="failed"
    role="status"
  >{{ props.node.alt }} — {{ t('errors.media.reference_unavailable') }}</span>
  <a
    v-else-if="workspacePath"
    :href="workspacePath"
    class="relative mx-1 inline-flex pl-4.5 align-baseline text-primary"
    @click.capture="onOpen"
    @auxclick.capture="onOpen"
  >
    <ImageIcon
      class="pointer-events-none absolute left-0 top-1/2 size-4 -translate-y-1/2"
      aria-hidden="true"
    />
    {{ label }}
  </a>
  <ImageNode
    v-else
    :node="node"
    v-bind="attrs"
  />
</template>
