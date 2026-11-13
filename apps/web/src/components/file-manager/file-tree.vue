<script setup lang="ts">
import { inject, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { HandlersFsFileInfo } from '@memohai/sdk'
import { InlineLoadingRow } from '@felinic/ui'
import { directoryEntriesFingerprint, sortDirsFirst } from './utils'
import { FileTreeKey } from './file-tree-context'
import FileTreeNode from './file-tree-node.vue'

const { t } = useI18n()
const ctx = inject(FileTreeKey)
if (!ctx) throw new Error('FileTree must be used within a FileTree provider')

const nodes = ref<HandlersFsFileInfo[]>([])
const loading = ref(false)
const loaded = ref(false)
let lastFingerprint: string | null = null

async function load() {
  loading.value = true
  try {
    const sorted = sortDirsFirst(await ctx!.listDirectory(ctx!.rootPath))
    const nextFingerprint = directoryEntriesFingerprint(sorted)
    if (nextFingerprint !== lastFingerprint) {
      nodes.value = sorted
      lastFingerprint = nextFingerprint
    }
    loaded.value = true
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(() => ctx!.refreshKey.value, load)
</script>

<template>
  <div class="py-1">
    <InlineLoadingRow
      v-if="loading && nodes.length === 0"
      class="justify-center py-10"
    >
      {{ t('common.loading') }}
    </InlineLoadingRow>

    <div
      v-else-if="loaded && nodes.length === 0"
      class="px-3 py-6 text-center text-xs text-muted-foreground"
    >
      {{ t('bots.files.empty') }}
    </div>

    <FileTreeNode
      v-for="entry in nodes"
      v-else
      :key="entry.path"
      :entry="entry"
      :depth="0"
    />
  </div>
</template>
