<template>
  <ContextMenu>
    <ContextMenuTrigger as-child>
      <button
        type="button"
        :class="rowClass"
        :style="{ paddingLeft: `${0.5 + depth * 0.875}rem` }"
        @click="openDoc"
      >
        <!-- Chevron doubles as the expand toggle; a leaf keeps the slot so
             titles across rows stay on one column. -->
        <span
          class="flex size-4 shrink-0 items-center justify-center rounded-sm text-muted-foreground"
          :class="hasChildren ? chevronHoverClass : 'opacity-0'"
          @click.stop="hasChildren && store.toggleExpanded(node.id!)"
        >
          <ChevronRight
            class="size-3 transition-transform duration-150"
            :class="expanded ? 'rotate-90' : ''"
          />
        </span>
        <FileText class="size-3.5 shrink-0 text-muted-foreground" />
        <span class="min-w-0 flex-1 truncate">{{ node.title || t('projects.untitled') }}</span>
      </button>
    </ContextMenuTrigger>
    <ContextMenuContent>
      <ContextMenuItem @select="emit('create-child', node)">
        <Plus class="mr-2 size-3.5" />
        {{ t('projects.newSubDoc') }}
      </ContextMenuItem>
      <ContextMenuItem @select="emit('rename', node)">
        <Pencil class="mr-2 size-3.5" />
        {{ t('common.rename') }}
      </ContextMenuItem>
      <ContextMenuSeparator />
      <ContextMenuItem
        variant="destructive"
        @select="emit('delete', node)"
      >
        <Trash2 class="mr-2 size-3.5" />
        {{ t('common.delete') }}
      </ContextMenuItem>
    </ContextMenuContent>
  </ContextMenu>

  <template v-if="expanded">
    <TreeNode
      v-for="child in children"
      :key="child.id"
      :project-id="projectId"
      :node="child"
      :depth="depth + 1"
      @create-child="emit('create-child', $event)"
      @rename="emit('rename', $event)"
      @delete="emit('delete', $event)"
    />
  </template>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight, FileText, Pencil, Plus, Trash2 } from 'lucide-vue-next'
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from '@felinic/ui'
import type { ProjectTreeNode } from '@memohai/sdk'
import { useProjectsStore } from '@/store/projects'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'

const props = defineProps<{
  projectId: string
  node: ProjectTreeNode
  depth: number
}>()

const emit = defineEmits<{
  'create-child': [node: ProjectTreeNode]
  'rename': [node: ProjectTreeNode]
  'delete': [node: ProjectTreeNode]
}>()

const { t } = useI18n()
const store = useProjectsStore()
const workspaceTabs = useWorkspaceTabsStore()

// Hand-rolled nav rows (no tree primitive in @felinic/ui): the hover fill is
// the row's own chrome, same family as the sidebar session rows.
const rowClass = 'group flex w-full min-w-0 cursor-pointer items-center gap-1 rounded-md py-1 pr-2 text-left text-label text-foreground hover:bg-[color:var(--sidebar-hover)]' /* ui-allow-style */
const chevronHoverClass = 'hover:bg-[color:var(--ui-hover)] hover:text-foreground' /* ui-allow-style */

const children = computed(() => store.childrenOf(props.projectId, props.node.id ?? null))
const hasChildren = computed(() => children.value.length > 0)
const expanded = computed(() => !!props.node.id && store.isExpanded(props.node.id))

function openDoc() {
  if (!props.node.id) return
  workspaceTabs.openProjectDoc({
    projectId: props.projectId,
    nodeId: props.node.id,
    title: props.node.title,
  })
}
</script>
