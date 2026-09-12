<script setup lang="ts">
import { computed, useAttrs } from 'vue'
import { useI18n } from 'vue-i18n'
import { markdownAssetUrl } from './media-url'
import { LinkNode, type LinkNodeProps } from 'markstream-vue'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { tryParseLocalhostHref } from '@/utils/localhost-link'

// Custom markstream `link` node. Rendering is delegated wholesale to markstream's
// own LinkNode (so styling, tooltip and stream animation are untouched); we only
// hijack the CLICK for links that point at a container-local dev server, opening
// them in the workspace browser panel instead of the user's OS browser — the
// container's localhost is not the user's localhost. Everything else (external
// links, modifier-clicks) keeps default behavior.
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  node: LinkNodeProps['node']
  indexKey: LinkNodeProps['indexKey']
}>()

const attrs = useAttrs()
const { t } = useI18n()
const failed = computed(() => props.node.href.startsWith('memoh-media-error:'))
const assetUrl = computed(() => markdownAssetUrl(props.node.href))
const pending = computed(() => !assetUrl.value && /^\/data\//.test(props.node.href) && !/[#?]|:\d/.test(props.node.href))
const displayNode = computed(() => {
  const url = assetUrl.value
  const href = url ? `${url}${url.includes('?') ? '&' : '?'}download=${encodeURIComponent(props.node.text || 'download')}` : props.node.href
  return { ...props.node, href }
})
const tabs = useWorkspaceTabsStore()

const localAddress = computed(() => tryParseLocalhostHref(props.node?.href))

function onClickCapture(event: MouseEvent) {
  const parsed = localAddress.value
  if (!parsed) return
  // Leave modifier/middle clicks to the browser (open externally) for users who
  // really want an OS tab.
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  // openBrowserAt returns false when the workspace browser is unavailable
  // (no manage permission or no dock) — fall back to the OS tab.
  if (!tabs.openBrowserAt(parsed.display)) {
    window.open(props.node.href, '_blank', 'noopener')
  }
}
</script>

<template>
  <span
    v-if="failed"
    role="status"
  >{{ node.text }} — {{ t('errors.media.reference_unavailable') }}</span>
  <span
    v-else-if="pending"
    role="status"
  >{{ node.text }} — {{ t('chat.mediaPreparing') }}</span>
  <span
    v-else
    @click.capture="onClickCapture"
  ><LinkNode
    :node="displayNode"
    :index-key="indexKey"
    v-bind="attrs"
  /></span>
</template>
