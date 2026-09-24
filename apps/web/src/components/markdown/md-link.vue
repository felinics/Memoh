<script setup lang="ts">
import { computed, provide, reactive, useAttrs } from 'vue'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@felinic/ui'
import { FileText, Globe } from 'lucide-vue-next'
import { LinkNode, type LinkNodeProps } from 'markstream-vue'
import { useSiteIcon } from '@/composables/useSiteIcon'
import { useWorkspaceLink } from '@/composables/useWorkspaceLink'
import { classifyWorkspaceLink } from '@/utils/workspace-link'
import { siteIconOrigin } from '@/utils/site-icon'

// Keep markstream's rich inline text, URL sanitization and streaming
// behavior. The leading icon sits over the anchor's padding so it shares the
// same click target without replacing the renderer or adding a second link.
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  node: LinkNodeProps['node']
  indexKey: LinkNodeProps['indexKey']
}>()

// The shared Memoh tooltip owns link hints. markstream's injected
// `markstreamShowTooltips` overrides LinkNode's own prop, so disabling its
// popover has to go through the same key. With the popover off, LinkNode puts
// the URL in a native title instead (attrs cannot override it); remove it, or
// it shows a second, delayed hint.
provide('markstreamShowTooltips', computed(() => false))
function removeNativeTitle(element: HTMLElement) {
  element.removeAttribute('title')
}
const vWithoutNativeTitle = { mounted: removeNativeTitle, updated: removeNativeTitle }

const attrs = useAttrs()
const openWorkspaceLink = useWorkspaceLink()
const link = computed(() => props.node.loading ? null : classifyWorkspaceLink(props.node.href))
const icon = computed(() => link.value?.kind === 'file' ? FileText : Globe)
const favicon = reactive(useSiteIcon(() => link.value?.kind === 'external' ? siteIconOrigin(props.node.href) : null))
</script>

<template>
  <TooltipProvider :delay-duration="0">
    <Tooltip
      :delay-duration="0"
      :disabled="node.loading"
    >
      <TooltipTrigger as-child>
        <span
          class="relative inline-flex align-baseline text-primary"
          :class="{ 'mx-1': link }"
          @click.capture="openWorkspaceLink($event, props.node.href)"
          @auxclick.capture="openWorkspaceLink($event, props.node.href)"
        >
          <component
            :is="icon"
            v-if="link && !favicon.ready"
            class="pointer-events-none absolute left-0 top-1/2 size-4 -translate-y-1/2"
            aria-hidden="true"
          />
          <img
            v-if="favicon.usable"
            :key="favicon.src"
            :src="favicon.src"
            :style="{ colorScheme: favicon.colorScheme }"
            alt=""
            aria-hidden="true"
            referrerpolicy="no-referrer"
            loading="lazy"
            class="pointer-events-none absolute left-0 top-1/2 size-4 -translate-y-1/2 object-contain"
            :class="{ invisible: !favicon.ready }"
            @load="favicon.onLoad"
            @error="favicon.onError"
          >
          <LinkNode
            v-without-native-title
            :node="node"
            :index-key="indexKey"
            v-bind="attrs"
            :class="{ 'pl-4.5': link }"
            :target="link?.kind === 'external' ? '_blank' : undefined"
          />
        </span>
      </TooltipTrigger>
      <TooltipContent>{{ node.title || node.href }}</TooltipContent>
    </Tooltip>
  </TooltipProvider>
</template>
