<script setup lang="ts">
import type { CodeBlockNodeProps } from 'markstream-vue'
import { CodeBlockNode } from 'markstream-vue'
import { CodePanel } from '@felinic/ui'
import { computed, useAttrs } from 'vue'
import { useI18n } from 'vue-i18n'
import { useSettingsStore } from '@/store/settings'
import { useCodeSurfaceAppearance } from '@/components/code-surface/theme'

// Chat / file-preview fenced code. CodePanel owns the design-system frame (the
// menu-shell radius, card fill, edge rule and the copy button); CodeBlockNode
// owns rendering through stream-diffs, including streaming handoff and `diff`
// fences. The built-in header and its copy button are off because CodePanel
// already provides both.
interface CodeFenceNode {
  type: string
  language?: string
  code?: string
  raw?: string
  loading?: boolean
  diff?: boolean
}

const props = defineProps<{
  node: CodeFenceNode
  loading?: boolean
  stream?: boolean
}>()

const { t } = useI18n()
const settings = useSettingsStore()
const { theme, fontSize, lineHeight, fontFamily } = useCodeSurfaceAppearance()
const attrs = useAttrs()

const text = computed(() => props.node.code ?? props.node.raw ?? '')
const blockNode = computed(() => props.node as unknown as CodeBlockNodeProps['node'])

// markstream's renderer hands every code block its list position and height
// estimate; the built-in block uses them to keep scroll anchoring stable while
// earlier blocks stream in. This wrapper replaces that block, so it has to
// forward them rather than swallow them.
const forwardedBindings = computed(() => ({
  indexKey: attrs['index-key'],
  estimatedHeightPx: attrs.estimatedHeightPx,
  estimatedContentHeightPx: attrs.estimatedContentHeightPx,
  estimatedDiffInline: attrs.estimatedDiffInline,
}))

function firstToken(value: unknown): string {
  return String(value ?? '').trim().split(/\s+/, 1)[0] ?? ''
}

// Same rule markstream uses to route a fence to its diff rendering: the node
// flag, the fence's language token, or the info string of the raw opening line.
function fenceInfo(raw: unknown): string {
  const openingLine = String(raw ?? '').trimStart().split(/\r?\n|\r/, 1)[0] ?? ''
  return /^(?:`{3,}|~{3,})\s*(.*)$/.exec(openingLine)?.[1] ?? ''
}

const isDiff = computed(() => props.node.diff === true
  || firstToken(props.node.language) === 'diff'
  || firstToken(fenceInfo(props.node.raw)) === 'diff')

const codeBlockOptions = computed(() => ({
  fontSize: fontSize.value,
  lineHeight: lineHeight.value,
  fontFamily: fontFamily.value,
  tabSize: 2,
  overflow: 'scroll' as const,
  // stream-diffs declares `--diffs-bg` on its own shadow host, so the card fill
  // has to be injected there; an ancestor value would never win.
  unsafeCSS: ':host{--diffs-bg:var(--card)}',
}))
</script>

<template>
  <CodePanel
    :code="text"
    :width="isDiff ? 'full' : 'fit'"
    :copy-label="t('common.copy')"
    :copied-label="t('common.copied')"
    :copy-failed-message="t('common.copyFailed')"
    class="chat-code-block my-2"
  >
    <CodeBlockNode
      v-bind="forwardedBindings"
      :node="blockNode"
      :loading="props.loading ?? props.node.loading"
      :stream="props.stream"
      :theme="theme"
      :themes="[theme.dark, theme.light]"
      :is-dark="settings.isDark"
      :code-block-options="codeBlockOptions"
      :show-line-numbers="isDiff"
      :show-header="false"
      :show-copy-button="false"
      :show-collapse-button="false"
      :show-expand-button="false"
      :show-font-size-buttons="false"
      :show-preview-button="false"
      :show-tooltips="false"
    />
  </CodePanel>
</template>
