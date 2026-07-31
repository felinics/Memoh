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
      class="overflow-x-auto py-1.5 text-[13px] leading-relaxed"
    />
    <Button
      variant="ghost"
      size="icon-sm"
      class="shrink-0 text-muted-foreground focus-visible:ring-0 hover:text-foreground"
      :aria-label="t('common.copy', 'Copy')"
      @click="copy"
    >
      <Check
        v-if="copied"
        class="size-3.5"
      />
      <Copy
        v-else
        class="size-3.5"
      />
    </Button>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { Check, Copy } from 'lucide-vue-next'
import { Button, toast, useClipboard } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
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
const { copyText } = useClipboard()

const code = computed(() => props.node.code ?? props.node.raw ?? '')
const language = computed(() => (props.node.language ?? '').trim().toLowerCase())

const copied = ref(false)
let resetTimer: ReturnType<typeof setTimeout> | null = null
async function copy() {
  const ok = await copyText(code.value)
  if (!ok) {
    toast.error(t('common.copyFailed'))
    return
  }
  copied.value = true
  if (resetTimer) clearTimeout(resetTimer)
  resetTimer = setTimeout(() => { copied.value = false }, 1500)
}
</script>
