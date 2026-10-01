<template>
  <!-- A rolled-back first send's turn, leaving. No longer part of any
       transcript: inert, and removed when its exit ends. -->
  <div
    ref="rootEl"
    data-chat-turn
    aria-hidden="true"
    inert
    class="pointer-events-none"
  >
    <div
      data-turn-motion
      class="space-y-6"
    >
      <div
        v-for="msg in turns"
        :key="msg.id"
        class="px-2 -mx-2"
      >
        <MessageItem
          :message="msg"
          :bot-id="botId ?? undefined"
          :bot-name="botName"
          :bot-avatar-url="botAvatarUrl"
          :is-scrolling="false"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, useTemplateRef } from 'vue'
import type { ChatMessage } from '@/store/chat-list'
import type { ChatViewTarget } from '@/store/chat/types'
import MessageItem from './message-item.vue'
import { animateTurnExit, TURN_MOTION_MAX_DISTANCE_PX } from '../composables/turn-entrance'

const props = defineProps<{
  // The rollback this exit belongs to. Reported back on done exactly as it
  // was at mount, so an exit cut short by the pane moving on still names the
  // view it came from.
  target: ChatViewTarget
  invocationId: string
  turns: ChatMessage[]
  botId?: string | null
  botName?: string
  botAvatarUrl?: string
}>()

const emit = defineEmits<{
  done: [target: ChatViewTarget, invocationId: string]
}>()

const rootEl = useTemplateRef<HTMLElement>('rootEl')
let stop: (() => void) | undefined
const rollback = { target: { ...props.target }, invocationId: props.invocationId }
const done = () => emit('done', rollback.target, rollback.invocationId)

// The exit starts from this element's own mount rather than from a watcher
// in the pane. A post-flush watcher there was seen to run before the pane had
// rendered this block, find no element, and end the rollback unanimated;
// mounting is the one moment the element is guaranteed to exist.
onMounted(() => {
  const el = rootEl.value
  if (!el) {
    done()
    return
  }
  stop = animateTurnExit(el, TURN_MOTION_MAX_DISTANCE_PX, done)
})

// Unmounted mid-exit (the pane moved to another view): stopping still reports
// done, so the rollback never outlives the view it belongs to.
onBeforeUnmount(() => stop?.())
</script>
