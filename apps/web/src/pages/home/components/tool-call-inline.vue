<template>
  <!-- A successful apply_patch splits into one edit-style row per file: the
       call is one tool step, but each file reads exactly like an edit (title,
       counts, flush diff card) instead of a second, bespoke layout. -->
  <div
    v-if="patchFiles.length"
    class="font-[400] space-y-0.5"
    :class="inGroup ? '' : 'text-[0.90625rem]'"
  >
    <div
      v-for="(file, index) in patchFiles"
      :key="index"
    >
      <!-- A deleted file has no diff (the server never reads it), so its row
           does not expand; the struck-through name says what happened. -->
      <ToolRow
        :expandable="Boolean(file.diff)"
        :open="patchFileOpen(index)"
        :action="renderedActionLabel"
        :target="patchFileTarget(file)"
        :target-class="file.operation === 'delete' ? 'line-through' : ''"
        :target-title="patchFileTitle(file)"
        :openable="Boolean(file.diff && openInFileManager)"
        :execution-location="executionLocationLabel"
        :add="file.add"
        :remove="file.remove"
        @toggle="togglePatchFile(index)"
        @open-target="openInFileManager?.(file.movedTo || file.path, false)"
      />
      <CollapseSection
        v-if="file.diff"
        :open="patchFileOpen(index)"
      >
        <!-- Same flush card as edit (see below). -->
        <div class="mt-1.5 rounded-sm bg-card font-[400] overflow-hidden">
          <DiffPanel
            :diff="file.diff"
            :filename="extractFilename(file.movedTo || file.path)"
          />
        </div>
      </CollapseSection>
    </div>
  </div>

  <div
    v-else
    class="font-[400]"
    :class="inGroup ? '' : 'text-[0.90625rem]'"
  >
    <ToolRow
      :expandable="expandable"
      :open="open"
      :action="showActionLabel ? renderedActionLabel : ''"
      :action-class="actionClass"
      :target="display.target"
      :target-class="targetClass"
      :target-title="display.fullTarget"
      :openable="canOpenInFiles"
      :execution-location="executionLocationLabel"
      :add="display.diffAdd"
      :remove="display.diffRemove"
      @toggle="toggleOpen"
      @open-target="handleOpenInFiles"
    >
      <template #leading>
        <ConnectorLogo
          v-if="connector"
          :connector="connector"
        />
      </template>
      <span
        v-if="approvalLabel"
        class="shrink-0 text-xs"
        :class="block.approval?.status === 'pending' ? 'text-warning-foreground' : 'text-muted-foreground'"
      >{{ approvalLabel }}</span>
      <span
        v-if="userInputLabel"
        class="shrink-0 text-xs text-muted-foreground"
      >{{ userInputLabel }}</span>
      <template #trailing>
        <span
          v-if="elapsedLabel"
          class="shrink-0 text-xs text-muted-foreground"
        >{{ elapsedLabel }}</span>
      </template>
    </ToolRow>

    <CollapseSection
      v-if="expandable"
      :open="open && !isPending"
    >
      <!-- inGroup: a card nested inside the group's own muted capsule needs a
           visibly different fill (bg-card, not bg-muted) so it reads as one
           layer up — a genuinely different surface, not a padding drift of
           the capsule shape below, so it stays hand-written.
           edit (always) and write (when it carries a server diff) use that
           card and drop the padding so their diff rows meet the card edges:
           one rounded surface, same radius and borderless fill as every
           other detail surface, no colored block nested inside a second
           card. -->
      <div
        v-if="inGroup || flushDiffCard"
        class="mt-1.5 rounded-sm bg-card font-[400]"
        :class="flushDiffCard ? 'overflow-hidden' : 'px-2.5 py-2'"
      >
        <component
          :is="detailComponent"
          v-if="detailComponent"
          :block="block"
        />
        <ToolCallDetailGeneric
          v-else
          :block="block"
        />
      </div>
      <Capsule
        v-else
        density="detail"
        class="mt-1.5 font-[400]"
      >
        <component
          :is="detailComponent"
          v-if="detailComponent"
          :block="block"
        />
        <ToolCallDetailGeneric
          v-else
          :block="block"
        />
      </Capsule>
    </CollapseSection>
  </div>
</template>

<script setup lang="ts">
import { computed, inject, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ToolCallBlock } from '@/store/chat-list'
import { openInFileManagerKey } from '../composables/useFileManagerProvider'
import { useConnectorLogos } from '../composables/useConnectorLogos'
import {
  getToolTitle,
  isDirPathTool,
  isFilePathTool,
  patchFileDiffs,
  type PatchFileDiff,
} from './tool-call-registry'
import { extractFilename } from '@/composables/useShikiHighlighter'
import DiffPanel from './tool-call-diff-panel.vue'
import ConnectorLogo from './tool-detail/connector-logo.vue'
import ToolRow from './tool-detail/tool-row.vue'
import ToolCallDetailGeneric from './tool-call-detail-generic.vue'
import { hasToolResultError } from './tool-result-error'
import ToolCallDetailWrite from './tool-call-detail-write.vue'
import CollapseSection from './collapse-section.vue'
import { getCollapseOpen, setCollapseOpen, toolCollapseKey } from './process-collapse'
import Capsule from './tool-detail/capsule.vue'

const props = defineProps<{ block: ToolCallBlock, messageId: string, inGroup?: boolean, showExecutionLocation?: boolean }>()
const { t } = useI18n()
const elapsedLabel = computed(() => {
  const seconds = props.block.elapsed_time_seconds
  if (seconds === undefined || !props.block.running || props.block.approval?.status === 'pending' || props.block.userInput?.status === 'pending') return ''
  return t('chat.tools.elapsedRunning', { seconds: Math.floor(seconds) })
})

const openInFileManager = inject(openInFileManagerKey, undefined)

const title = computed(() => getToolTitle(props.block, t))
const display = computed(() => title.value.display)
// Specialized panels describe successful results or attempted inputs. Failed
// results use the shared diagnostic detail, without changing the neutral title.
const resultFailed = computed(() => hasToolResultError(props.block))
const detailComponent = computed(() => resultFailed.value ? ToolCallDetailGeneric : display.value.detail)

// A Connect-It tool carries its binding's alias in the tool name; when that
// alias resolves to one of the bot's connectors the row leads with its logo.
const connectorLookup = useConnectorLogos()
const connector = computed(() => connectorLookup.value(props.block.toolName))
const executionLocationLabel = computed(() => {
  if (!props.showExecutionLocation) return ''
  const location = props.block.execution_location
  if (!location) return ''
  if (location.kind === 'native') return t('bots.remoteRuntime.nativeWorkspace')
  return location.name?.trim() || ''
})

// Persisted, user-driven toggle (survives the post-turn refetch/remount).
const collapseKey = computed(() => toolCollapseKey(props.messageId, props.block))
const open = ref(getCollapseOpen(collapseKey.value) ?? (display.value.defaultOpen === true))
watch(collapseKey, (key) => {
  open.value = getCollapseOpen(key) ?? (display.value.defaultOpen === true)
})

const expandable = computed(() => {
  if (isPending.value) return false
  if (resultFailed.value) return true
  if (display.value.detail === ToolCallDetailWrite) {
    const input = props.block.input as Record<string, unknown> | undefined
    // block.diff covers write(path, "") clearing a file: empty content must
    // not make the server-rendered all-delete diff unreachable.
    return (typeof input?.content === 'string' && input.content.length > 0)
      || input?.content_truncated === true
      || Boolean(props.block.diff)
  }
  return Boolean(display.value.detail) || display.value.expandable === true
})

const isPending = computed(() => title.value.pending)
const showPendingLabel = computed(() => title.value.pending)
const showActionLabel = computed(() => title.value.showAction)
const renderedActionLabel = computed(() => title.value.action)

// edit always renders its diff flush with the card edges; write joins it
// only when the server attached a diff (older write records keep the padded
// content block).
const flushDiffCard = computed(() => {
  if (props.block.toolName === 'edit') return true
  return props.block.toolName === 'write' && Boolean(props.block.diff)
})

// Per-file rows of a successful apply_patch (empty → the single row below).
// Each file keeps its own persisted open state under the call's collapse key.
const patchFiles = computed<PatchFileDiff[]>(() => {
  if (isPending.value || resultFailed.value) return []
  return patchFileDiffs(props.block)
})
const patchFileOpenState = ref<Record<number, boolean>>({})
watch(collapseKey, () => {
  patchFileOpenState.value = {}
})

function patchFileCollapseKey(index: number): string {
  return collapseKey.value ? `${collapseKey.value}#${index}` : ''
}

function patchFileOpen(index: number): boolean {
  return patchFileOpenState.value[index]
    ?? getCollapseOpen(patchFileCollapseKey(index))
    ?? (display.value.defaultOpen === true)
}

function togglePatchFile(index: number) {
  const next = !patchFileOpen(index)
  patchFileOpenState.value = { ...patchFileOpenState.value, [index]: next }
  setCollapseOpen(patchFileCollapseKey(index), next)
}

function patchFileTarget(file: PatchFileDiff): string {
  const from = extractFilename(file.path)
  const to = extractFilename(file.movedTo)
  return !file.movedTo || from === to ? to || from : `${from} → ${to}`
}

function patchFileTitle(file: PatchFileDiff): string {
  return file.movedTo ? `${file.path} → ${file.movedTo}` : file.path
}

// Brief tools (e.g. send/memory) finish in <100ms. Showing the running
// shimmer for them flickers, so we only display it after a short delay.
const showRunning = ref(false)
let runningTimer: ReturnType<typeof setTimeout> | null = null
const RUNNING_SHIMMER_DELAY_MS = 250

function clearRunningTimer() {
  if (runningTimer !== null) {
    clearTimeout(runningTimer)
    runningTimer = null
  }
}

watch(
  () => props.block.done,
  (done) => {
    clearRunningTimer()
    if (done) {
      showRunning.value = false
      return
    }
    runningTimer = setTimeout(() => {
      showRunning.value = true
      runningTimer = null
    }, RUNNING_SHIMMER_DELAY_MS)
  },
  { immediate: true },
)

onBeforeUnmount(clearRunningTimer)

const targetClass = computed(() => {
  if (showRunning.value) return 'tool-shimmer-text'
  return '' // inherit the row's gray→black hover color
})

const actionClass = computed(() => {
  if (showPendingLabel.value) return 'tool-shimmer-text'
  if (showRunning.value && !display.value.target) return 'tool-shimmer-text'
  return ''
})

// Pending approvals are answered from the composer-dock panel (see
// composer-panel.vue), never here — this row keeps only the read-only status
// label so history still shows which call needed one and how it ended. The
// old inline Allow/Reject also carried raw color classes that bypassed the
// Button variants, so nothing of it is worth keeping. An approved call is
// the common case and reads as noise next to the title, so its label is
// hidden; only pending/declined/canceled stay visible.
const approvalLabel = computed(() => {
  const approval = props.block.approval
  if (!approval?.approval_id || approval.status === 'approved') return ''
  if (approval.status === 'pending') return t('chat.tools.pendingApproval', 'Awaiting approval')
  if (approval.status === 'rejected') return t('chat.tools.approvalDeclined', 'Declined')
  // The backend emits "cancelled" (approval.StatusCancelled); accept the
  // American spelling too so external runtimes don't fall through raw.
  if (approval.status === 'cancelled' || approval.status === 'canceled') return t('chat.tools.approvalCanceled', 'Canceled')
  if (approval.status === 'expired') return t('chat.tools.approvalExpired', 'Expired')
  return approval.status
})

const userInputLabel = computed(() => {
  const userInput = props.block.userInput
  if (!userInput?.user_input_id) return ''
  if (userInput.status === 'pending') return ''
  return userInputStatusLabel(userInput.status)
})

function userInputStatusLabel(status: string) {
  const normalized = status.trim().toLowerCase()
  switch (normalized) {
    case 'submitted':
      return t('chat.tools.userInputSubmitted', 'answered')
    case 'canceled':
      return t('chat.tools.userInputCanceled', 'canceled')
    case 'failed':
      return t('chat.tools.userInputFailed', 'failed')
    case 'expired':
      return t('chat.tools.userInputExpired', 'expired')
    default:
      return status
  }
}

const filePath = computed(() => {
  if (!isFilePathTool(props.block.toolName)) return ''
  const input = props.block.input as Record<string, unknown> | undefined
  return (input?.path as string) ?? ''
})

const canOpenInFiles = computed(
  () => Boolean(filePath.value) && Boolean(openInFileManager),
)

function toggleOpen() {
  open.value = !open.value
  setCollapseOpen(collapseKey.value, open.value)
}

function handleOpenInFiles() {
  if (!filePath.value || !openInFileManager) return
  openInFileManager(filePath.value, isDirPathTool(props.block.toolName))
}
</script>
