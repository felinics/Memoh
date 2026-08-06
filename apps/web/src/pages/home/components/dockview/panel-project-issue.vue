<template>
  <DockPanelFrame>
    <PanePlaceholder
      v-if="loading && !detail"
      loading
    >
      {{ t('common.loading') }}
    </PanePlaceholder>

    <div
      v-else-if="detail"
      class="h-full w-full overflow-y-auto [scrollbar-gutter:stable]"
    >
      <section class="mx-auto max-w-3xl px-6 pb-12 pt-8">
        <input
          v-model="titleDraft"
          type="text"
          class="w-full bg-transparent text-heading font-semibold text-foreground outline-none placeholder:text-muted-foreground"
          :placeholder="t('projects.untitled')"
          :aria-label="t('projects.issueTitle')"
          @blur="commitTitle"
          @keydown.enter.prevent="commitTitle"
        >

        <!-- Field row: every control commits on change (auto-save, silent). -->
        <div class="mt-4 flex flex-wrap items-center gap-2">
          <Select
            :model-value="issue?.status ?? 'todo'"
            @update:model-value="(v) => updateIssue({ status: String(v) })"
          >
            <SelectTrigger class="w-36">
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

          <Select
            :model-value="issue?.priority || 'none'"
            @update:model-value="(v) => updateIssue({ priority: v === 'none' ? '' : String(v) })"
          >
            <SelectTrigger class="w-36">
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

          <span
            v-if="issue"
            class="text-caption text-muted-foreground"
          >
            {{ t('projects.revision', { n: issue.revision }) }}
          </span>
        </div>

        <!-- Description: markdown, same edit/preview split as the doc panel. -->
        <div class="mt-6 space-y-2.5">
          <div class="flex items-center gap-2">
            <h2 class="text-label font-medium text-muted-foreground">
              {{ t('projects.description') }}
            </h2>
            <div class="flex-1" />
            <SegmentedControl
              v-model="mode"
              :items="modeItems"
              :aria-label="t('projects.viewMode')"
            />
          </div>
          <div
            v-if="conflict"
            class="flex items-center gap-2 rounded-md border border-destructive-border bg-destructive-soft px-3 py-2 text-body"
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
            class="rounded-md"
          />
          <p
            v-else
            class="text-body text-muted-foreground"
          >
            {{ t('projects.noDescription') }}
          </p>
        </div>

        <!-- Comments + field activity, one chronological surface. -->
        <div class="mt-8 space-y-2.5">
          <h2 class="text-label font-medium text-muted-foreground">
            {{ t('projects.activityTitle') }}
          </h2>
          <div class="space-y-3">
            <div
              v-for="entry in timeline"
              :key="entry.key"
              class="flex items-start gap-2 text-body"
            >
              <template v-if="entry.kind === 'comment'">
                <div class="min-w-0 flex-1 rounded-md border border-border bg-card px-3 py-2">
                  <p class="whitespace-pre-wrap text-body text-foreground">
                    {{ entry.comment.body }}
                  </p>
                  <p class="mt-1 text-caption text-muted-foreground">
                    {{ formatTime(entry.at) }}
                  </p>
                </div>
              </template>
              <template v-else>
                <p class="min-w-0 flex-1 text-body text-muted-foreground">
                  {{ activityLine(entry.activity) }}
                  <span class="text-caption"> · {{ formatTime(entry.at) }}</span>
                </p>
              </template>
            </div>
          </div>

          <form
            class="flex items-start gap-2 pt-1"
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
      </section>
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
  SegmentedControl,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Spinner,
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

const titleDraft = ref(props.params.params.title ?? '')
const bodyDraft = ref('')
const mode = ref<'edit' | 'preview'>('preview')
const modeItems = computed(() => [
  { label: t('projects.preview'), value: 'preview' as const },
  { label: t('projects.edit'), value: 'edit' as const },
])

let ackTitle = ''
let ackBody = ''
let saveTimer: ReturnType<typeof setTimeout> | null = null
let saving = false
const AUTOSAVE_DEBOUNCE_MS = 2000

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
      props.params.api.setTitle(ackTitle || t('projects.untitled'))
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
      props.params.api.setTitle(ackTitle || t('projects.untitled'))
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
    // Field changes append to the activity stream — refresh it quietly.
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
