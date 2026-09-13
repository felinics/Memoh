<script setup lang="ts">
import { useAttrs } from 'vue'
import { LinkNode, type LinkNodeProps } from 'markstream-vue'
import { useWorkspaceLink } from '@/composables/useWorkspaceLink'

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
const openWorkspaceLink = useWorkspaceLink()
</script>

<template>
  <span @click.capture="openWorkspaceLink($event, props.node.href)"><LinkNode
    :node="node"
    :index-key="indexKey"
    v-bind="attrs"
  /></span>
</template>
