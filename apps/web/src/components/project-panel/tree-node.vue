<template>
  <ContextMenu>
    <ContextMenuTrigger as-child>
      <NavRow
        :label="node.title || t('projects.untitled')"
        :depth="depth"
        :expandable="hasChildren"
        :expanded="expanded"
        @activate="openDoc"
        @toggle="toggle"
      >
        <template #icon>
          <FileText class="size-3.5" />
        </template>
        <template #actions>
          <!-- Creation must be reachable without a right-click. -->
          <Button
            variant="ghost"
            size="icon-sm"
            shape="circle"
            :class="rowActionClass"
            :title="t('projects.newSubDoc')"
            :aria-label="t('projects.newSubDoc')"
            @click.stop="emit('create-child', node)"
          >
            <Plus
              :stroke-width="1.75"
              class="size-3.5"
            />
          </Button>
        </template>
      </NavRow>
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
import { FileText, Pencil, Plus, Trash2 } from 'lucide-vue-next'
import {
  Button,
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from '@felinic/ui'
import type { ProjectTreeNode } from '@memohai/sdk'
import { useProjectsStore } from '@/store/projects'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import NavRow from './nav-row.vue'
import { rowActionClass } from './row-chrome'

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

const children = computed(() => store.childrenOf(props.projectId, props.node.id ?? null))
const hasChildren = computed(() => children.value.length > 0)
const expanded = computed(() => !!props.node.id && store.isExpanded(props.node.id))

function toggle() {
  if (!hasChildren.value || !props.node.id) return
  store.toggleExpanded(props.node.id)
}

function openDoc() {
  if (!props.node.id) return
  workspaceTabs.openProjectDoc({
    projectId: props.projectId,
    nodeId: props.node.id,
    title: props.node.title,
  })
}
</script>
