<template>
  <CodeSurface
    v-if="text"
    :code="text"
    :filename="highlightFilename"
    :theme="surfaceTheme"
    :theme-type="surfaceThemeType"
    :font-size="surfaceFontSize"
    :line-height="surfaceLineHeight"
    :font-family="surfaceFontFamily"
    max-height="18rem"
    :line-numbers="false"
  />
  <EmptyRow v-else>
    {{ t('chat.tools.detail.noOutput') }}
  </EmptyRow>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ToolCallBlock } from '@/store/chat-list'
import CodeSurface from '@/components/code-surface/CodeSurface.vue'
import { useCodeSurfaceAppearance } from '@/components/code-surface/theme'
import EmptyRow from './tool-detail/empty-row.vue'

const props = defineProps<{ block: ToolCallBlock }>()
const { t } = useI18n()
const {
  theme: surfaceTheme,
  themeType: surfaceThemeType,
  fontSize: surfaceFontSize,
  lineHeight: surfaceLineHeight,
  fontFamily: surfaceFontFamily,
} = useCodeSurfaceAppearance()

// Only file reads carry a meaningful language; directory listings and other
// plain outputs stay unhighlighted (empty filename -> plaintext kernel path).
const highlightFilename = computed(() => {
  if (props.block.toolName !== 'read') return ''
  const input = props.block.input as Record<string, unknown> | undefined
  return (input?.path as string) ?? ''
})

// Render the real tool output (file text / listing), handling every shape the
// backend uses: a plain `content` string, an MCP `content[].text` array, a
// `structuredContent` object, or a bare string result.
const text = computed(() => {
  const result = props.block.result
  if (result == null) return ''
  if (typeof result === 'string') return result
  const r = result as Record<string, unknown>
  if (typeof r.content === 'string') return r.content
  if (Array.isArray(r.content)) {
    const joined = (r.content as Array<Record<string, unknown>>)
      .filter(c => c.type === 'text')
      .map(c => c.text as string)
      .filter(Boolean)
      .join('\n')
    if (joined) return joined
  }
  const sc = r.structuredContent as Record<string, unknown> | undefined
  if (sc && typeof sc.content === 'string') return sc.content
  if (sc && typeof sc.output === 'string') return sc.output
  try {
    return JSON.stringify(sc ?? r, null, 2)
  }
  catch {
    return String(result)
  }
})
</script>
