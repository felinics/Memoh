<script setup lang="ts">
import { computed, inject, type Ref } from 'vue'
import { MermaidBlockNode, type CodeBlockNodeProps } from 'markstream-vue'
import { useSettingsStore } from '@/store/settings'
import { applyMermaidThemeToSource, resolveMermaidIsDark } from '@/store/settings/mermaid'

// 2.0's own `CodeBlockNode` export is the block component (value), which shadows
// the parser's node type of the same name; the node type rides on the props.
type CodeBlockNode = CodeBlockNodeProps['node']

defineOptions({ inheritAttrs: false })

const props = defineProps<{
  node: CodeBlockNode
  loading?: boolean
  isDark?: boolean
}>()

const settings = useSettingsStore()

// markstream mutates `node.code` in place (same object reference) on every stream
// tick and bumps this injected version ref to drive its own updates. Depend on it
// so the themed copy re-derives as the source grows; otherwise a non-auto theme
// freezes the diagram at its first partial frame. Key is markstream-internal.
const streamVersion = inject<Ref<number> | undefined>('markstreamStreamVersion', undefined)

const themedNode = computed<CodeBlockNode>(() => {
  if (settings.mermaidTheme === 'auto') return props.node
  void streamVersion?.value
  const code = props.node?.code ?? ''
  const next = applyMermaidThemeToSource(code, settings.mermaidTheme)
  if (next === code) return props.node
  return { ...props.node, code: next }
})

const themedIsDark = computed(() =>
  resolveMermaidIsDark(settings.mermaidTheme, Boolean(props.isDark)),
)
</script>

<template>
  <MermaidBlockNode
    v-bind="$attrs"
    :node="themedNode"
    :loading="loading"
    :is-dark="themedIsDark"
  />
</template>
