<script setup lang="ts">
import { computed, nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, useId, watch } from 'vue'
import { appKeyboardCommands } from '@/lib/keyboard-commands'
import { useKeyboardCommand } from '@/composables/useKeyboardCommand'
import { isFileSaveEligible } from './file-save-command'
import {
  canApplyExternalReload,
  detectSaveConflict,
  deriveChipContext,
  fileMetadataFingerprint,
  resolveChipButtons,
  resolvePolledTextChange,
  resolveSaveBehavior,
  type ChipButton,
} from './file-conflict'
import { useI18n } from 'vue-i18n'
import { DiffTitleBar, PanePlaceholder, toast } from '@felinic/ui'
import { File, FileX, Download, RefreshCw, GitCompare, X } from 'lucide-vue-next'
import { Button } from '@felinic/ui'
import {
  getBotsByBotIdContainerFs,
  getBotsByBotIdContainerFsRead,
  postBotsByBotIdContainerFsWrite,
  getBotsByBotIdContainerFsDownload,
} from '@memohai/sdk'
import type { HandlersFsFileInfo } from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import MonacoEditor from '@/components/monaco-editor/index.vue'
import MonacoDiff from '@/components/monaco-editor/diff.vue'
import { sdkApiUrl, sdkAuthQuery } from '@/lib/api-client'
import { formatRelativeTime } from '@/utils/date-time'
import { isTextFile, isImageFile } from './utils'
import { useChatStore } from '@/store/chat-list'
import { storeToRefs } from 'pinia'

const props = defineProps<{
  botId: string
  file: HandlersFsFileInfo
  readonly?: boolean
}>()

const EXTERNAL_FILE_POLL_MS = 2_000

const emit = defineEmits<{
  saved: []
  'update:dirty': [dirty: boolean]
}>()

const { t } = useI18n()

const content = ref('')
const originalContent = ref('')
const baseRevision = ref('')
const loading = ref(false)
const saving = ref(false)
const imageUrl = ref('')
// True once the file has been read at least once for the current path. Used
// to suppress the full-area spinner on subsequent reloads — the editor / image
// stays mounted and the content swaps in place, the way VS Code handles
// external file changes.
const loaded = ref(false)
// Timestamp (ms) of the last successful read. Feeds the save baseline guard —
// any fs bump newer than this for our path means the user would overwrite a
// fresher disk version. Resets on path change.
const lastLoadedAt = ref(0)
// Conflict state machine. 'chip' = inline banner above the editor with the
// usual choice (Compare / Reload / Save anyway). 'compare' = diff editor
// replaces the main pane. 'none' = clean.
const conflictState = ref<'none' | 'chip' | 'compare'>('none')
const diskState = ref<'available' | 'stale' | 'deleted'>('available')
// Right side of the diff editor. Snapshot at the moment Compare opens; the
// agent might write again while Compare is up, in which case we surface a
// "Refresh diff" affordance in the compare toolbar rather than silently
// updating the right pane underneath the user's review.
const compareDiskContent = ref('')
// Flips to true when a fsChangedAt for this path lands while we're in
// conflictState='compare'. Reset on every successful openCompare(), on path
// change, and on exit.
const compareStale = ref(false)
let compareController: AbortController | null = null
const observedExternalRevision = ref('')
const imageMetadataFingerprint = ref('')
let externalPollTimer: ReturnType<typeof setInterval> | null = null
let externalPollController: AbortController | null = null
let externalPollingActive = false
// Stable ids so the chip buttons can aria-describedby the relevant inline
// message span, giving keyboard / screen-reader users the "why is this here"
// context. The compare-toolbar Refresh button uses the staleNotice id by the
// same pattern.
const chipMessageId = useId()
const compareStaleId = useId()
// Reference to MonacoEditor so we can hand focus back when an action removes
// the chip / closes Compare and the activated button itself was just torn
// down. Without this, focus would fall through to document.body.
const monacoEditorRef = ref<{ $el?: HTMLElement } | null>(null)
// Drives "5s ago" / "2m ago" relative-time labels in the chip. Ticks once a
// minute; agent writes are usually fresh enough that "just now" wins, but a
// chip left up across coffee should age gracefully.
const nowTick = ref(Date.now())
let nowTickInterval: ReturnType<typeof setInterval> | null = null

const filename = computed(() => props.file.name ?? '')
const filePath = computed(() => props.file.path ?? '')
const isText = computed(() => isTextFile(filename.value))
const isImage = computed(() => isImageFile(filename.value))
const isDirty = computed(() => content.value !== originalContent.value)

const chatStore = useChatStore()
const { fsChangedAt, currentBotId, bots } = storeToRefs(chatStore)
const botName = computed(() => {
  const bot = bots.value.find(b => b.id === props.botId)
  return bot?.display_name || bot?.name || null
})
const fsEvent = computed(() => chatStore.fsEventForPath(filePath.value))
const chipContext = computed(() => deriveChipContext(fsEvent.value, botName.value))
const fallbackAgent = computed(() => t('bots.files.externalChange.fallbackAgent'))
const chipButtons = computed<ChipButton[]>(() => resolveChipButtons({
  diskState: diskState.value,
  isText: isText.value,
  isDirty: isDirty.value,
}))

function focusEditor() {
  const host = monacoEditorRef.value?.$el
  if (!host) return
  // Monaco renders a hidden <textarea.inputarea> inside its host that
  // receives text input — focusing it routes keystrokes to the editor and
  // restores the visible caret.
  const textarea = host.querySelector?.<HTMLTextAreaElement>('textarea.inputarea')
  if (textarea) {
    textarea.focus()
    return
  }
  if (typeof (host as HTMLElement).focus === 'function') (host as HTMLElement).focus()
}

async function onChipButton(btn: ChipButton) {
  if (btn.kind === 'compare') await openCompare()
  else if (btn.kind === 'reload') await acceptExternalChange()
  else if (btn.kind === 'forceSave') await handleSave(true)
  // Hand focus back to the editor when the chip just got unmounted (its
  // wrapper v-if'd out); without this the browser drops focus to <body>.
  await nextTick()
  if (conflictState.value === 'none') focusEditor()
}

const reloadLabelKey = (key: 'reload' | 'tryAgain') => `bots.files.externalChange.${key}`
const saveLabelKey = (key: 'saveAnyway' | 'saveToRestore') => `bots.files.externalChange.${key}`

const chipMessage = computed(() => {
  if (diskState.value === 'deleted') return t('bots.files.externalChange.deleted')
  if (diskState.value === 'stale') return t('bots.files.externalChange.reloadFailed')
  const ctx = chipContext.value
  const agent = ctx.agentName ?? fallbackAgent.value
  // formatRelativeTime depends on Date.now(); reading the tick here makes the
  // label re-evaluate as it ages.
  void nowTick.value
  const time = ctx.occurredAt ? formatRelativeTime(new Date(ctx.occurredAt)) : ''
  if (ctx.kind === 'write' && ctx.newLineCount != null) {
    return t('bots.files.externalChange.wrote', { agent, lines: ctx.newLineCount, time })
  }
  if (ctx.kind === 'edit' && (ctx.addedLines != null || ctx.removedLines != null)) {
    return t('bots.files.externalChange.edited', {
      agent,
      added: ctx.addedLines ?? 0,
      removed: ctx.removedLines ?? 0,
      time,
    })
  }
  if (ctx.kind === 'exec') return t('bots.files.externalChange.ran', { agent, time })
  if (ctx.kind === 'apply_patch') return t('bots.files.externalChange.patched', { agent, time })
  return t('bots.files.externalChange.generic', { agent, time })
})

watch(isDirty, (dirty) => {
  emit('update:dirty', dirty)
}, { immediate: true })

// One in-flight read at a time per viewer; a new load aborts the old one so a
// slower stale response can't overwrite newer content when fsChangedAt fires
// in bursts.
let activeReadController: AbortController | null = null

function isHttpStatus(error: unknown, status: number): boolean {
  const maybe = error as { status?: unknown; response?: { status?: unknown } } | null | undefined
  return maybe?.status === status || maybe?.response?.status === status
}

function isDocumentVisible(): boolean {
  return typeof document === 'undefined' || document.visibilityState === 'visible'
}

// Force the chip back into view on a terminal disk-state transition (delete /
// stale read). Tears down any active Compare so the user isn't reviewing a diff
// against bytes that no longer exist.
function dropToChipForBadDiskState() {
  compareController?.abort()
  compareController = null
  compareDiskContent.value = ''
  compareStale.value = false
  conflictState.value = 'chip'
}

function notePolledExternalRevision(revision: string) {
  if (observedExternalRevision.value === revision) return
  observedExternalRevision.value = revision
  chatStore.markFsChanged(filePath.value)
}

function applyPolledTextContent(contentValue: string, revision: string) {
  content.value = contentValue
  originalContent.value = contentValue
  baseRevision.value = revision
  observedExternalRevision.value = ''
  lastLoadedAt.value = Date.now()
  loaded.value = true
  diskState.value = 'available'
  conflictState.value = 'none'
}

async function pollTextFile(signal: AbortSignal) {
  const { data } = await getBotsByBotIdContainerFsRead({
    path: { bot_id: props.botId },
    query: { path: filePath.value },
    signal,
    throwOnError: true,
  })
  if (signal.aborted) return
  const nextRevision = data.revision ?? ''
  const action = resolvePolledTextChange({
    loaded: loaded.value,
    loading: loading.value,
    saving: saving.value,
    currentRevision: baseRevision.value,
    nextRevision,
    isDirty: isDirty.value,
    conflictState: conflictState.value,
  })
  if (action === 'ignore') return
  if (action === 'apply') {
    applyPolledTextContent(data.content ?? '', nextRevision)
    return
  }
  notePolledExternalRevision(nextRevision)
  diskState.value = 'available'
  if (action === 'mark-compare-stale') {
    compareStale.value = true
    return
  }
  if (conflictState.value === 'none') conflictState.value = 'chip'
}

async function pollImageFile(signal: AbortSignal) {
  const { data } = await getBotsByBotIdContainerFs({
    path: { bot_id: props.botId },
    query: { path: filePath.value },
    signal,
    throwOnError: true,
  })
  if (signal.aborted) return
  const nextFingerprint = fileMetadataFingerprint(data)
  if (!imageMetadataFingerprint.value) {
    imageMetadataFingerprint.value = nextFingerprint
    return
  }
  if (nextFingerprint === imageMetadataFingerprint.value) return
  const previousFingerprint = imageMetadataFingerprint.value
  await loadImageBlob({ notifyOnError: false })
  imageMetadataFingerprint.value = diskState.value === 'available'
    ? nextFingerprint
    : previousFingerprint
}

async function pollExternalFile() {
  if (!externalPollingActive) return
  if (!props.botId || !loaded.value || loading.value || saving.value || externalPollController) return
  if (!isDocumentVisible()) return
  if (!isText.value && !isImage.value) return

  const controller = new AbortController()
  externalPollController = controller
  try {
    if (isText.value) await pollTextFile(controller.signal)
    else if (isImage.value) await pollImageFile(controller.signal)
  } catch (error) {
    if (controller.signal.aborted) return
    if (isHttpStatus(error, 404)) {
      diskState.value = 'deleted'
      loaded.value = true
      observedExternalRevision.value = 'deleted'
      imageMetadataFingerprint.value = ''
      dropToChipForBadDiskState()
      return
    }
    diskState.value = 'stale'
    loaded.value = true
    dropToChipForBadDiskState()
  } finally {
    if (externalPollController === controller) externalPollController = null
  }
}

function startExternalFilePoll() {
  if (!externalPollingActive || externalPollTimer !== null) return
  externalPollTimer = window.setInterval(() => {
    void pollExternalFile()
  }, EXTERNAL_FILE_POLL_MS)
}

function stopExternalFilePoll() {
  if (externalPollTimer !== null) {
    window.clearInterval(externalPollTimer)
    externalPollTimer = null
  }
  externalPollController?.abort()
  externalPollController = null
}

function handleVisibilityChange() {
  if (!externalPollingActive) return
  if (isDocumentVisible()) void pollExternalFile()
}

async function loadTextContent(options: { forceApply?: boolean; notifyOnError?: boolean } = {}) {
  activeReadController?.abort()
  const controller = new AbortController()
  activeReadController = controller
  const contentAtRequestStart = content.value
  const originalContentAtRequestStart = originalContent.value
  loading.value = true
  try {
    const { data } = await getBotsByBotIdContainerFsRead({
      path: { bot_id: props.botId },
      query: { path: filePath.value },
      signal: controller.signal,
      throwOnError: true,
    })
    if (controller.signal.aborted) return
    if (!canApplyExternalReload({
      contentAtRequestStart,
      originalContentAtRequestStart,
      currentContent: content.value,
      currentOriginalContent: originalContent.value,
      force: options.forceApply,
    })) {
      // The GET succeeded — disk is reachable. Clear any prior 'stale' so the
      // chip stops claiming reloadFailed; the dirty buffer is left untouched.
      diskState.value = 'available'
      if (conflictState.value === 'none') conflictState.value = 'chip'
      return
    }
    content.value = data.content ?? ''
    originalContent.value = content.value
    baseRevision.value = data.revision ?? ''
    observedExternalRevision.value = ''
    lastLoadedAt.value = Date.now()
    loaded.value = true
    diskState.value = 'available'
    // Don't drop the user out of an open Compare; only clear the chip surface.
    if (conflictState.value !== 'compare') conflictState.value = 'none'
  } catch (error) {
    if (controller.signal.aborted) return
    if (isHttpStatus(error, 404)) {
      diskState.value = 'deleted'
      loaded.value = true
      dropToChipForBadDiskState()
      return
    }
    diskState.value = 'stale'
    loaded.value = true
    dropToChipForBadDiskState()
    if (options.notifyOnError !== false) {
      toast.error(resolveApiErrorMessage(error, t('bots.files.readFailed')))
    }
  } finally {
    if (activeReadController === controller) {
      activeReadController = null
      loading.value = false
    }
  }
}

async function loadImageBlob(options: { notifyOnError?: boolean } = {}) {
  activeReadController?.abort()
  const controller = new AbortController()
  activeReadController = controller
  loading.value = true
  let url = ''
  try {
    const response = await getBotsByBotIdContainerFsDownload({
      path: { bot_id: props.botId },
      query: { path: filePath.value },
      parseAs: 'blob',
      signal: controller.signal,
      throwOnError: true,
    })
    if (controller.signal.aborted) return
    const blob = response.data as unknown as Blob
    url = URL.createObjectURL(blob)
    cleanupImageUrl()
    imageUrl.value = url
    url = ''
    imageMetadataFingerprint.value = fileMetadataFingerprint({
      path: filePath.value,
      isDir: false,
      size: blob.size,
      modTime: props.file.modTime,
    })
    lastLoadedAt.value = Date.now()
    loaded.value = true
    diskState.value = 'available'
    if (conflictState.value !== 'compare') conflictState.value = 'none'
  } catch (error) {
    if (controller.signal.aborted) return
    // Release any stale blob URL from a prior successful load so the deleted /
    // stale states don't keep a now-detached <img> source alive.
    cleanupImageUrl()
    if (isHttpStatus(error, 404)) {
      diskState.value = 'deleted'
      loaded.value = true
      dropToChipForBadDiskState()
      return
    }
    diskState.value = 'stale'
    loaded.value = true
    dropToChipForBadDiskState()
    if (options.notifyOnError !== false) {
      toast.error(resolveApiErrorMessage(error, t('bots.files.readFailed')))
    }
  } finally {
    if (url) URL.revokeObjectURL(url)
    if (activeReadController === controller) {
      activeReadController = null
      loading.value = false
    }
  }
}

// Returns whether the file is in a saved state afterwards, so an external caller
// (the tab close-confirm flow) can decide whether to proceed with closing. A
// read-only or already-clean file reports success without a network round-trip.
async function handleSave(force = false): Promise<boolean> {
  const behavior = resolveSaveBehavior({
    readonly: props.readonly ?? false,
    saving: saving.value,
    isDirty: isDirty.value,
    force,
    diskState: diskState.value,
  })
  if (behavior.outcome === 'noop') return true
  if (behavior.outcome === 'block') return false
  // VS Code-style two-gate save: even if the chip wasn't surfaced yet (e.g.
  // user pressed Cmd+S in the brief window between bump and consumer
  // reaction), re-check the baseline before we POST. bypassConflictGuard skips
  // this — that's the "Save anyway" / "Save to restore" path the user
  // explicitly chose (or the ORPHAN-recreate path where there's no concurrent
  // disk version).
  if (!behavior.bypassConflictGuard) {
    const hasConflict = detectSaveConflict({
      lastFsChangeAt: fsChangedAt.value,
      lastLoadedAt: lastLoadedAt.value,
      affects: chatStore.affectsPath(filePath.value),
    })
    if (hasConflict) {
      // Don't drop the user back to compare if they were already comparing —
      // but flag the diff as stale so the toolbar offers a Refresh diff
      // affordance instead of leaving them on the now-outdated snapshot.
      if (conflictState.value === 'compare') compareStale.value = true
      else conflictState.value = 'chip'
      return false
    }
  }
  saving.value = true
  // Snapshot pre-save chip context so we can:
  //   a) detect an agent bump that lands inside the POST window (the
  //      fsChangedAt watcher is gated by saving=true and would otherwise drop
  //      that signal)
  //   b) tone down the 409 toast when the conflict followed a known-bad read
  const fsChangedAtBeforeSave = fsChangedAt.value
  const diskStateBeforeSave = diskState.value
  try {
    // Empty string isn't a meaningful baseline — fall through to an
    // unconditional write rather than asking the backend to interpret '' as
    // "expect file absent". The bypass path already drops the field; this
    // covers the normal-save-with-no-known-baseline case (e.g. saving on top
    // of a stale read that never returned a revision).
    const sendBaseline = !behavior.bypassConflictGuard && baseRevision.value !== ''
    const requestBody = sendBaseline
      ? { path: filePath.value, content: content.value, expectedRevision: baseRevision.value }
      : { path: filePath.value, content: content.value }
    const { data } = await postBotsByBotIdContainerFsWrite({
      path: { bot_id: props.botId },
      body: requestBody,
      throwOnError: true,
    })
    originalContent.value = content.value
    baseRevision.value = data.revision ?? baseRevision.value
    observedExternalRevision.value = ''
    diskState.value = 'available'
    // An agent bump observed during our POST window means the disk diverged
    // from what our save just persisted. Anchor lastLoadedAt to BEFORE that
    // bump so detectSaveConflict still fires on the next Cmd+S, and surface
    // the chip now rather than waiting for the user to try again.
    const interleavedBump =
      fsChangedAt.value > fsChangedAtBeforeSave
      && chatStore.affectsPath(filePath.value)
    if (interleavedBump) {
      lastLoadedAt.value = fsChangedAtBeforeSave > 0 ? fsChangedAtBeforeSave : 1
      if (conflictState.value === 'compare') compareStale.value = true
      else conflictState.value = 'chip'
    } else {
      lastLoadedAt.value = Date.now()
      conflictState.value = 'none'
    }
    toast.success(t('bots.files.saveSuccess'))
    emit('saved')
    return true
  } catch (error) {
    if (isHttpStatus(error, 409)) {
      // A 409 means our baseline is stale; bumping the disk state surfaces
      // "Try again" (Reload) as the chip's primary affordance and prevents a
      // Cmd+S loop from re-POSTing with the same stale expectedRevision.
      diskState.value = 'stale'
      if (conflictState.value === 'compare') compareStale.value = true
      else conflictState.value = 'chip'
      // Skip the saveConflict toast when the chip already explains a known-
      // bad read; the wording would contradict the "couldn't load" chip text.
      if (diskStateBeforeSave === 'available') {
        toast.error(t('bots.files.externalChange.saveConflict'))
      }
      return false
    }
    toast.error(resolveApiErrorMessage(error, t('bots.files.saveFailed')))
    return false
  } finally {
    saving.value = false
  }
}

function forceSave() {
  void handleSave(true)
}

defineExpose({ save: () => handleSave(false) })

function handleDownload() {
  const url = sdkApiUrl({
    url: '/bots/{bot_id}/container/fs/download',
    path: { bot_id: props.botId },
    query: { path: filePath.value, ...sdkAuthQuery() },
  })
  const a = document.createElement('a')
  a.href = url
  a.download = filename.value
  a.click()
}

function cleanupImageUrl() {
  if (imageUrl.value) {
    URL.revokeObjectURL(imageUrl.value)
    imageUrl.value = ''
  }
}

watch(() => props.file.path, () => {
  // Tear down any in-flight load and the compare snapshot before the new
  // path's loaders kick off — otherwise a slow previous-path read could
  // resolve onto the new path's empty buffer and silently surface a chip on a
  // file that's never even been loaded.
  activeReadController?.abort()
  activeReadController = null
  compareController?.abort()
  compareController = null
  cleanupImageUrl()
  content.value = ''
  originalContent.value = ''
  baseRevision.value = ''
  diskState.value = 'available'
  conflictState.value = 'none'
  compareDiskContent.value = ''
  compareStale.value = false
  lastLoadedAt.value = 0
  observedExternalRevision.value = ''
  imageMetadataFingerprint.value = ''
  loaded.value = false
  if (isText.value) {
    void loadTextContent()
  } else if (isImage.value) {
    void loadImageBlob()
  }
}, { immediate: true })

// Reload the file when the chat agent runs a fs-mutating tool (write/edit/apply_patch/exec)
// against the same bot AND the change targets this path. Skip if the user is
// mid-save — the in-flight save owns content/originalContent and we'd race it.
// If the user has unsaved edits, surface an inline notice rather than silently
// reloading or silently dropping the agent's change.
watch(fsChangedAt, () => {
  if (!props.botId || props.botId !== currentBotId.value) return
  if (!chatStore.affectsPath(filePath.value)) return
  if (saving.value) return
  // While Compare is open the user is actively reviewing — never tear it down
  // or silently rewrite the right pane underneath them. Mark the disk side as
  // stale so the toolbar can offer a Refresh-diff affordance instead.
  if (conflictState.value === 'compare') {
    compareStale.value = true
    return
  }
  if (isDirty.value) {
    if (conflictState.value === 'none') conflictState.value = 'chip'
    return
  }
  if (isText.value) void loadTextContent({ notifyOnError: false })
  else if (isImage.value) void loadImageBlob({ notifyOnError: false })
})

async function acceptExternalChange() {
  // Reload from the Compare toolbar means "apply the disk side and dismiss
  // the diff" — keeping the user in Compare with the OLD compareDiskContent
  // pointing at the previous snapshot and the new content from this load
  // would render a meaningless diff against itself.
  const wasInCompare = conflictState.value === 'compare'
  if (isText.value) await loadTextContent({ forceApply: true })
  else if (isImage.value) await loadImageBlob()
  if (wasInCompare && diskState.value === 'available') {
    compareController?.abort()
    compareController = null
    compareDiskContent.value = ''
    compareStale.value = false
    conflictState.value = 'none'
  }
}

async function openCompare() {
  if (!isText.value) return
  // Abort any in-flight Compare read first so a previously dispatched
  // fresh-read can't resolve later and clobber the writeContent we're about
  // to set on the fast path (or this call's own fresh read).
  compareController?.abort()
  compareController = null
  // Prefer the agent's tool-message content — same bytes the server saw, no
  // extra round trip and immune to "what if disk changed again" races. Fall
  // back to a fresh read for edit / apply_patch / exec where we don't have the
  // whole file.
  const event = fsEvent.value
  if (event?.writeContent != null) {
    compareDiskContent.value = event.writeContent
    compareStale.value = false
    conflictState.value = 'compare'
    return
  }
  const controller = new AbortController()
  compareController = controller
  try {
    const { data } = await getBotsByBotIdContainerFsRead({
      path: { bot_id: props.botId },
      query: { path: filePath.value },
      signal: controller.signal,
      throwOnError: true,
    })
    if (controller.signal.aborted) return
    compareDiskContent.value = data.content ?? ''
    compareStale.value = false
    conflictState.value = 'compare'
  } catch (error) {
    if (controller.signal.aborted) return
    if (isHttpStatus(error, 404)) {
      // The disk version we wanted to diff against is gone. Fall back to the
      // deleted-chip surface so the "Save to restore" affordance is reachable
      // instead of leaving the user on the available chip clicking Compare
      // into the same 404 repeatedly.
      diskState.value = 'deleted'
      compareDiskContent.value = ''
      compareStale.value = false
      conflictState.value = 'chip'
      return
    }
    toast.error(resolveApiErrorMessage(error, t('bots.files.compareLoadFailed')))
  } finally {
    if (compareController === controller) compareController = null
  }
}

function exitCompare() {
  compareController?.abort()
  compareController = null
  compareStale.value = false
  conflictState.value = 'chip'
}

async function refreshCompare() {
  // Re-runs openCompare to re-snapshot the right pane against the latest
  // tool-message content (or a fresh disk read when no event content is on
  // hand). Used by the inline "Refresh diff" affordance the toolbar surfaces
  // when compareStale is true.
  await openCompare()
}

// Save on Cmd/Ctrl+S via the shared keyboard layer. Even if a conflict is
// detected handleSave returns false — but we still report `true` to the
// keyboard layer because we've already shown the conflict UI; falling through
// to browser save would be confusing.
useKeyboardCommand(appKeyboardCommands.saveActiveFile, () => {
  if (!isFileSaveEligible({
    readonly: props.readonly ?? false,
    isText: isText.value,
    isDirty: isDirty.value,
    saving: saving.value,
  })) {
    return false
  }
  void handleSave(false)
  return true
})

onMounted(() => {
  externalPollingActive = true
  nowTickInterval = setInterval(() => { nowTick.value = Date.now() }, 60_000)
  document.addEventListener('visibilitychange', handleVisibilityChange)
  startExternalFilePoll()
})

onActivated(() => {
  externalPollingActive = true
  startExternalFilePoll()
  void pollExternalFile()
})

onDeactivated(() => {
  externalPollingActive = false
  stopExternalFilePoll()
})

onBeforeUnmount(() => {
  externalPollingActive = false
  if (nowTickInterval) clearInterval(nowTickInterval)
  nowTickInterval = null
  document.removeEventListener('visibilitychange', handleVisibilityChange)
  stopExternalFilePoll()
  activeReadController?.abort()
  compareController?.abort()
  cleanupImageUrl()
})
</script>

<template>
  <div class="flex h-full flex-col overflow-hidden bg-surface-editor">
    <!-- Chip: inline banner above the editor when we have a pending external
         change. role=status + aria-live=polite live only on the chipMessage
         span so the buttons aren't dragged into the per-minute re-announce
         when the relative-time label ages; aria-describedby on every action
         button gives the chip text as context. -->
    <div
      v-if="conflictState === 'chip'"
      class="flex shrink-0 items-center gap-2 border-b border-border bg-accent/40 px-3 py-1.5 text-caption text-foreground"
    >
      <FileX
        v-if="diskState === 'deleted'"
        class="size-3.5 shrink-0 text-muted-foreground"
        aria-hidden="true"
      />
      <RefreshCw
        v-else
        class="size-3.5 shrink-0 text-muted-foreground"
        aria-hidden="true"
      />
      <span
        :id="chipMessageId"
        role="status"
        aria-live="polite"
        aria-atomic="true"
        class="min-w-0 flex-1 truncate"
      >{{ chipMessage }}</span>
      <template
        v-for="(btn, idx) in chipButtons"
        :key="idx"
      >
        <Button
          v-if="btn.kind === 'compare'"
          variant="ghost"
          size="sm"
          class="h-6 shrink-0 px-2 text-xs"
          :aria-describedby="chipMessageId"
          @click="onChipButton(btn)"
        >
          <GitCompare
            class="mr-1 size-3"
            aria-hidden="true"
          />
          {{ t('bots.files.externalChange.compare') }}
        </Button>
        <Button
          v-else-if="btn.kind === 'reload'"
          variant="ghost"
          size="sm"
          class="h-6 shrink-0 px-2 text-xs"
          :aria-describedby="chipMessageId"
          @click="onChipButton(btn)"
        >
          {{ t(reloadLabelKey(btn.labelKey)) }}
        </Button>
        <Button
          v-else-if="btn.kind === 'forceSave'"
          variant="ghost"
          size="sm"
          class="h-6 shrink-0 px-2 text-xs"
          :aria-describedby="chipMessageId"
          @click="onChipButton(btn)"
        >
          {{ t(saveLabelKey(btn.labelKey)) }}
        </Button>
      </template>
    </div>

    <div class="flex-1 min-h-0 overflow-hidden">
      <!-- Compare view: diff editor replacing the main pane -->
      <div
        v-if="conflictState === 'compare'"
        class="flex h-full flex-col"
      >
        <DiffTitleBar>
          <span class="min-w-0 truncate">
            <span>{{ t('bots.files.compare.yours') }} ↔ {{ t('bots.files.compare.disk') }}</span>
            <span
              v-if="compareStale"
              :id="compareStaleId"
              class="ml-2 text-warning"
              role="status"
              aria-live="polite"
              aria-atomic="true"
            >· {{ t('bots.files.compare.staleNotice') }}</span>
          </span>
          <template #actions>
            <Button
              v-if="compareStale"
              variant="ghost"
              size="sm"
              class="h-6 px-2 text-xs"
              :aria-describedby="compareStaleId"
              @click="refreshCompare"
            >
              <RefreshCw
                class="mr-1 size-3"
                aria-hidden="true"
              />
              {{ t('bots.files.compare.refresh') }}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              class="h-6 px-2 text-xs"
              @click="acceptExternalChange"
            >
              {{ t('bots.files.externalChange.reload') }}
            </Button>
            <Button
              v-if="isDirty"
              variant="ghost"
              size="sm"
              class="h-6 px-2 text-xs"
              @click="forceSave"
            >
              {{ t('bots.files.externalChange.saveAnyway') }}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              class="h-6 px-2 text-xs text-muted-foreground"
              @click="exitCompare"
            >
              <X
                class="mr-1 size-3"
                aria-hidden="true"
              />
              {{ t('bots.files.compare.close') }}
            </Button>
          </template>
        </DiffTitleBar>
        <MonacoDiff
          :original="compareDiskContent"
          :modified="content"
          :filename="filename"
          :original-title="t('bots.files.compare.disk')"
          :modified-title="t('bots.files.compare.yours')"
          class="min-h-0 flex-1"
        />
      </div>

      <!-- Full-area spinner only on the FIRST load for this path (nothing else
           to show). Subsequent reloads keep the editor/image mounted and let
           the content swap in place — the external-change UX of a code editor. -->
      <PanePlaceholder
        v-else-if="loading && !loaded"
        loading
      >
        {{ t('common.loading') }}
      </PanePlaceholder>

      <MonacoEditor
        v-else-if="isText"
        ref="monacoEditorRef"
        v-model="content"
        :filename="filename"
        :readonly="readonly"
        class="h-full"
      />

      <PanePlaceholder v-else-if="isImage && diskState === 'deleted'">
        <template #icon>
          <FileX class="size-12 text-destructive opacity-40" />
        </template>
        {{ t('bots.files.imageDeleted') }}
      </PanePlaceholder>

      <div
        v-else-if="isImage && imageUrl"
        class="flex h-full items-center justify-center overflow-auto p-4 bg-muted/30"
      >
        <img
          :src="imageUrl"
          :alt="filename"
          class="max-h-full max-w-full object-contain rounded"
        >
      </div>

      <PanePlaceholder v-else>
        <template #icon>
          <File class="size-12 opacity-30" />
        </template>
        {{ t('bots.files.previewNotAvailable') }}
        <template #action>
          <Button
            variant="outline"
            size="sm"
            @click="handleDownload"
          >
            <Download class="mr-1.5 size-3" />
            {{ t('bots.files.download') }}
          </Button>
        </template>
      </PanePlaceholder>
    </div>
  </div>
</template>
