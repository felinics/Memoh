<script setup lang="ts">
import { computed, useAttrs } from 'vue'
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
  <span @click.capture="onClickCapture"><LinkNode
    :node="node"
    :index-key="indexKey"
    v-bind="attrs"
  /></span>
</template>
