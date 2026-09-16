<script setup lang="ts">
import { computed } from 'vue'
import MarkdownRender, { enableKatex, enableMermaid, setCustomComponents } from 'markstream-vue'
import ThemedMermaidBlock from '@/components/themed-mermaid-block/index.vue'
import ChatCodeBlock from '@/pages/home/components/chat-code-block.vue'
import { useSettingsStore } from '@/store/settings'
import { disableInlineMath, registerSharedMarkdownComponents } from '@/components/markdown'

const props = withDefaults(defineProps<{
  content: string
  class?: string
}>(), {
  class: undefined,
})

enableKatex()
enableMermaid()
// Global mermaid override so the appearance preference wins over the markstream
// default (which only follows the host renderer's isDark flag); one registration
// covers chat + file preview + any other MarkdownRender call site.
setCustomComponents({ mermaid: ThemedMermaidBlock })
// File preview reuses the chat's design-system node components (library
// Checkbox task markers, link-language footnotes). It also uses the same
// non-Monaco code block as chat so the file editor's Monaco theme is not
// affected by code blocks in the preview.
registerSharedMarkdownComponents('file-preview-md', { code_block: ChatCodeBlock, shell: ChatCodeBlock })

const settings = useSettingsStore()
const isDark = computed(() => settings.isDark)
const codeFontRenderKey = computed(() => settings.codeFontStack)
</script>

<template>
  <div :class="['h-full w-full min-h-0 overflow-auto bg-surface-editor', props.class]">
    <div class="prose prose-sm dark:prose-invert max-w-none px-6 py-4 *:first:mt-0">
      <MarkdownRender
        :key="codeFontRenderKey"
        :content="props.content"
        :is-dark="isDark"
        :typewriter="false"
        :fade="false"
        :show-tooltips="false"
        :mermaid-props="{ showTooltips: false }"
        :custom-markdown-it="disableInlineMath"
        custom-id="file-preview-md"
      />
    </div>
  </div>
</template>
