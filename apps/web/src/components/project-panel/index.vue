<template>
  <!-- PUSH/PULL rail, mirroring the left sidebar: fixed width, margin-RIGHT
       slides it off-screen when closed so its flex footprint shrinks to 0 and
       the dock grows to fill the space. Only margin-right transitions; width
       stays untransitioned so the resize handle tracks the pointer 1:1. -->
  <aside
    class="relative flex shrink-0 flex-col border-l border-border bg-sidebar"
    :style="asideStyle"
    :inert="!panelOpen || undefined"
  >
    <!-- Resize handle on the LEFT edge (panel grows leftward). -->
    <div
      class="group absolute inset-y-0 left-0 w-1 cursor-col-resize"
      @mousedown="onResizeStart"
    >
      <div
        :class="[resizeRailClass, { 'bg-ring': isResizing }]"
      />
    </div>

    <header class="flex h-11 shrink-0 items-center gap-1 pl-4 pr-2">
      <span class="min-w-0 flex-1 truncate text-label font-medium text-foreground">
        {{ t('projects.title') }}
      </span>
      <Button
        variant="ghost"
        size="icon-sm"
        shape="circle"
        :class="headerButtonClass"
        :title="t('projects.newProject')"
        :aria-label="t('projects.newProject')"
        @click="beginCreateProject"
      >
        <Plus
          :stroke-width="1.75"
          class="size-4"
        />
      </Button>
      <!-- No collapse control here: the rail toggle lives in the tab strip
           (group-actions), same as the left sidebar's. -->
    </header>

    <div class="min-h-0 flex-1 overflow-y-auto px-2 pb-4">
      <InlineLoadingRow
        v-if="store.projectsLoading && !store.projectsLoaded"
        class="px-2"
      >
        {{ t('common.loading') }}
      </InlineLoadingRow>

      <!-- Empty state keeps the panel calm: one line + the one guiding action. -->
      <div
        v-else-if="store.projectsLoaded && store.projects.length === 0"
        class="flex flex-col items-center gap-3 px-4 py-12 text-center"
      >
        <p class="text-body text-muted-foreground">
          {{ t('projects.emptyHint') }}
        </p>
        <Button
          variant="outline"
          size="sm"
          @click="beginCreateProject"
        >
          <Plus />
          {{ t('projects.newProject') }}
        </Button>
      </div>

      <!-- A Project is a CATEGORY, not a tree row: its name is a quiet section
           label, and the two things it actually contains — Issues and Wiki —
           are the rows underneath. The doc tree hangs off Wiki, never beside
           Issues. -->
      <section
        v-for="project in store.projects"
        :key="project.id"
        class="mt-3 first:mt-1"
      >
        <ContextMenu>
          <ContextMenuTrigger as-child>
            <h2 :class="projectLabelClass">
              <span class="min-w-0 flex-1 truncate">{{ project.name }}</span>
            </h2>
          </ContextMenuTrigger>
          <ContextMenuContent>
            <ContextMenuItem @select="beginCreateDoc(project, null)">
              <Plus class="mr-2 size-3.5" />
              {{ t('projects.newDoc') }}
            </ContextMenuItem>
            <ContextMenuItem @select="beginRenameProject(project)">
              <Pencil class="mr-2 size-3.5" />
              {{ t('common.rename') }}
            </ContextMenuItem>
            <ContextMenuSeparator />
            <ContextMenuItem
              variant="destructive"
              @select="deleteProjectTarget = project"
            >
              <Trash2 class="mr-2 size-3.5" />
              {{ t('common.delete') }}
            </ContextMenuItem>
          </ContextMenuContent>
        </ContextMenu>

        <!-- Issues: opens the kanban tab. Deliberately not expandable —
             issues would drown the doc tree. -->
        <NavRow
          :label="t('projects.issues')"
          @activate="openKanban(project)"
        >
          <template #icon>
            <SquareKanban class="size-3.5" />
          </template>
        </NavRow>

        <!-- Wiki: the doc tree's root row. -->
        <NavRow
          :label="t('projects.wiki')"
          expandable
          :expanded="isWikiExpanded(project)"
          @activate="toggleWiki(project)"
          @toggle="toggleWiki(project)"
        >
          <template #icon>
            <BookText class="size-3.5" />
          </template>
          <template #actions>
            <Button
              variant="ghost"
              size="icon-sm"
              shape="circle"
              :class="rowActionClass"
              :title="t('projects.newDoc')"
              :aria-label="t('projects.newDoc')"
              @click.stop="beginCreateDoc(project, null)"
            >
              <Plus
                :stroke-width="1.75"
                class="size-3.5"
              />
            </Button>
          </template>
        </NavRow>

        <template v-if="isWikiExpanded(project)">
          <InlineLoadingRow
            v-if="store.treeLoading[project.id!] && !store.trees[project.id!]"
            class="pl-8"
          >
            {{ t('common.loading') }}
          </InlineLoadingRow>
          <p
            v-else-if="(store.trees[project.id!]?.length ?? 0) === 0"
            class="py-1 pl-8 text-body text-muted-foreground"
          >
            {{ t('projects.wikiEmpty') }}
          </p>
          <TreeNode
            v-for="node in store.childrenOf(project.id!, null)"
            :key="node.id"
            :project-id="project.id!"
            :node="node"
            :depth="1"
            @create-child="beginCreateDoc(project, $event)"
            @rename="beginRenameDoc(project, $event)"
            @delete="deleteNodeTarget = { project, node: $event }"
          />
        </template>
      </section>
    </div>

    <NameDialog
      :open="nameDialog !== null"
      :title="nameDialogCopy.title"
      :label="nameDialogCopy.label"
      :placeholder="nameDialogCopy.placeholder"
      :confirm-label="nameDialogCopy.confirm"
      :initial-value="nameDialog?.initialValue"
      :busy="nameDialogBusy"
      @update:open="(v: boolean) => { if (!v) nameDialog = null }"
      @confirm="confirmNameDialog"
    />

    <ConfirmDeleteDialog
      :open="!!deleteProjectTarget"
      :title="t('projects.deleteProjectTitle')"
      :description="t('projects.deleteProjectConfirm', { name: deleteProjectTarget?.name ?? '' })"
      :cancel-label="t('common.cancel')"
      :confirm-label="t('common.delete')"
      :loading="deleting"
      @update:open="(v: boolean) => { if (!v) deleteProjectTarget = null }"
      @confirm="confirmDeleteProject"
    />
    <ConfirmDeleteDialog
      :open="!!deleteNodeTarget"
      :title="t('projects.deleteDocTitle')"
      :description="t('projects.deleteDocConfirm', { name: deleteNodeTarget?.node.title ?? '' })"
      :cancel-label="t('common.cancel')"
      :confirm-label="t('common.delete')"
      :loading="deleting"
      @update:open="(v: boolean) => { if (!v) deleteNodeTarget = null }"
      @confirm="confirmDeleteNode"
    />
  </aside>
</template>

<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { BookText, Pencil, Plus, SquareKanban, Trash2 } from 'lucide-vue-next'
import {
  Button,
  ConfirmDeleteDialog,
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
  InlineLoadingRow,
  toast,
} from '@felinic/ui'
import type { ProjectProject, ProjectTreeNode } from '@memohai/sdk'
import { useProjectsStore } from '@/store/projects'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { resolveApiErrorMessage } from '@/utils/api-error'
import NameDialog from './name-dialog.vue'
import NavRow from './nav-row.vue'
import TreeNode from './tree-node.vue'
import { rowActionClass } from './row-chrome'

const { t } = useI18n()
const store = useProjectsStore()
const workspaceTabs = useWorkspaceTabsStore()
const { panelOpen, panelWidth } = storeToRefs(store)

// A category label, not a control: no hover fill and no chevron — it names the
// group of rows beneath it, the same role section labels play on settings pages.
// Its left padding matches NavRow's depth-0 indent so label and rows share an edge.
const projectLabelClass = 'flex min-w-0 items-center px-2 py-1 text-body font-medium text-muted-foreground'
// Resize rail hover, mirroring the left sidebar's handle.
const resizeRailClass = 'h-full w-full transition-colors group-hover:bg-border' /* ui-allow-style */
// Same chrome as the tab strip's icon buttons (prefix cluster / group actions),
// so the panel header and the strip read as one family.
const headerButtonClass = 'size-7 shrink-0 p-0 text-muted-foreground hover:text-foreground' /* ui-allow-style */

const asideStyle = computed<Record<string, string>>(() => ({
  width: `${panelWidth.value}px`,
  marginRight: panelOpen.value ? '0px' : `-${panelWidth.value}px`,
  transition: 'margin-right 300ms cubic-bezier(0.32, 0.72, 0, 1)',
  // Same sidebar-scoped ghost-hover lightening as the left rail.
  '--btn-ghost-hover': 'var(--sidebar-hover)',
}))

onMounted(() => {
  void store.loadProjects().catch((error) => {
    toast.error(resolveApiErrorMessage(error, t('projects.loadFailed')))
  })
})

// The Wiki row has no node id of its own (it is the tree's root, not a row in
// the data), so its expansion is keyed by a synthetic id that can never
// collide with a node UUID.
function wikiKey(project: ProjectProject): string {
  return `wiki:${project.id}`
}

function isWikiExpanded(project: ProjectProject): boolean {
  return !!project.id && store.isExpanded(wikiKey(project))
}

function toggleWiki(project: ProjectProject) {
  if (!project.id) return
  store.toggleExpanded(wikiKey(project))
  if (store.isExpanded(wikiKey(project))) {
    void store.loadTree(project.id).catch((error) => {
      toast.error(resolveApiErrorMessage(error, t('projects.loadFailed')))
    })
  }
}

function openKanban(project: ProjectProject) {
  if (!project.id) return
  workspaceTabs.openProjectKanban({ projectId: project.id, projectName: project.name })
}

// ---- name dialog (create/rename project, create/rename doc) ---------------

type NameDialogState
  = | { kind: 'create-project', initialValue?: string }
    | { kind: 'rename-project', project: ProjectProject, initialValue: string }
    | { kind: 'create-doc', project: ProjectProject, parent: ProjectTreeNode | null, initialValue?: string }
    | { kind: 'rename-doc', project: ProjectProject, node: ProjectTreeNode, initialValue: string }

const nameDialog = ref<NameDialogState | null>(null)
const nameDialogBusy = ref(false)

const nameDialogCopy = computed(() => {
  switch (nameDialog.value?.kind) {
    case 'rename-project':
      return { title: t('projects.renameProject'), label: t('projects.projectName'), placeholder: t('projects.projectNamePlaceholder'), confirm: t('common.rename') }
    case 'create-doc':
      return { title: t('projects.newDoc'), label: t('projects.docTitle'), placeholder: t('projects.docTitlePlaceholder'), confirm: t('common.create') }
    case 'rename-doc':
      return { title: t('projects.renameDoc'), label: t('projects.docTitle'), placeholder: t('projects.docTitlePlaceholder'), confirm: t('common.rename') }
    default:
      return { title: t('projects.newProject'), label: t('projects.projectName'), placeholder: t('projects.projectNamePlaceholder'), confirm: t('common.create') }
  }
})

function beginCreateProject() {
  nameDialog.value = { kind: 'create-project' }
}

function beginRenameProject(project: ProjectProject) {
  nameDialog.value = { kind: 'rename-project', project, initialValue: project.name ?? '' }
}

function beginCreateDoc(project: ProjectProject, parent: ProjectTreeNode | null) {
  nameDialog.value = { kind: 'create-doc', project, parent }
}

function beginRenameDoc(project: ProjectProject, node: ProjectTreeNode) {
  nameDialog.value = { kind: 'rename-doc', project, node, initialValue: node.title ?? '' }
}

async function confirmNameDialog(value: string) {
  const state = nameDialog.value
  if (!state) return
  nameDialogBusy.value = true
  try {
    switch (state.kind) {
      case 'create-project': {
        // A project renders as a category with its rows always visible, so
        // nothing to expand — but open its Wiki so the empty-state hint (and
        // the ＋ that fills it) is right there.
        const created = await store.createProject(value)
        if (created.id) store.setExpanded(wikiKey(created), true)
        break
      }
      case 'rename-project':
        await store.renameProject(state.project.id!, value)
        break
      case 'create-doc': {
        // Reveal where the new doc lands: the Wiki row, and the parent when
        // creating a sub-doc.
        store.setExpanded(wikiKey(state.project), true)
        if (state.parent?.id) store.setExpanded(state.parent.id, true)
        const nodeId = await store.createDoc(state.project.id!, value, state.parent?.id)
        if (nodeId) {
          workspaceTabs.openProjectDoc({ projectId: state.project.id!, nodeId, title: value })
        }
        break
      }
      case 'rename-doc':
        await store.renameDoc(state.project.id!, state.node.id!, value, state.node.version ?? 1)
        break
    }
    nameDialog.value = null
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('projects.saveFailed')))
  } finally {
    nameDialogBusy.value = false
  }
}

// ---- deletes ---------------------------------------------------------------

const deleteProjectTarget = ref<ProjectProject | null>(null)
const deleteNodeTarget = ref<{ project: ProjectProject, node: ProjectTreeNode } | null>(null)
const deleting = ref(false)

async function confirmDeleteProject() {
  const project = deleteProjectTarget.value
  if (!project?.id) return
  deleting.value = true
  try {
    await store.deleteProject(project.id)
    deleteProjectTarget.value = null
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('projects.deleteFailed')))
  } finally {
    deleting.value = false
  }
}

async function confirmDeleteNode() {
  const target = deleteNodeTarget.value
  if (!target?.node.id || !target.project.id) return
  deleting.value = true
  try {
    await store.deleteNode(target.project.id, target.node.id)
    deleteNodeTarget.value = null
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('projects.deleteFailed')))
  } finally {
    deleting.value = false
  }
}

// ---- resize ----------------------------------------------------------------

const MIN_WIDTH = 220
const MAX_WIDTH = 480
const isResizing = ref(false)
let cleanupResize: (() => void) | null = null

function onResizeStart(e: MouseEvent) {
  e.preventDefault()
  isResizing.value = true
  const startX = e.clientX
  const startWidth = panelWidth.value

  function onMouseMove(ev: MouseEvent) {
    // Panel sits on the RIGHT: dragging left grows it.
    const delta = startX - ev.clientX
    panelWidth.value = Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, startWidth + delta))
  }
  function onMouseUp() {
    isResizing.value = false
    document.removeEventListener('mousemove', onMouseMove)
    document.removeEventListener('mouseup', onMouseUp)
    cleanupResize = null
  }
  document.addEventListener('mousemove', onMouseMove)
  document.addEventListener('mouseup', onMouseUp)
  cleanupResize = onMouseUp
}

onBeforeUnmount(() => {
  cleanupResize?.()
})
</script>
