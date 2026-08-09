<script setup lang="ts">
import { getBotsByBotIdContainerFsList } from '@memohai/sdk'
import DirectoryPickerNode from './directory-picker-node.vue'

// A directories-only tree for choosing ONE workspace directory. Same row shape
// and disclosure language as the Explorer tree (see ./tree-row), so a folder
// reads the same wherever it appears in the product — the picker is not a menu
// or a command palette and must not borrow their chrome.
//
// The caller supplies the bot and the root; everything else (listing, lazy
// expansion, per-node loading and retry) is owned here, so a host only needs
// `v-model` and no layout of its own.

const props = withDefaults(defineProps<{
  botId: string
  /** Absolute workspace path the tree is rooted at. */
  rootPath?: string
  /** Label for the root row — the surface's name, not a directory name. */
  rootLabel: string
  /** Absolute path of the row drawn as selected. */
  selectedPath: string
}>(), {
  rootPath: '/data',
})

// Selection is reported, not owned: the host decides what a clicked directory
// means (here: it becomes the target) and feeds the highlight back through
// `selectedPath`, so the tree can also follow a selection the host derived from
// something else — e.g. the name the user typed.
defineEmits<{ select: [path: string] }>()

async function listDirectory(path: string): Promise<string[]> {
  const { data } = await getBotsByBotIdContainerFsList({
    path: { bot_id: props.botId },
    query: { path },
    throwOnError: true,
  })
  return (data.entries ?? [])
    .filter(entry => entry.isDir && (entry.name ?? '').trim() && !(entry.name ?? '').startsWith('.'))
    .map(entry => entry.name ?? '')
    .sort((a, b) => a.localeCompare(b))
}
</script>

<template>
  <div class="overflow-hidden rounded-md border border-border">
    <div class="max-h-56 overflow-y-auto py-1">
      <DirectoryPickerNode
        :path="rootPath"
        :name="rootLabel"
        :depth="0"
        :selected-path="selectedPath"
        :list-directory="listDirectory"
        expand-on-mount
        @select="$emit('select', $event)"
      />
    </div>
  </div>
</template>
