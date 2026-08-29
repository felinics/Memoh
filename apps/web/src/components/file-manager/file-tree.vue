<script setup lang="ts">
import { inject, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { HandlersFsFileInfo } from '@memohai/sdk'
import { InlineLoadingRow } from '@felinic/ui'
import { sortDirsFirst } from './utils'
import { createSequentialLoader, nodeNeedsRefresh } from './freshness'
import { FileTreeKey } from './file-tree-context'
import FileTreeNode from './file-tree-node.vue'

const { t } = useI18n()
const ctx = inject(FileTreeKey)
if (!ctx) throw new Error('FileTree must be used within a FileTree provider')

const nodes = ref<HandlersFsFileInfo[]>([])
const loading = ref(false)
const loaded = ref(false)

// Background reloads keep the previous listing on failure; foreground loads
// keep the original toast-and-empty behavior via ctx.listDirectory.
const loader = createSequentialLoader(async (background) => {
  if (!background) loading.value = true
  try {
    nodes.value = sortDirsFirst(await ctx!.listDirectory(ctx!.rootPath, { background }))
    loaded.value = true
  } catch {
    // background refresh failed — keep what we have
  } finally {
    if (!background) loading.value = false
  }
})

onMounted(() => loader.request(false))
watch(() => ctx!.refreshSignal.value, (signal) => {
  if (nodeNeedsRefresh(ctx!.rootPath, signal)) loader.request(signal.background)
})
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
