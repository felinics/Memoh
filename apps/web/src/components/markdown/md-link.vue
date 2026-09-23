<script setup lang="ts">
import { computed, provide, ref, useAttrs, watch } from 'vue'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@felinic/ui'
import { getSiteIcon } from '@memohai/sdk'
import { useSettingsStore } from '@/store/settings'
import { FileText, Globe } from 'lucide-vue-next'
import { LinkNode, type LinkNodeProps } from 'markstream-vue'
import { useWorkspaceLink } from '@/composables/useWorkspaceLink'
import { classifyWorkspaceLink } from '@/utils/workspace-link'

// Keep markstream's rich inline text, URL sanitization and streaming
// behavior. The leading icon sits over the anchor's padding so it shares the
// same click target without replacing the renderer or adding a second link.
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  node: LinkNodeProps['node']
  indexKey: LinkNodeProps['indexKey']
}>()

// The shared Memoh tooltip owns link hints. Disable the renderer tooltip and
// remove its native title fallback, which would produce a second delayed hint.
provide('markstreamShowTooltips', computed(() => false))
function removeNativeTitle(element: HTMLElement) {
  element.removeAttribute('title')
}
const vWithoutNativeTitle = { mounted: removeNativeTitle, updated: removeNativeTitle }

const attrs = useAttrs()
const openWorkspaceLink = useWorkspaceLink()
const link = computed(() => props.node.loading ? null : classifyWorkspaceLink(props.node.href))
const icon = computed(() => link.value?.kind === 'file' ? FileText : Globe)

const settings = useSettingsStore()
const favicon = ref('')
watch([() => props.node.href, link, () => settings.resolvedColorMode], async ([href, target, theme], _, onCleanup) => {
  favicon.value = ''
  if (target?.kind !== 'external') return
  const controller = new AbortController()
  onCleanup(() => controller.abort())
  try {
    const url = new URL(href, window.location.href)
    const { data } = await getSiteIcon({ query: { url: url.href, theme }, signal: controller.signal })
    if (!controller.signal.aborted) favicon.value = data?.url || ''
  } catch { /* Optional site metadata must not interrupt a chat message. */ }
}, { immediate: true })
const loadedFavicon = ref('')
const failedFavicon = ref('')
watch(favicon, () => { loadedFavicon.value = ''; failedFavicon.value = '' })
function faviconLoaded(event: Event) {
  loadedFavicon.value = (event.target as HTMLImageElement).src
}
function faviconFailed(event: Event) {
  loadedFavicon.value = ''
  failedFavicon.value = (event.target as HTMLImageElement).src
}

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
            v-if="link && (!favicon || loadedFavicon !== favicon)"
            class="pointer-events-none absolute left-0 top-1/2 size-4 -translate-y-1/2"
            aria-hidden="true"
          />
          <img
            v-if="favicon && failedFavicon !== favicon"
            :key="favicon"
            :src="favicon"
            :style="{ colorScheme: settings.resolvedColorMode }"
            alt=""
            aria-hidden="true"
            referrerpolicy="no-referrer"
            loading="lazy"
            class="pointer-events-none absolute left-0 top-1/2 size-4 -translate-y-1/2 object-contain"
            :class="{ invisible: loadedFavicon !== favicon }"
            @load="faviconLoaded"
            @error="faviconFailed"
          >
          <LinkNode
            v-without-native-title
            :node="node"
            :index-key="indexKey"
            v-bind="attrs"
            :class="{ 'pl-4.5': link }"
            :show-tooltip="false"
            :target="link?.kind === 'external' ? '_blank' : undefined"
          />
        </span>
      </TooltipTrigger>
      <TooltipContent>{{ node.title || node.href }}</TooltipContent>
    </Tooltip>
  </TooltipProvider>
</template>
