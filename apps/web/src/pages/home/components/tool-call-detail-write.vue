<template>
  <div class="space-y-1.5">
    <p
      v-if="contentTruncated && !diffText"
      class="rounded-sm border border-border bg-muted/30 px-2 py-1 text-xs text-muted-foreground"
    >
      {{ t('chat.tools.detail.contentTruncated', { bytes: contentBytes }) }}
    </p>
    <DiffPanel
      v-if="diffText"
      :diff="diffText"
      :filename="extractFilename(filePath)"
    />
    <CodeBlock
      v-else-if="content"
      :code="content"
      :filename="filePath"
      class="max-h-96 overflow-y-auto overflow-x-auto text-xs leading-relaxed"
    />
    <EmptyRow v-else>
      {{ t('chat.tools.detail.noContent') }}
    </EmptyRow>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ToolCallBlock } from '@/store/chat-list'
import { extractFilename } from '@/composables/useShikiHighlighter'
import CodeBlock from './code-block.vue'
import EmptyRow from './tool-detail/empty-row.vue'
import DiffPanel from './tool-call-diff-panel.vue'

const props = defineProps<{ block: ToolCallBlock }>()
const { t } = useI18n()

// New write calls carry the same server-computed UI-only diff as edit: a
// brand-new file renders as all additions, an overwrite as a real diff
// against the previous content. Older records without it keep the plain
// content block below.
const diffText = computed(() => props.block.diff ?? '')

const filePath = computed(() => {
  const input = props.block.input as Record<string, unknown> | undefined
  return (input?.path as string) ?? ''
})

const content = computed(() => {
  const input = props.block.input as Record<string, unknown> | undefined
  return (input?.content as string) ?? ''
})

const contentTruncated = computed(() => {
  const input = props.block.input as Record<string, unknown> | undefined
  return input?.content_truncated === true
})

const contentBytes = computed(() => {
  const input = props.block.input as Record<string, unknown> | undefined
  const bytes = input?.content_bytes
  return typeof bytes === 'number' && Number.isFinite(bytes) ? bytes : 0
})
</script>
