<template>
  <DockPanelFrame editor-surface>
    <template #header>
      <div class="flex h-9 shrink-0 items-center gap-2 border-b border-border px-3">
        <input
          v-model="titleDraft"
          type="text"
          class="min-w-0 flex-1 bg-transparent text-label font-medium text-foreground outline-none placeholder:text-muted-foreground"
          :placeholder="t('projects.untitled')"
          :aria-label="t('projects.docTitle')"
          @blur="commitTitle"
          @keydown.enter.prevent="commitTitle"
        >
        <span
          v-if="node"
          class="shrink-0 text-caption text-muted-foreground"
        >v{{ node.version }}</span>
        <SegmentedControl
          v-model="mode"
          :items="modeItems"
          :aria-label="t('projects.viewMode')"
        />
      </div>
    </template>

    <PanePlaceholder
      v-if="loading && !node"
      loading
    >
      {{ t('common.loading') }}
    </PanePlaceholder>

    <!-- Conflict banner: the remote moved past our expected version. Keep the
         local draft in the editor (never silently drop user text); reloading
         is the user's explicit choice. -->
    <div
      v-else-if="node"
      class="flex h-full min-h-0 flex-col"
    >
      <div
        v-if="conflict"
        class="flex shrink-0 items-center gap-2 border-b border-destructive-border bg-destructive-soft px-3 py-2 text-body"
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
      <div class="min-h-0 flex-1">
        <MonacoEditor
          v-if="mode === 'edit'"
          v-model="bodyDraft"
          language="markdown"
        />
        <MarkdownPreview
          v-else
          :content="bodyDraft"
          class="h-full"
        />
      </div>
    </div>

    <PanePlaceholder v-else>
      {{ t('projects.docGone') }}
    </PanePlaceholder>
  </DockPanelFrame>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, PanePlaceholder, SegmentedControl, toast } from '@felinic/ui'
import type { DockviewApi, DockviewPanelApi } from 'dockview-vue'
import {
  getProjectsByProjectIdNodesByNodeId,
  patchProjectsByProjectIdNodesByNodeId,
  type ProjectNode,
} from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useProjectsStore } from '@/store/projects'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
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

const { t } = useI18n()
const projectsStore = useProjectsStore()
const workspaceTabs = useWorkspaceTabsStore()

const projectId = props.params.params.projectId
const nodeId = props.params.params.nodeId
const panelId = `project-doc:${projectId}:${nodeId}`

const node = ref<ProjectNode | null>(null)
const loading = ref(false)
const conflict = ref(false)
const titleDraft = ref(props.params.params.title ?? '')
const bodyDraft = ref('')
const mode = ref<'edit' | 'preview'>('preview')

const modeItems = computed(() => [
  { label: t('projects.preview'), value: 'preview' as const },
  { label: t('projects.edit'), value: 'edit' as const },
])

// Content the server last acknowledged; the dirty check and the save payload
// diff against this, not against whatever the panel initially loaded.
let ackTitle = ''
let ackBody = ''
let saveTimer: ReturnType<typeof setTimeout> | null = null
let saving = false

const AUTOSAVE_DEBOUNCE_MS = 2000

async function load() {
  loading.value = true
  try {
    const { data } = await getProjectsByProjectIdNodesByNodeId({
      path: { project_id: projectId, node_id: nodeId },
      throwOnError: true,
    })
    const fresh = data?.node ?? null
    node.value = fresh
    if (fresh) {
      ackTitle = fresh.title ?? ''
      ackBody = fresh.body ?? ''
      titleDraft.value = ackTitle
      bodyDraft.value = ackBody
      props.params.api.setTitle(ackTitle || t('projects.untitled'))
    }
  } catch (error) {
    node.value = null
    toast.error(resolveApiErrorMessage(error, t('projects.loadFailed')))
  } finally {
    loading.value = false
  }
}

const dirty = computed(() =>
  !!node.value && (titleDraft.value.trim() !== ackTitle || bodyDraft.value !== ackBody),
)

function scheduleSave() {
  if (saveTimer) clearTimeout(saveTimer)
  saveTimer = setTimeout(() => void save(), AUTOSAVE_DEBOUNCE_MS)
}

watch(bodyDraft, (next) => {
  if (!node.value || next === ackBody) return
  // First edit pins the tab out of the ephemeral preview slot (same rule as
  // file tabs).
  workspaceTabs.pinPanel(panelId)
  scheduleSave()
})

function commitTitle() {
  if (!node.value) return
  if (!titleDraft.value.trim()) {
    titleDraft.value = ackTitle
    return
  }
  if (titleDraft.value.trim() !== ackTitle) {
    workspaceTabs.pinPanel(panelId)
    void save()
  }
}

async function save(): Promise<boolean> {
  const current = node.value
  if (!current || saving || conflict.value) return !dirty.value
  if (!dirty.value) return true
  saving = true
  const title = titleDraft.value.trim() || ackTitle
  const body = bodyDraft.value
  try {
    const { data } = await patchProjectsByProjectIdNodesByNodeId({
      path: { project_id: projectId, node_id: nodeId },
      body: {
        title,
        body,
        expected_version: current.version ?? 1,
      },
      throwOnError: true,
    })
    if (data) {
      node.value = data
      ackTitle = data.title ?? ''
      ackBody = data.body ?? ''
      props.params.api.setTitle(ackTitle || t('projects.untitled'))
    }
    // Keep the nav tree's titles/versions in step with what was just saved.
    void projectsStore.loadTree(projectId, true)
    return true
  } catch (error) {
    if (isConflict(error)) {
      conflict.value = true
    } else {
      toast.error(resolveApiErrorMessage(error, t('projects.saveFailed')))
    }
    return false
  } finally {
    saving = false
  }
}

function isConflict(error: unknown): boolean {
  const status = (error as { status?: number, response?: { status?: number } })
  return status?.status === 409 || status?.response?.status === 409
}

// Reload drops the local draft in favor of the remote head — the user's
// explicit choice from the conflict banner.
async function reloadRemote() {
  conflict.value = false
  await load()
}

void load()

onBeforeUnmount(() => {
  if (saveTimer) clearTimeout(saveTimer)
  // Last-chance flush; conflicts here are silently dropped (the panel is
  // going away — version history still holds every acknowledged save).
  void save()
})
</script>
