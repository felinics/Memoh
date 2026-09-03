<template>
  <div
    data-queue-item
    class="flex min-w-0 items-center gap-1 rounded-lg bg-accent px-1.5 py-1"
  >
    <span
      data-queue-handle
      class="grid size-6 shrink-0 cursor-grab place-items-center text-muted-foreground active:cursor-grabbing"
      :title="$t('chat.queue.reorder')"
      :aria-label="$t('chat.queue.reorder')"
    >
      <GripVertical class="size-3.5" />
    </span>
    <Input
      v-model="draft"
      :disabled="busy"
      class="h-8 min-w-0 flex-1 border-0 bg-transparent px-1.5 text-label shadow-none focus-visible:ring-0"
      @keydown.enter.prevent="emit('save', draft)"
      @blur="emit('save', draft)"
    />
    <Button
      v-if="item.queueKind !== 'steer'"
      type="button"
      variant="ghost"
      size="icon-sm"
      :title="$t('chat.queue.enqueueSteer')"
      :aria-label="$t('chat.queue.enqueueSteer')"
      class="size-7 shrink-0 text-muted-foreground"
      :disabled="busy"
      @pointerdown.prevent
      @click="emit('steer')"
    >
      <CornerDownLeft class="size-3.5" />
    </Button>
    <span
      v-else
      data-queue-steer-status
      class="grid size-7 shrink-0 place-items-center text-success-foreground"
      :title="$t('chat.queue.steerQueued')"
      :aria-label="$t('chat.queue.steerQueued')"
    >
      <CircleCheck class="size-3.5" />
    </span>
    <Button
      v-if="item.queueKind !== 'steer'"
      type="button"
      variant="ghost"
      size="icon-sm"
      :disabled="busy"
      :title="$t('chat.queue.remove')"
      :aria-label="$t('chat.queue.remove')"
      class="size-7 shrink-0 text-muted-foreground"
      @pointerdown.prevent
      @click="emit('remove')"
    >
      <Trash2 class="size-3.5" />
    </Button>
  </div>
</template>

<script setup lang="ts">
import { CircleCheck, CornerDownLeft, GripVertical, Trash2 } from 'lucide-vue-next'
import { Button, Input } from '@felinic/ui'
import { ref, watch } from 'vue'
import type { EditableFollowUpQueueItem } from './use-session-follow-up-queue'

const props = defineProps<{
  item: EditableFollowUpQueueItem
  busy: boolean
}>()
const draft = ref(props.item.text)
watch(() => props.item.text, value => { draft.value = value })

const emit = defineEmits<{
  save: [text: string]
  steer: []
  remove: []
}>()
</script>
