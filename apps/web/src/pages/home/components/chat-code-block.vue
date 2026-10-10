<template>
  <!-- One uniform code block for every language (no per-language label / no
       terminal "run" chrome): a hairline divider frame and pure-white body.
       Layout is a flex row — the code scrolls inside its own column while the
       copy button is a stable trailing sibling (top-aligned). This keeps the
       block fit-to-content without the awkward reserved gap, and the button no
       longer jitters as the width grows during streaming. Highlighting itself
       is delegated to the shared CodeBlock kernel. -->
  <div class="chat-code-block my-2 flex w-fit max-w-full items-start gap-1.5 overflow-hidden rounded-lg border border-border/60 bg-white py-1 pl-3.5 pr-1.5 dark:bg-card">
    <CodeBlock
      :code="code"
      :lang="language || 'text'"
      class="overflow-x-auto py-1.5 text-[13px] leading-[1.8]"
    />
    <CopyActionButton
      :text="code"
      icon-class="size-3.5"
      class="shrink-0 text-muted-foreground focus-visible:ring-0 hover:text-foreground"
    />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import CodeBlock from './code-block.vue'
import CopyActionButton from './copy-action-button.vue'

interface CodeFenceNode {
  type: string
  language?: string
  code?: string
  raw?: string
  loading?: boolean
}

const props = defineProps<{ node: CodeFenceNode }>()
const code = computed(() => props.node.code ?? props.node.raw ?? '')
const language = computed(() => (props.node.language ?? '').trim().toLowerCase())
</script>
