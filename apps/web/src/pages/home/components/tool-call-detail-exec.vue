<template>
  <div class="space-y-1.5 font-mono text-[12px] leading-relaxed">
    <div
      v-if="command"
      class="text-muted-foreground whitespace-pre-wrap break-all"
    >
      $ {{ command }}
    </div>
    <div
      v-if="exitLabel"
      class="text-muted-foreground"
    >
      {{ exitLabel }}
    </div>
    <div
      v-if="backgroundMeta.length"
      class="flex flex-wrap gap-x-2 gap-y-0.5 text-caption text-muted-foreground"
    >
      <span
        v-for="item in backgroundMeta"
        :key="item"
      >{{ item }}</span>
    </div>
    <pre
      v-if="progressText"
      class="text-foreground overflow-x-auto whitespace-pre-wrap break-all max-h-72 overflow-y-auto"
    >{{ progressText }}</pre>
    <pre
      v-if="backgroundOutput"
      class="text-foreground overflow-x-auto whitespace-pre-wrap break-all max-h-72 overflow-y-auto"
    >{{ backgroundOutput }}</pre>
    <pre
      v-if="stdout"
      class="text-foreground overflow-x-auto whitespace-pre-wrap break-all max-h-72 overflow-y-auto"
    >{{ stdout }}</pre>
    <pre
      v-if="stderr"
      class="text-destructive overflow-x-auto whitespace-pre-wrap break-all max-h-72 overflow-y-auto"
    >{{ stderr }}</pre>
    <pre
      v-if="errorText"
      class="text-destructive overflow-x-auto whitespace-pre-wrap break-all max-h-72 overflow-y-auto"
    >{{ errorText }}</pre>
    <pre
      v-if="fallbackText"
      class="text-foreground overflow-x-auto whitespace-pre-wrap break-all max-h-72 overflow-y-auto"
    >{{ fallbackText }}</pre>
    <EmptyRow v-if="!progressText && !backgroundOutput && !stdout && !stderr && !errorText && !fallbackText">
      {{ isBackgroundActive ? t('chat.tools.detail.waitingOutput') : t('chat.tools.detail.noOutput') }}
    </EmptyRow>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ToolCallBlock } from '@/store/chat-list'
import EmptyRow from './tool-detail/empty-row.vue'

const props = defineProps<{ block: ToolCallBlock }>()
const { t } = useI18n()

const command = computed(() => {
  const input = props.block.input as Record<string, unknown> | undefined
  return typeof input?.command === 'string' ? input.command : ''
})

const backgroundTask = computed(() => props.block.backgroundTask)

function resolveResult(): Record<string, unknown> | null {
  if (!props.block.result) return null
  const result = props.block.result as Record<string, unknown>
  const sc = result.structuredContent as Record<string, unknown> | undefined
  return sc ?? result
}

// 退出码属于按需查看的执行诊断，不应出现在工具标题中。
const exitLabel = computed(() => {
  if (!props.block.done) return ''
  const result = resolveResult()
  const code = result?.exit_code ?? result?.exitCode
  return typeof code === 'number' && code !== 0
    ? t('chat.tools.exitCode', { code }) : ''
})

const stdout = computed(() => {
  const r = resolveResult()
  return (r?.stdout as string) ?? ''
})

const stderr = computed(() => {
  const r = resolveResult()
  return (r?.stderr as string) ?? ''
})

const backgroundOutput = computed(() =>
  backgroundTask.value?.outputTail
  || backgroundTask.value?.chunk
  || '',
)

const isBackgroundActive = computed(() => {
  const status = (backgroundTask.value?.status ?? '').trim().toLowerCase()
  return status === 'running' || status === 'stalled'
})

const backgroundMeta = computed(() => {
  const task = backgroundTask.value
  if (!task?.taskId) return []
  const items = [task.taskId]
  if (task.status) items.push(task.status)
  if (task.duration) items.push(task.duration)
  if (task.outputFile) items.push(task.outputFile)
  return items
})

const errorText = computed(() => {
  if (!props.block.result) return ''
  const result = props.block.result as Record<string, unknown>
  if (result.isError !== true) return ''
  const content = result.content as Array<Record<string, unknown>> | undefined
  if (!Array.isArray(content)) return ''
  return content
    .filter(c => c.type === 'text')
    .map(c => c.text as string)
    .join('\n')
})

// Fallback for tools whose output lands in the MCP-style content[].text array
// (rather than structuredContent.stdout). Only used when nothing else matched
// so we never silently show "no output" when output is actually present.
const fallbackText = computed(() => {
  if (stdout.value || stderr.value || errorText.value) return ''
  const result = props.block.result as Record<string, unknown> | null
  if (!result || !Array.isArray(result.content)) return ''
  return (result.content as Array<Record<string, unknown>>)
    .filter(c => c.type === 'text')
    .map(c => c.text as string)
    .filter(Boolean)
    .join('\n')
})

const progressText = computed(() =>
  (props.block.progress ?? [])
    .map(item => formatProgress(item))
    .filter(Boolean)
    .join('\n'),
)

function formatProgress(val: unknown): string {
  if (typeof val === 'string') return val
  try {
    return JSON.stringify(val, null, 2)
  }
  catch {
    return String(val)
  }
}
</script>
