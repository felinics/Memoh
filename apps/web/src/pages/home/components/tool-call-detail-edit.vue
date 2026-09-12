<template>
  <div class="space-y-1.5">
    <DiffPanel
      v-if="diffText"
      :diff="diffText"
      :filename="extractFilename(filePath)"
    />
    <div
      v-else-if="hasChanges && shiki.loading.value"
      class="flex items-center gap-1.5 text-xs text-muted-foreground"
    >
      <LoaderCircle class="size-3 animate-spin" />
    </div>
    <!-- eslint-disable vue/no-v-html -->
    <div
      v-else-if="hasChanges"
      class="shiki-diff-container overflow-x-auto overflow-y-auto max-h-96 text-xs font-mono [&_pre]:bg-transparent! [&_pre]:p-2 [&_pre]:m-0 [&_code]:text-xs"
      v-html="shiki.html.value"
    />
    <!-- eslint-enable vue/no-v-html -->
    <EmptyRow v-else>
      {{ t('chat.tools.detail.noChanges') }}
    </EmptyRow>
  </div>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { LoaderCircle } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { ToolCallBlock } from '@/store/chat-list'
import { extractFilename, useShikiHighlighter } from '@/composables/useShikiHighlighter'
import EmptyRow from './tool-detail/empty-row.vue'
import DiffPanel from './tool-call-diff-panel.vue'

const props = defineProps<{ block: ToolCallBlock }>()
const { t } = useI18n()
const shiki = useShikiHighlighter()

function asObject(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' ? (value as Record<string, unknown>) : {}
}

// New edit calls carry a unified diff computed server-side from the file
// content actually read and written, attached to the tool call as UI-only
// metadata (the model never sees it); the shared panel renders it with its
// real surrounding context.
const diffText = computed(() => props.block.diff ?? '')

const filePath = computed(() => {
  const input = asObject(props.block.input)
  return (input.path as string) ?? ''
})

const oldText = computed(() => {
  const input = asObject(props.block.input)
  return (input.old_text as string) ?? ''
})

const newText = computed(() => {
  const input = asObject(props.block.input)
  return (input.new_text as string) ?? ''
})

const hasChanges = computed(() => Boolean(oldText.value || newText.value))

// Older records have no server-computed diff; keep the plain old/new block
// view for them. Re-highlight on stream-in: input lands after the tool block
// first renders, so an onMounted-only highlight would miss it.
watch(
  [diffText, oldText, newText, filePath],
  ([diff, oldT, newT, path]) => {
    if (!diff && (oldT || newT)) {
      void shiki.highlightDiff(oldT, newT, extractFilename(path))
    }
  },
  { immediate: true },
)
</script>

<style>
.shiki-diff-container .diff-block pre {
  margin: 0 !important;
  padding: 0.5rem 0.75rem !important;
  background: transparent !important;
}
.shiki-diff-container .diff-block.diff-remove {
  background-color: var(--diff-remove);
  border-left: 3px solid var(--diff-remove-border);
}
.shiki-diff-container .diff-block.diff-add {
  background-color: var(--diff-add);
  border-left: 3px solid var(--diff-add-border);
}
</style>
