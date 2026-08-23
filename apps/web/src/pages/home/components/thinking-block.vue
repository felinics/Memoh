<template>
  <div
    class="font-[400]"
    :class="inGroup ? '' : 'text-[0.90625rem]'"
  >
    <button
      class="group/h flex items-center gap-1.5 w-full text-left transition-colors duration-75 cursor-pointer py-px text-cop-title hover:text-foreground select-none"
      @click="toggleOpen"
    >
      <span
        class="min-w-0 truncate tracking-[0.01em]"
        :class="streaming ? 'tool-shimmer-text' : ''"
      >{{ label }}</span>
      <ChevronDown
        v-if="open"
        class="size-3.5 shrink-0 ml-0.5 opacity-50 group-hover/h:opacity-100"
      />
      <ChevronRight
        v-else
        class="size-3.5 shrink-0 ml-0.5 opacity-50 group-hover/h:opacity-100"
      />
    </button>
    <CollapseSection :open="open">
      <div
        class="mt-1 whitespace-pre-wrap text-muted-foreground"
        :class="inGroup ? 'leading-snug' : 'leading-relaxed'"
        v-text="bodyText"
      />
    </CollapseSection>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ChevronDown, ChevronRight } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { ThinkingBlock } from '@/store/chat-list'
import CollapseSection from './collapse-section.vue'
import { getReasoningDuration } from './reasoning-timing'
import { getCollapseOpen, reasoningCollapseKey, setCollapseOpen } from './process-collapse'

const props = defineProps<{
  block: ThinkingBlock
  messageId: string
  streaming: boolean
  // True when nested inside a multi-step process card: inherit the card's smaller
  // type scale + tighter leading instead of the root-level cop size.
  inGroup?: boolean
}>()

const { t } = useI18n()

// Persisted, user-driven toggle (survives the post-turn refetch/remount).
const collapseKey = computed(() => reasoningCollapseKey(props.messageId, props.block))
const open = ref(getCollapseOpen(collapseKey.value))
watch(collapseKey, (key) => {
  open.value = getCollapseOpen(key)
})

// Trimmed so the expanded body doesn't open with leading blank lines/space.
const bodyText = computed(() => (props.block.content ?? '').trim())

// Duration is measured centrally in message-item (every reasoning block, not
// just the streaming tail) and cached by stable block identity, so the
// re-mounted "done" block recovers it here. Historical blocks (never streamed
// this session) have no timing and fall back to a plain "Thought".
const durationMs = computed(() => getReasoningDuration(props.messageId, props.block))

const label = computed(() => {
  if (props.streaming) return t('chat.thinkingInProgress')
  if (durationMs.value > 0) {
    return t('chat.process.thoughtSeconds', { seconds: Math.max(1, Math.round(durationMs.value / 1000)) })
  }
  // No measured duration (historical block, or a sub-second thought) — a worded
  // phrase reads more naturally than a bare "Thought" or a fake "0s".
  return t('chat.process.thoughtBriefly')
})

function toggleOpen() {
  open.value = !open.value
  setCollapseOpen(collapseKey.value, open.value)
}
</script>
