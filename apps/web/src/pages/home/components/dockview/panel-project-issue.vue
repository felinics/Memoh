<template>
  <DockPanelFrame>
    <PanePlaceholder
      v-if="loading && !detail"
      loading
    >
      {{ t('common.loading') }}
    </PanePlaceholder>

    <!-- Two columns: the issue itself reads as a document on the left, its
         properties sit in a narrow rail on the right. The rail is a sibling,
         not a card inside the content — properties belong to the issue, not to
         its description. -->
    <div
      v-else-if="detail"
      class="flex h-full min-h-0"
    >
      <div class="min-w-0 flex-1 overflow-y-auto [scrollbar-gutter:stable]">
        <div class="mx-auto flex min-h-full max-w-2xl flex-col px-6 pb-6 pt-8">
          <!-- Identifier line: the short handle people quote in conversation. -->
          <p class="text-body text-muted-foreground">
            {{ issueHandle }}
          </p>

          <input
            v-model="titleDraft"
            type="text"
            class="mt-2 w-full bg-transparent text-heading font-semibold text-foreground outline-none placeholder:text-muted-foreground"
            :placeholder="t('projects.untitled')"
            :aria-label="t('projects.issueTitle')"
            @blur="commitTitle"
            @keydown.enter.prevent="commitTitle"
          >

          <div
            v-if="conflict"
            class="mt-4 flex items-center gap-2 rounded-md border border-destructive-border bg-destructive-soft px-3 py-2 text-body"
          >
            <span class="min-w-0 flex-1">{{ t('projects.conflictHint') }}</span>
            <Button
              variant="outline"
              size="sm"
              @click="reloadRemote"
            >
              {{ t('projects.reload') }}
            </Button>
          </div>

          <!-- Description reads inline; the edit affordance appears on hover so
               a reader is never looking at a toolbar they did not ask for. -->
          <div class="group/desc mt-5">
            <div class="mb-1.5 flex h-6 items-center">
              <span class="text-label font-medium text-muted-foreground">
                {{ t('projects.description') }}
              </span>
              <div class="flex-1" />
              <TextButton
                variant="ghost"
                :class="editToggleClass"
                @click="mode = mode === 'edit' ? 'preview' : 'edit'"
              >
                {{ mode === 'edit' ? t('projects.preview') : t('common.edit') }}
              </TextButton>
            </div>
            <div
              v-if="mode === 'edit'"
              class="h-64 overflow-hidden rounded-md border border-border"
            >
              <MonacoEditor
                v-model="bodyDraft"
                language="markdown"
              />
            </div>
            <MarkdownPreview
              v-else-if="bodyDraft.trim()"
              :content="bodyDraft"
            />
            <p
              v-else
              class="text-body text-muted-foreground"
            >
              {{ t('projects.noDescription') }}
            </p>
          </div>

          <!-- Comments and field activity share one chronological surface. -->
          <div class="mt-8 space-y-3">
            <span class="text-label font-medium text-muted-foreground">
              {{ t('projects.activityTitle') }}
            </span>
            <div
              v-for="entry in timeline"
              :key="entry.key"
              class="text-body"
            >
              <div
                v-if="entry.kind === 'comment'"
                class="rounded-md border border-border bg-card px-3 py-2"
              >
                <p class="whitespace-pre-wrap text-body text-foreground">
                  {{ entry.comment.body }}
                </p>
                <p class="mt-1 text-caption text-muted-foreground">
                  {{ formatTime(entry.at) }}
                </p>
              </div>
              <p
                v-else
                class="text-body text-muted-foreground"
              >
                {{ activityLine(entry.activity) }}
                <span class="text-caption"> · {{ formatTime(entry.at) }}</span>
              </p>
            </div>
          </div>

          <!-- Composer pinned to the bottom of the column, like the reference. -->
          <form
            class="mt-4 flex items-start gap-2"
            @submit.prevent="submitComment"
          >
            <Textarea
              v-model="commentDraft"
              :placeholder="t('projects.commentPlaceholder')"
              class="min-h-9 flex-1"
              rows="2"
            />
            <Button
              type="submit"
              :disabled="!commentDraft.trim() || commenting"
            >
              <Spinner v-if="commenting" />
              {{ t('projects.comment') }}
            </Button>
          </form>
        </div>
      </div>

      <!-- Properties rail. The card carries the only edge here — a border on
           the rail as well would stack two strokes on one visual unit. -->
      <aside class="w-80 shrink-0 overflow-y-auto p-4">
        <div class="rounded-xl border border-border bg-card p-4">
          <h3 class="text-label font-medium text-foreground">
            {{ t('projects.properties') }}
          </h3>

          <dl class="mt-3 space-y-3">
            <div class="flex items-center gap-2">
              <dt class="w-16 shrink-0 text-body text-muted-foreground">
                {{ t('projects.field.status') }}
              </dt>
              <dd class="min-w-0 flex-1">
                <Select
                  :model-value="issue?.status ?? 'todo'"
                  @update:model-value="(v) => updateIssue({ status: String(v) })"
                >
                  <SelectTrigger class="h-8 w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem
                      v-for="status in STATUSES"
                      :key="status"
                      :value="status"
                    >
                      {{ t(`projects.status.${status}`) }}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </dd>
            </div>

            <div class="flex items-center gap-2">
              <dt class="w-16 shrink-0 text-body text-muted-foreground">
                {{ t('projects.field.priority') }}
              </dt>
              <dd class="min-w-0 flex-1">
                <Select
                  :model-value="issue?.priority || 'none'"
                  @update:model-value="(v) => updateIssue({ priority: v === 'none' ? '' : String(v) })"
                >
                  <SelectTrigger class="h-8 w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">
                      {{ t('projects.priority.none') }}
                    </SelectItem>
                    <SelectItem
                      v-for="priority in PRIORITIES"
                      :key="priority"
                      :value="priority"
                    >
                      {{ t(`projects.priority.${priority}`) }}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </dd>
            </div>
          </dl>

          <!-- Read-only metadata: same card, its own group, separated by
               spacing rather than a divider (a full-bleed rule inside a card
               would slice the surface into stacked tiles). -->
          <h3 class="mt-6 text-label font-medium text-foreground">
            {{ t('projects.details') }}
          </h3>
          <dl class="mt-3 space-y-2">
            <div class="flex items-baseline gap-2">
              <dt class="w-16 shrink-0 text-body text-muted-foreground">
                {{ t('projects.createdAt') }}
              </dt>
              <dd class="min-w-0 flex-1 truncate text-body text-foreground">
                {{ detail.node?.created_at ? formatDate(detail.node.created_at) : '—' }}
              </dd>
            </div>
            <div class="flex items-baseline gap-2">
              <dt class="w-16 shrink-0 text-body text-muted-foreground">
                {{ t('projects.updatedAt') }}
              </dt>
              <dd class="min-w-0 flex-1 truncate text-body text-foreground">
                {{ detail.node?.updated_at ? formatDate(detail.node.updated_at) : '—' }}
              </dd>
            </div>
            <div class="flex items-baseline gap-2">
              <dt class="w-16 shrink-0 text-body text-muted-foreground">
                {{ t('projects.revisionLabel') }}
              </dt>
              <dd class="min-w-0 flex-1 truncate text-body text-foreground">
                {{ issue?.revision ?? 1 }}
              </dd>
            </div>
          </dl>
        </div>
      </aside>
    </div>

    <PanePlaceholder v-else>
      {{ t('projects.docGone') }}
    </PanePlaceholder>
  </DockPanelFrame>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Button,
  PanePlaceholder,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Spinner,
  TextButton,
  Textarea,
  toast,
} from '@felinic/ui'
import type { DockviewApi, DockviewPanelApi } from 'dockview-vue'
import {
  getProjectsByProjectIdNodesByNodeId,
  getProjectsByProjectIdNodesByNodeIdActivity,
  getProjectsByProjectIdNodesByNodeIdComments,
  patchProjectsByProjectIdNodesByNodeId,
  patchProjectsByProjectIdNodesByNodeIdIssue,
  postProjectsByProjectIdNodesByNodeIdComments,
  type ProjectActivity,
  type ProjectComment,
  type ProjectNodeDetail,
} from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import MonacoEditor from '@/components/monaco-editor/index.vue'
import DockPanelFrame from './panel-frame.vue'

const MarkdownPreview = defineAsyncComponent(() => import('@/components/markdown-preview/index.vue'))

const props = defineProps<{
  params: {
    params: { projectId: string, nodeId: string, title?: string }
    api: DockviewPanelApi
    containerApi: DockviewApi
  }
}>()

const { t, locale } = useI18n()
const projectId = props.params.params.projectId
const nodeId = props.params.params.nodeId

const STATUSES = ['todo', 'in_progress', 'done', 'cancelled'] as const
const PRIORITIES = ['low', 'medium', 'high', 'urgent'] as const

const detail = ref<ProjectNodeDetail | null>(null)
const comments = ref<ProjectComment[]>([])
const activity = ref<ProjectActivity[]>([])
const loading = ref(false)
const conflict = ref(false)

const issue = computed(() => detail.value?.issue ?? null)
const issueNumber = computed(() => detail.value?.node?.number ?? 0)
const issueHandle = computed(() => (issueNumber.value ? `#${issueNumber.value}` : ''))

const titleDraft = ref(props.params.params.title ?? '')
const bodyDraft = ref('')
const mode = ref<'edit' | 'preview'>('preview')

// The edit toggle is a text affordance, revealed on hover of the description
// block so a reader sees prose, not chrome.
const editToggleClass = 'opacity-0 transition-opacity focus-visible:opacity-100 group-hover/desc:opacity-100' /* ui-allow-style */

let ackTitle = ''
let ackBody = ''
let saveTimer: ReturnType<typeof setTimeout> | null = null
let saving = false
const AUTOSAVE_DEBOUNCE_MS = 2000

function panelTitle(): string {
  const name = ackTitle || t('projects.untitled')
  return issueNumber.value ? `#${issueNumber.value} ${name}` : name
}

async function load() {
  loading.value = true
  try {
    const [detailRes, commentsRes, activityRes] = await Promise.all([
      getProjectsByProjectIdNodesByNodeId({
        path: { project_id: projectId, node_id: nodeId },
        throwOnError: true,
      }),
      getProjectsByProjectIdNodesByNodeIdComments({
        path: { project_id: projectId, node_id: nodeId },
        throwOnError: true,
      }),
      getProjectsByProjectIdNodesByNodeIdActivity({
        path: { project_id: projectId, node_id: nodeId },
        throwOnError: true,
      }),
    ])
    detail.value = detailRes.data ?? null
    comments.value = commentsRes.data ?? []
    activity.value = activityRes.data ?? []
    const node = detail.value?.node
    if (node) {
      ackTitle = node.title ?? ''
      ackBody = node.body ?? ''
      titleDraft.value = ackTitle
      bodyDraft.value = ackBody
      props.params.api.setTitle(panelTitle())
    }
  } catch (error) {
    detail.value = null
    toast.error(resolveApiErrorMessage(error, t('projects.loadFailed')))
  } finally {
    loading.value = false
  }
}

void load()

// ---- content saves (title + description share the content lock) -----------

const contentDirty = computed(() =>
  !!detail.value?.node && (titleDraft.value.trim() !== ackTitle || bodyDraft.value !== ackBody),
)

watch(bodyDraft, (next) => {
  if (!detail.value?.node || next === ackBody) return
  if (saveTimer) clearTimeout(saveTimer)
  saveTimer = setTimeout(() => void saveContent(), AUTOSAVE_DEBOUNCE_MS)
})

function commitTitle() {
  if (!detail.value?.node) return
  if (!titleDraft.value.trim()) {
    titleDraft.value = ackTitle
    return
  }
  if (titleDraft.value.trim() !== ackTitle) void saveContent()
}

async function saveContent(): Promise<void> {
  const node = detail.value?.node
  if (!node || saving || conflict.value || !contentDirty.value) return
  saving = true
  try {
    const { data } = await patchProjectsByProjectIdNodesByNodeId({
      path: { project_id: projectId, node_id: nodeId },
      body: {
        title: titleDraft.value.trim() || ackTitle,
        body: bodyDraft.value,
        expected_version: node.version ?? 1,
      },
      throwOnError: true,
    })
    if (data && detail.value) {
      detail.value = { ...detail.value, node: data }
      ackTitle = data.title ?? ''
      ackBody = data.body ?? ''
      props.params.api.setTitle(panelTitle())
    }
  } catch (error) {
    if (isConflict(error)) {
      conflict.value = true
    } else {
      toast.error(resolveApiErrorMessage(error, t('projects.saveFailed')))
    }
  } finally {
    saving = false
  }
}

async function reloadRemote() {
  conflict.value = false
  await load()
}

// ---- issue field saves (independent revision lock) -------------------------

async function updateIssue(patch: { status?: string, priority?: string }) {
  const current = issue.value
  if (!current) return
  try {
    const { data } = await patchProjectsByProjectIdNodesByNodeIdIssue({
      path: { project_id: projectId, node_id: nodeId },
      body: {
        expected_revision: current.revision ?? 1,
        status: patch.status ?? null,
        priority: patch.priority ?? null,
        assignee_user_id: null,
        assignee_bot_id: null,
        due_at: null,
        rank: null,
      },
      throwOnError: true,
    })
    if (data && detail.value) {
      detail.value = { ...detail.value, issue: data }
    }
    void refreshActivity()
  } catch (error) {
    if (isConflict(error)) {
      toast.error(t('projects.boardConflict'))
      await load()
    } else {
      toast.error(resolveApiErrorMessage(error, t('projects.saveFailed')))
    }
  }
}

async function refreshActivity() {
  try {
    const { data } = await getProjectsByProjectIdNodesByNodeIdActivity({
      path: { project_id: projectId, node_id: nodeId },
      throwOnError: true,
    })
    activity.value = data ?? []
  } catch {
    // Quiet refresh; the stream catches up on the next full load.
  }
}

// ---- comments + activity timeline ------------------------------------------

type TimelineEntry
  = | { kind: 'comment', key: string, at: string, comment: ProjectComment }
    | { kind: 'activity', key: string, at: string, activity: ProjectActivity }

const timeline = computed<TimelineEntry[]>(() => {
  const entries: TimelineEntry[] = [
    ...comments.value.map(comment => ({
      kind: 'comment' as const,
      key: `c:${comment.id}`,
      at: comment.created_at ?? '',
      comment,
    })),
    ...activity.value.map(item => ({
      kind: 'activity' as const,
      key: `a:${item.id}`,
      at: item.created_at ?? '',
      activity: item,
    })),
  ]
  return entries.sort((a, b) => a.at.localeCompare(b.at))
})

function activityLine(item: ProjectActivity): string {
  const field = item.field ?? ''
  const from = humanValue(field, item.old_value ?? '')
  const to = humanValue(field, item.new_value ?? '')
  return t('projects.activityChanged', { field: t(`projects.field.${field}`), from, to })
}

function humanValue(field: string, value: string): string {
  if (!value) return t('projects.valueNone')
  if (field === 'status') return t(`projects.status.${value}`)
  if (field === 'priority') return t(`projects.priority.${value}`)
  return value
}

function formatTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

function formatDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium' }).format(date)
}

const commentDraft = ref('')
const commenting = ref(false)

async function submitComment() {
  const body = commentDraft.value.trim()
  if (!body || commenting.value) return
  commenting.value = true
  try {
    const { data } = await postProjectsByProjectIdNodesByNodeIdComments({
      path: { project_id: projectId, node_id: nodeId },
      body: { body },
      throwOnError: true,
    })
    if (data) comments.value = [...comments.value, data]
    commentDraft.value = ''
  } catch (error) {
    toast.error(resolveApiErrorMessage(error, t('projects.saveFailed')))
  } finally {
    commenting.value = false
  }
}

function isConflict(error: unknown): boolean {
  const status = (error as { status?: number, response?: { status?: number } })
  return status?.status === 409 || status?.response?.status === 409
}

onBeforeUnmount(() => {
  if (saveTimer) clearTimeout(saveTimer)
  void saveContent()
})
</script>
