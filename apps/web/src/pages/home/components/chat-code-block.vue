<template>
  <!-- One uniform code block for every language (no per-language label / no
       terminal "run" chrome). The frame and copy button are CodePanel's;
       highlighting is delegated to the shared CodeBlock kernel. -->
  <CodePanel
    :code="code"
    width="fit"
    :copy-label="t('common.copy')"
    :copied-label="t('common.copied')"
    :copy-failed-message="t('common.copyFailed')"
    class="chat-code-block my-2"
  >
    <CodeBlock
      :code="code"
      :lang="language || 'text'"
    />
  </CodePanel>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { CodePanel } from '@felinic/ui'
import CodeBlock from './code-block.vue'

interface CodeFenceNode {
  type: string
  language?: string
  code?: string
  raw?: string
  loading?: boolean
}

const props = defineProps<{ node: CodeFenceNode }>()
const { t } = useI18n()
const code = computed(() => props.node.code ?? props.node.raw ?? '')
const language = computed(() => (props.node.language ?? '').trim().toLowerCase())
</script>
