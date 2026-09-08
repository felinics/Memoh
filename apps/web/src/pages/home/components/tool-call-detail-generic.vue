<template>
  <div class="space-y-0.5 text-xs leading-relaxed">
    <div
      v-for="entry in inputEntries"
      :key="entry.k"
      class="flex gap-1.5 font-mono break-all"
    >
      <span class="shrink-0 text-muted-foreground">{{ entry.k }}:</span>
      <span class="min-w-0 text-foreground">{{ entry.v }}</span>
    </div>
    <div
      v-if="errorText"
      class="font-mono whitespace-pre-wrap break-all text-destructive"
    >
      {{ errorText }}
    </div>
    <div
      v-else-if="resultText"
      class="font-mono whitespace-pre-wrap break-all text-foreground"
    >
      {{ resultText }}
    </div>
    <EmptyRow v-if="!inputEntries.length && !resultText && !errorText">
      {{ t('chat.tools.detail.noData') }}
    </EmptyRow>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ToolCallBlock } from '@/store/chat-list'
import EmptyRow from './tool-detail/empty-row.vue'
import { hasToolResultError, toolResultErrorText } from './tool-result-error'

const props = defineProps<{ block: ToolCallBlock }>()
const { t } = useI18n()

function stringify(val: unknown): string {
  if (val == null) return ''
  if (typeof val === 'string') return val
  try {
    return JSON.stringify(val)
  }
  catch {
    return String(val)
  }
}

// Keep input parameters readable as key:value rows inside the shared detail card.
const inputEntries = computed(() => {
  const input = props.block.input
  if (!input || typeof input !== 'object' || Array.isArray(input)) return []
  return Object.entries(input as Record<string, unknown>)
    .filter(([, v]) => v !== undefined && v !== null && v !== '')
    .map(([k, v]) => ({ k, v: stringify(v) }))
})

const isError = computed(() => hasToolResultError(props.block))

function extractText(): string {
  const r = props.block.result
  if (r == null) return ''
  if (typeof r === 'string') return r
  const obj = r as Record<string, unknown>
  if (Array.isArray(obj.content)) {
    const texts = (obj.content as Array<Record<string, unknown>>)
      .filter(c => c.type === 'text')
      .map(c => c.text as string)
      .filter(Boolean)
    if (texts.length) return texts.join('\n')
  }
  if (typeof obj.content === 'string') return obj.content
  const sc = obj.structuredContent
  return sc ? stringify(sc) : ''
}

const errorText = computed(() => isError.value
  ? toolResultErrorText(props.block.result) || t('chat.tools.detail.failed')
  : '')
const resultText = computed(() => (isError.value ? '' : extractText()))
</script>
