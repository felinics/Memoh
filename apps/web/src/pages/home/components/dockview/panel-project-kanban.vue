<template>
  <DockPanelFrame>
    <PanePlaceholder
      v-if="loading && !loaded"
      loading
    >
      {{ t('common.loading') }}
    </PanePlaceholder>

    <div
      v-else
      class="flex h-full min-h-0 gap-3 overflow-x-auto bg-background p-3"
    >
      <div
        v-for="column in columns"
        :key="column.status"
        class="flex h-full w-64 shrink-0 flex-col"
      >
        <div class="flex shrink-0 items-center gap-1.5 px-1 pb-2">
          <span class="text-label font-medium text-foreground">{{ column.label }}</span>
          <span class="text-caption text-muted-foreground">{{ column.cards.length }}</span>
          <div class="flex-1" />
          <Button
            variant="ghost"
            size="icon-sm"
            class="size-6 rounded-sm p-0 text-muted-foreground"
            :title="t('projects.newIssue')"
            :aria-label="t('projects.newIssue')"
            @click="beginCreate(column.status)"
          >
            <Plus class="size-3.5" />
          </Button>
        </div>

        <!-- Sortable list root. Cards render from the SAME array sortablejs
             mutates positions in — the drop handler reads neighbors from the
             DOM order, computes the rank midpoint, and PATCHes status+rank in
             one call. -->
        <div
          :ref="el => registerColumnEl(column.status, el as HTMLElement | null)"
          :data-status="column.status"
          class="min-h-0 flex-1 space-y-2 overflow-y-auto rounded-md"
        >
          <button
            v-for="card in column.cards"
            :key="card.id"
            type="button"
            :data-node-id="card.id"
            :data-revision="card.revision"
            :class="cardClass"
            @click="openIssue(card)"
          >
            <div class="text-label text-foreground">
              {{ card.title || t('projects.untitled') }}
            </div>
            <div
              v-if="card.labels?.length || card.priority || card.due_at"
              class="mt-2 flex flex-wrap items-center gap-1.5"
            >
              <Badge
                v-if="card.priority"
                variant="outline"
              >
                {{ t(`projects.priority.${card.priority}`) }}
              </Badge>
              <Badge
                v-for="label in card.labels"
                :key="label.id"
                variant="secondary"
              >
                {{ label.name }}
              </Badge>
              <span
                v-if="card.due_at"
                class="text-caption text-muted-foreground"
              >
                {{ formatDueDate(card.due_at) }}
              </span>
            </div>
          </button>
        </div>
      </div>
    </div>

    <NameDialog
      :open="createStatus !== null"
      :title="t('projects.newIssue')"
      :label="t('projects.issueTitle')"
      :placeholder="t('projects.issueTitlePlaceholder')"
      :confirm-label="t('common.create')"
      :busy="creating"
      @update:open="(v: boolean) => { if (!v) createStatus = null }"
      @confirm="confirmCreate"
    />
  </DockPanelFrame>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Sortable from 'sortablejs'
import { Plus } from 'lucide-vue-next'
import { Badge, Button, PanePlaceholder, toast } from '@felinic/ui'
import type { DockviewApi, DockviewPanelApi } from 'dockview-vue'
import {
  getProjectsByProjectIdIssues,
  patchProjectsByProjectIdNodesByNodeIdIssue,
  postProjectsByProjectIdNodes,
  type ProjectIssue,
} from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { rankBetween } from '@/utils/lexorank'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import { usePanelVisible } from './use-panel-visible'
import NameDialog from '@/components/project-panel/name-dialog.vue'
import DockPanelFrame from './panel-frame.vue'

const props = defineProps<{
  params: {
    params: { projectId: string, projectName?: string }
    api: DockviewPanelApi
    containerApi: DockviewApi
  }
}>()

const { t, locale } = useI18n()
const workspaceTabs = useWorkspaceTabsStore()
const projectId = props.params.params.projectId

const STATUSES = ['todo', 'in_progress', 'done', 'cancelled'] as const
type IssueStatus = typeof STATUSES[number]

// Hand-rolled kanban card (no card-as-drag-item primitive): the whole card is
// the click/drag target, so its hover fill is the card's own chrome.
const cardClass = 'block w-full cursor-pointer rounded-md border border-border bg-card p-3 text-left hover:bg-[color:var(--ui-hover)]' /* ui-allow-style */

const issues = ref<ProjectIssue[]>([])
const loading = ref(false)
const loaded = ref(false)

const columns = computed(() => STATUSES.map(status => ({
  status,
  label: t(`projects.status.${status}`),
  cards: issues.value
    .filter(issue => issue.status === status)
    .sort((a, b) => {
      const ra = a.rank ?? ''
      const rb = b.rank ?? ''
      if (ra !== rb) return ra < rb ? -1 : 1
      return (a.id ?? '') < (b.id ?? '') ? -1 : 1
    }),
})))

async function load() {
  loading.value = true
  try {
    const { data } = await getProjectsByProjectIdIssues({
      path: { project_id: projectId },
      throwOnError: true,
    })
    issues.value = data ?? []
    loaded.value = true
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('projects.loadFailed')))
  } finally {
    loading.value = false
  }
}

// The board is a shared surface others mutate (issue detail tab, another
// user): refresh whenever this panel becomes the visible tab again.
const visible = usePanelVisible(props.params.api)
void load()
watch(visible, (isVisible) => {
  if (isVisible && loaded.value) void load()
})

function formatDueDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(locale.value, { month: 'short', day: 'numeric' }).format(date)
}

function openIssue(card: ProjectIssue) {
  if (!card.id) return
  workspaceTabs.openProjectIssue({ projectId, nodeId: card.id, title: card.title })
}

// ---- drag and drop ---------------------------------------------------------

const sortables = new Map<IssueStatus, Sortable>()

function registerColumnEl(status: IssueStatus, el: HTMLElement | null) {
  if (!el) return
  if (sortables.has(status)) return
  sortables.set(status, Sortable.create(el, {
    group: 'project-kanban',
    animation: 150,
    ghostClass: 'opacity-40',
    onEnd: (event) => { void onDrop(event) },
  }))
}

// One atomic PATCH per drop: status (target column) + rank (midpoint between
// DOM neighbors) under the issue's revision lock. On any failure the board
// reloads — sortablejs already moved the DOM, so the reload is also what
// rolls a rejected drop back.
async function onDrop(event: Sortable.SortableEvent) {
  const nodeId = event.item.dataset.nodeId
  const toStatus = (event.to as HTMLElement).dataset.status as IssueStatus | undefined
  if (!nodeId || !toStatus) return
  const issue = issues.value.find(i => i.id === nodeId)
  if (!issue) return

  const siblings = Array.from(event.to.children) as HTMLElement[]
  const index = siblings.indexOf(event.item)
  const prev = index > 0 ? siblings[index - 1]?.dataset.nodeId : undefined
  const next = index >= 0 && index < siblings.length - 1 ? siblings[index + 1]?.dataset.nodeId : undefined
  const prevRank = (prev ? issues.value.find(i => i.id === prev)?.rank : '') ?? ''
  const nextRank = (next ? issues.value.find(i => i.id === next)?.rank : '') ?? ''

  try {
    const rank = rankBetween(prevRank, nextRank)
    await patchProjectsByProjectIdNodesByNodeIdIssue({
      path: { project_id: projectId, node_id: nodeId },
      body: {
        expected_revision: issue.revision ?? 1,
        status: toStatus === issue.status ? null : toStatus,
        rank,
        assignee_user_id: null,
        assignee_bot_id: null,
        priority: null,
        due_at: null,
      },
      throwOnError: true,
    })
  } catch (error) {
    if (!isConflict(error)) {
      toast.error(resolveApiErrorMessage(error, t('projects.saveFailed')))
    } else {
      toast.error(t('projects.boardConflict'))
    }
  } finally {
    await load()
  }
}

function isConflict(error: unknown): boolean {
  const status = (error as { status?: number, response?: { status?: number } })
  return status?.status === 409 || status?.response?.status === 409
}

// ---- quick create ----------------------------------------------------------

const createStatus = ref<IssueStatus | null>(null)
const creating = ref(false)

function beginCreate(status: IssueStatus) {
  createStatus.value = status
}

async function confirmCreate(title: string) {
  const status = createStatus.value
  if (!status) return
  creating.value = true
  try {
    await postProjectsByProjectIdNodes({
      path: { project_id: projectId },
      body: { type: 'issue', title, body: '', parent_id: null, status },
      throwOnError: true,
    })
    createStatus.value = null
    await load()
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('projects.saveFailed')))
  } finally {
    creating.value = false
  }
}

onBeforeUnmount(() => {
  for (const sortable of sortables.values()) sortable.destroy()
  sortables.clear()
})
</script>
