<template>
  <div class="space-y-1.5">
    <DiffPanel
      v-if="diffText"
      :diff="diffText"
    />
    <!-- Older records carry no server-computed unified diff; compare the
         recorded old/new text directly instead. -->
    <CodeDiff
      v-else-if="hasChanges"
      :original="oldText"
      :modified="newText"
      :filename="extractFilename(filePath)"
      :theme="theme"
      :theme-type="themeType"
      :font-size="fontSize"
      :line-height="lineHeight"
      :font-family="fontFamily"
      :fill="false"
      max-height="24rem"
    />
    <EmptyRow v-else>
      {{ t('chat.tools.detail.noChanges') }}
    </EmptyRow>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ToolCallBlock } from '@/store/chat-list'
import { extractFilename } from '@/utils/unified-diff'
import { useCodeSurfaceAppearance } from '@/components/code-surface/theme'
import CodeDiff from '@/components/code-surface/CodeDiff.vue'
import EmptyRow from './tool-detail/empty-row.vue'
import DiffPanel from './tool-call-diff-panel.vue'

const props = defineProps<{ block: ToolCallBlock }>()
const { t } = useI18n()
const { theme, themeType, fontSize, lineHeight, fontFamily } = useCodeSurfaceAppearance()

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
</script>
