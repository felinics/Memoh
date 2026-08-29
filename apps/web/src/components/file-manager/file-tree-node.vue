<script setup lang="ts">
import { computed, inject, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useDark } from '@vueuse/core'
import {
  ChevronRight,
  Download,
  File,
  FileArchive,
  FolderPlus,
  SplitSquareHorizontal,
  SquarePen,
  Trash2,
  Upload,
} from 'lucide-vue-next'
import {
  Checkbox,
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
  Spinner,
} from '@felinic/ui'
import type { HandlersFsFileInfo } from '@memohai/sdk'
import { isArchiveFile, sortDirsFirst } from './utils'
import { resolveFileIcon } from './file-icon'
import { FileTreeKey } from './file-tree-context'
import {
  treeGlyphSlotClass,
  treeIndentClass,
  treeRowClass,
  treeRowIdleClass,
  treeRowSelectedClass,
} from './tree-row'
import { useTreeDisclosure } from './tree-disclosure'

const props = defineProps<{
  entry: HandlersFsFileInfo
  depth: number
}>()

const { t } = useI18n()
const ctx = inject(FileTreeKey)
if (!ctx) throw new Error('FileTreeNode must be used within a FileTree provider')
const tree = ctx
const isDark = useDark()

const children = ref<HandlersFsFileInfo[]>([])
const rowEl = ref<HTMLElement | null>(null)

const path = computed(() => props.entry.path ?? '')
const selectionMode = computed(() => tree.selectionMode.value)
const canWrite = computed(() => tree.canWrite.value)
const isActive = computed(() => !!path.value && tree.activePath.value === path.value)
const selected = computed(() => !!path.value && tree.isSelected(path.value))
const isArchive = computed(() => isArchiveFile(props.entry.name))

// Folders show only the disclosure chevron (no folder glyph); files map to a
// Seti type glyph by name/extension (color tracks the active theme).
const fileIcon = computed(() => resolveFileIcon(props.entry.name ?? '', isDark.value))

const { expanded, loaded, spinnerVisible, expand, toggle, reload } = useTreeDisclosure(async () => {
  if (!props.entry.isDir || !path.value) return false
  children.value = sortDirsFirst(await tree.listDirectory(path.value))
  return true
})

function onRowClick() {
  if (selectionMode.value && path.value) {
    tree.toggleSelect(props.entry, !selected.value)
    return
  }
  if (props.entry.isDir) toggle()
  else tree.openFile(props.entry)
}

// Re-fetch an expanded folder's children when the workspace changes.
watch(() => tree.refreshKey.value, () => {
  if (expanded.value) void reload()
})

// Reveal (deep-link): expand the chain of ancestor folders leading to the
// target, and scroll the target row into view.
function isOnRevealPath(): boolean {
  const reveal = tree.revealPath.value
  if (!reveal || !props.entry.isDir || !path.value) return false
  return reveal === path.value || reveal.startsWith(`${path.value}/`)
}

watch(() => tree.revealPath.value, async (reveal) => {
  if (isOnRevealPath() && !expanded.value) {
    await expand()
  }
  if (reveal && reveal === path.value) {
    requestAnimationFrame(() => rowEl.value?.scrollIntoView({ block: 'nearest' }))
  }
}, { immediate: true })

function onCheckbox(checked: boolean | 'indeterminate') {
  tree.toggleSelect(props.entry, checked === true)
}
</script>

<template>
  <ContextMenu>
    <ContextMenuTrigger as-child>
      <div
        ref="rowEl"
        :class="[treeRowClass, isActive ? treeRowSelectedClass : treeRowIdleClass]"
        @click="onRowClick"
      >
        <span
          v-for="g in depth"
          :key="g"
          :class="treeIndentClass"
        />

        <Checkbox
          v-if="selectionMode"
          :model-value="selected"
          class="mr-1.5 shrink-0"
          :aria-label="t('bots.files.selectItem', { name: entry.name ?? '' })"
          @click.stop
          @update:model-value="onCheckbox"
        />

        <span :class="treeGlyphSlotClass">
          <Spinner
            v-if="entry.isDir && spinnerVisible"
            class="text-muted-foreground"
          />
          <ChevronRight
            v-else-if="entry.isDir"
            :stroke-width="1.53"
            class="size-4 text-muted-foreground transition-[rotate]"
            :class="{ 'rotate-90': expanded && loaded }"
          />
          <span
            v-else
            class="seti-icon"
            :style="{ color: fileIcon.color }"
          >{{ fileIcon.char }}</span>
        </span>
        <span class="ml-1 min-w-0 flex-1 truncate">{{ entry.name }}</span>
      </div>
    </ContextMenuTrigger>
    <ContextMenuContent>
      <template v-if="!entry.isDir">
        <ContextMenuItem
          :disabled="isActive"
          @select="tree.openFilePinned(entry)"
        >
          <File class="mr-2 size-3.5" />
          {{ t('common.open') }}
        </ContextMenuItem>
        <ContextMenuItem @select="tree.openFileToSide(entry)">
          <SplitSquareHorizontal class="mr-2 size-3.5" />
          {{ t('bots.files.openToSide') }}
        </ContextMenuItem>
        <ContextMenuSeparator />
      </template>
      <template v-if="entry.isDir && canWrite">
        <ContextMenuItem @select="tree.requestNewFolder(entry)">
          <FolderPlus class="mr-2 size-3.5" />
          {{ t('bots.files.newFolder') }}
        </ContextMenuItem>
        <ContextMenuItem @select="tree.requestUpload(entry)">
          <Upload class="mr-2 size-3.5" />
          {{ t('bots.files.upload') }}
        </ContextMenuItem>
        <ContextMenuSeparator />
      </template>
      <ContextMenuItem @select="tree.requestDownload(entry)">
        <Download class="mr-2 size-3.5" />
        {{ t('bots.files.download') }}
      </ContextMenuItem>
      <ContextMenuItem
        v-if="canWrite && !entry.isDir && isArchive"
        @select="tree.requestExtract(entry)"
      >
        <FileArchive class="mr-2 size-3.5" />
        {{ t('bots.files.extract') }}
      </ContextMenuItem>
      <ContextMenuItem
        v-if="canWrite"
        @select="tree.requestRename(entry)"
      >
        <SquarePen class="mr-2 size-3.5" />
        {{ t('bots.files.rename') }}
      </ContextMenuItem>
      <ContextMenuSeparator v-if="canWrite" />
      <ContextMenuItem
        v-if="canWrite"
        variant="destructive"
        @select="tree.requestDelete(entry)"
      >
        <Trash2 class="mr-2 size-3.5" />
        {{ t('bots.files.delete') }}
      </ContextMenuItem>
    </ContextMenuContent>
  </ContextMenu>

  <!-- Children: kept mounted after first load to avoid re-mount cost on
       close/reopen. display:contents when expanded (no layout impact),
       display:none when collapsed (hidden but alive). -->
  <div
    v-if="entry.isDir && (expanded || loaded) && children.length > 0"
    :class="expanded ? 'contents' : 'hidden'"
  >
    <FileTreeNode
      v-for="child in children"
      :key="child.path"
      :entry="child"
      :depth="depth + 1"
    />
  </div>
</template>

<style scoped>
/* Seti glyphs are a private-use-area icon font; render at the 16px column size
 * with no smoothing artifacts. The @font-face for the family lives in
 * ./seti/seti.css (imported by ./file-icon). */
.seti-icon {
  font-family: 'seti';
  font-size: 20px;
  line-height: 1;
  font-style: normal;
  font-weight: normal;
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
}
</style>
