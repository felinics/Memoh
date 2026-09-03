<template>
  <div class="flex items-start gap-2 py-1.5 text-xs text-muted-foreground border-y border-border/60">
    <component
      :is="statusIcon"
      class="size-3.5 shrink-0 mt-0.5"
      :class="iconClass"
    />
    <div class="min-w-0 flex-1 space-y-0.5">
      <div class="flex items-center gap-1.5 min-w-0">
        <span
          class="shrink-0"
          :class="labelClass"
        >{{ statusLabel }}</span>
        <span
          v-if="task.command"
          class="font-mono truncate text-foreground/80"
          :title="task.command"
        >{{ task.command }}</span>
      </div>
      <div
        v-if="metaText"
        class="font-mono truncate text-muted-foreground/80"
        :title="metaText"
      >
        {{ metaText }}
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { CircleCheck, CircleX, LoaderCircle, SquareTerminal, TriangleAlert } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { BackgroundTask } from '@/store/chat-list'

const props = defineProps<{ task: BackgroundTask }>()
const { t } = useI18n()

const normalizedStatus = computed(() => (props.task.status || '').trim().toLowerCase())

const statusIcon = computed(() => {
  switch (normalizedStatus.value) {
    case 'running':
    case 'queued':
      return LoaderCircle
    case 'completed':
      return CircleCheck
    case 'failed':
    case 'killed':
      return CircleX
    case 'stalled':
    case 'unknown':
      return TriangleAlert
    default:
      return SquareTerminal
  }
})

const iconClass = computed(() => {
  switch (normalizedStatus.value) {
    case 'running':
    case 'queued':
      return 'animate-spin text-muted-foreground'
    case 'completed':
      return 'text-success-foreground'
    case 'failed':
    case 'killed':
      return 'text-destructive'
    case 'stalled':
    case 'unknown':
      return 'text-warning-foreground'
    default:
      return 'text-muted-foreground'
  }
})

const labelClass = computed(() => {
  switch (normalizedStatus.value) {
    case 'completed':
      return 'text-success-foreground'
    case 'failed':
    case 'killed':
      return 'text-destructive'
    case 'stalled':
    case 'unknown':
      return 'text-warning-foreground'
    default:
      return ''
  }
})

const statusLabel = computed(() => {
  const status = normalizedStatus.value || 'completed'
  return t(`chat.backgroundTask.${status}`, t('chat.backgroundTask.updated'))
})

const metaText = computed(() => {
  const parts: string[] = []
  if (typeof props.task.exitCode === 'number') parts.push(`exit ${props.task.exitCode}`)
  if (props.task.duration) parts.push(props.task.duration)
  if (props.task.outputFile) parts.push(props.task.outputFile)
  return parts.join(' | ')
})
</script>
