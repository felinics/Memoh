<template>
  <span
    ref="dot"
    data-slot="attention-dot"
    :data-kind="kind"
    :data-cutout="cutout || undefined"
    class="relative inline-block size-2 shrink-0 rounded-full"
    aria-hidden="true"
  />
</template>

<script setup lang="ts">
import { onMounted, useTemplateRef, watch } from 'vue'
import type { AttentionKind } from '@/composables/api/attention-api'
import { useUnreadStore } from '@/store/unread'
import { popDot } from '@/utils/attention-motion'

/**
 * The one unread mark for sessions, the inbox and the bot switcher. It pops only when
 * the attention it shows (`sessionId` + `attentionAt`) just arrived live, so mounting it
 * later (expanding a list, reloading, switching bots) stays still. `cutout` rings the dot
 * in the sidebar surface for avatar corners.
 */
const props = defineProps<{
  kind: AttentionKind
  sessionId?: string
  attentionAt?: string
  cutout?: boolean
}>()

const unread = useUnreadStore()
const dot = useTemplateRef('dot')

function popIfFresh() {
  if (dot.value && props.sessionId && props.attentionAt && unread.isFresh(props.sessionId, props.attentionAt)) popDot(dot.value)
}

onMounted(popIfFresh)
watch(() => props.attentionAt, popIfFresh)
</script>
