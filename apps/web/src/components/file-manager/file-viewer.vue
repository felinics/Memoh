<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { appKeyboardCommands } from '@/lib/keyboard-commands'
import { useKeyboardCommand } from '@/composables/useKeyboardCommand'
import { useVisiblePoll } from '@/composables/useVisiblePoll'
import { isFileSaveEligible } from './file-save-command'
import {
  isContainerFileNotFound,
  syncContainerTextFileCache,
  useContainerTextFileQuery,
  utf8ByteLength,
  type ContainerTextFileSnapshot,
} from './use-container-text-file'
import {
  detectSaveConflict,
  deriveChipContext,
  fileMetadataFingerprint,
  resolveChipButtons,
  resolvePolledTextChange,
  resolveSaveBehavior,
  type ChipButton,
} from './file-conflict'
import { useI18n } from 'vue-i18n'
import { useQueryCache } from '@pinia/colada'
import { DiffTitleBar, PanePlaceholder, toast } from '@felinic/ui'
import { File, FileX, Download, RefreshCw, GitCompare, X } from 'lucide-vue-next'
import { Button } from '@felinic/ui'
import {
  getBotsByBotIdContainerFs,
  postBotsByBotIdContainerFsWrite,
  getBotsByBotIdContainerFsDownload,
} from '@memohai/sdk'
import type { HandlersFsFileInfo } from '@memohai/sdk'
import { apiErrorStatus, resolveApiErrorMessage } from '@/utils/api-error'
import CodeEditor from '@/components/code-surface/CodeEditor.vue'
import CodeDiff from '@/components/code-surface/CodeDiff.vue'
import { useCodeSurfaceAppearance } from '@/components/code-surface/theme'
import { sdkApiUrl, sdkAuthQuery } from '@/lib/api-client'
import { fetchBoundedBlob } from '@/lib/bounded-fetch'
import { formatRelativeTime } from '@/utils/date-time'
import { formatFileSize, getDocumentMimeType, getDocumentPreviewKind, getDocumentPreviewMaxBytes, isImageFile, isTextFile } from './utils'
import { useChatStore } from '@/store/chat-list'
import { storeToRefs } from 'pinia'
import DocumentPreview from './document-preview.vue'

const props = withDefaults(defineProps<{
  botId: string
  file: HandlersFsFileInfo
  readonly?: boolean
  visible?: boolean
  keyboardActive?: boolean
}>(), {
  readonly: false,
  visible: true,
  keyboardActive: undefined,
})

const EXTERNAL_FILE_POLL_MS = 2_000

const emit = defineEmits<{
  saved: []
  'update:dirty': [dirty: boolean]
}>()

const { t } = useI18n()
const queryCache = useQueryCache()
const {
  theme: surfaceTheme,
  themeType: surfaceThemeType,
  fontSize: surfaceFontSize,
  lineHeight: surfaceLineHeight,
  fontFamily: surfaceFontFamily,
} = useCodeSurfaceAppearance()

const content = ref('')
const originalContent = ref('')
const baseRevision = ref('')
const loading = ref(false)
const saving = ref(false)
const savingContent = ref<string | null>(null)
let fileGeneration = 0
const imageUrl = ref('')
const documentBlob = ref<Blob | null>(null)
const documentOversizedBytes = ref<number | null>(null)
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
const compareDiskRevision = ref('')
// Flips to true when a fsChangedAt for this path lands while we're in
// conflictState='compare'. Reset on every successful openCompare(), on path
// change, and on exit.
const compareStale = ref(false)
// Bumped to drop the result of a Compare read that is no longer wanted.
let compareRequest = 0
// The disk revision last seen to differ from `baseRevision` without being
// applied (unsaved edits, or Compare open). Non-empty means a normal save would
// overwrite a change the user has not accepted.
const observedExternalRevision = ref('')
const binaryMetadataFingerprint = ref('')
// A reload that was skipped or cut short because the panel was hidden; it runs
// when the panel is shown again.
let pendingExternalRefresh = false
// Set when a read or stat of the text file failed. The next successful read
// then also clears the "deleted" / "couldn't reload" notice, even when it
// returns the revision the editor already holds.
let diskUnreadable = false
// Identifies the latest read this viewer started, so an overtaken one neither
// ends the loading state nor applies its result.
let textReadToken = 0
// The next disk content applied replaces the editor content unconditionally.
let forceApplyNextRead = false
let externalPollController: AbortController | null = null
// Stable ids so the chip buttons can aria-describedby the relevant inline
// message span, giving keyboard / screen-reader users the "why is this here"
// context. The compare-toolbar Refresh button uses the staleNotice id by the
// same pattern.
const chipMessageId = useId()
const compareStaleId = useId()
// Reference to the code editor so we can hand focus back when an action removes
// the chip / closes Compare and the activated button itself was just torn
// down. Without this, focus would fall through to document.body.
const codeEditorRef = ref<{ focus?: () => void } | null>(null)
// Drives "5s ago" / "2m ago" relative-time labels in the chip. Ticks once a
// minute; agent writes are usually fresh enough that "just now" wins, but a
// chip left up across coffee should age gracefully.
const nowTick = ref(Date.now())
let nowTickInterval: ReturnType<typeof setInterval> | null = null

const filename = computed(() => props.file.name ?? '')
const filePath = computed(() => props.file.path ?? '')
const isText = computed(() => isTextFile(filename.value))
const isImage = computed(() => isImageFile(filename.value))
const documentKind = computed(() => getDocumentPreviewKind(filename.value))
const isDocument = computed(() => documentKind.value !== null)
const isDirty = computed(() => content.value !== originalContent.value
  || (savingContent.value !== null && content.value !== savingContent.value))
// The first load shows a full-area spinner instead; afterwards the editor stays
// mounted through reloads so content swaps in place.
const editorMounted = computed(() => isText.value && (loaded.value || !loading.value))

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
  // The editor exposes focus(); test doubles for it may not.
  codeEditorRef.value?.focus?.()
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

// One in-flight image / document download at a time per viewer; a new load
// aborts the old one so a slower stale response can't overwrite newer content
// when fsChangedAt fires in bursts.
let activeReadController: AbortController | null = null

// Force the chip back into view on a terminal disk-state transition (delete /
// stale read). Tears down any active Compare so the user isn't reviewing a diff
// against bytes that no longer exist.
function dropToChipForBadDiskState() {
  compareRequest++
  compareDiskContent.value = ''
  compareDiskRevision.value = ''
  compareStale.value = false
  conflictState.value = 'chip'
}

// A text file's content reaches this viewer through one channel: the shared
// per-file query. Its poll (one stat per interval, one read per external
// change, shared by every panel showing the file), this viewer's own reads
// (first load, agent-tool notifications, Reload, Compare) and its saves all end
// up as the query's state, and `reconcileText` is the only place that decides
// what that state means for the editor.
const textQuery = useContainerTextFileQuery(() => props.botId, filePath, {
  visible: () => props.visible,
  enabled: isText,
})

function applyDiskSnapshot(snapshot: ContainerTextFileSnapshot, force: boolean) {
  const recovered = diskUnreadable
  diskUnreadable = false
  const action = force
    ? 'apply'
    : resolvePolledTextChange({
        loaded: true,
        loading: false,
        saving: false,
        currentRevision: conflictState.value === 'compare' ? compareDiskRevision.value : baseRevision.value,
        nextRevision: snapshot.revision,
        isDirty: isDirty.value,
        conflictState: conflictState.value,
      })
  if (action === 'ignore') {
    // The disk holds the revision the editor is based on again: a failed read
    // or stat recovered, a deleted file was recreated with the same content, or
    // an external change was reverted. `observedExternalRevision` being set
    // means an earlier snapshot differed, so this one is a newer read and the
    // save baseline is confirmed as of now.
    const revertedToBase = conflictState.value !== 'compare'
      && observedExternalRevision.value !== ''
      && snapshot.revision !== ''
      && snapshot.revision === baseRevision.value
    if (recovered || revertedToBase) {
      diskState.value = 'available'
      observedExternalRevision.value = ''
      if (revertedToBase) lastLoadedAt.value = Date.now()
      if (conflictState.value === 'chip') conflictState.value = 'none'
    }
    return
  }
  if (action === 'apply') {
    content.value = snapshot.content
    originalContent.value = snapshot.content
    baseRevision.value = snapshot.revision
    observedExternalRevision.value = ''
    lastLoadedAt.value = Date.now()
    diskState.value = 'available'
    conflictState.value = 'none'
    return
  }
  observedExternalRevision.value = snapshot.revision
  diskState.value = 'available'
  if (action === 'mark-compare-stale') {
    compareStale.value = true
    return
  }
  if (conflictState.value === 'none') conflictState.value = 'chip'
}

function markDiskUnreadable(error: unknown) {
  diskUnreadable = true
  if (isContainerFileNotFound(error)) {
    diskState.value = 'deleted'
    observedExternalRevision.value = 'deleted'
  } else {
    diskState.value = 'stale'
  }
  dropToChipForBadDiskState()
}

// Brings the editor in line with the query's current state. Safe to call at any
// time: it does nothing until the first load has finished, and while a read
// this viewer started or a save of this file is in flight; both call it again
// when they end. (`saving` itself can still belong to the previously shown
// file; `savingContent` is cleared on a switch.)
function reconcileText() {
  if (!isText.value || !loaded.value || loading.value || savingContent.value !== null) return
  const readError = textQuery.error.value
  const error = readError ?? textQuery.pollError.value
  if (error) {
    // A pending Reload ends with the read it asked for, failed or applied. A
    // failed stat says nothing about that read, so it leaves the request alone.
    if (readError) forceApplyNextRead = false
    markDiskUnreadable(error)
    return
  }
  const snapshot = textQuery.data.value
  if (!snapshot) return
  const force = forceApplyNextRead
  forceApplyNextRead = false
  applyDiskSnapshot(snapshot, force)
}

watch(
  [() => textQuery.data.value, () => textQuery.error.value, textQuery.pollError, () => props.botId, filePath] as const,
  ([, , , botId, path], [, , , previousBotId, previousPath]) => {
    // Switching files swaps in the new file's cache entry; that is handled by
    // the load the switch starts, not as an external change to the previous file.
    if (botId !== previousBotId || path !== previousPath) return
    reconcileText()
  },
)

// Reads the text file through the shared query and reports whether this is
// still the read the viewer is waiting for. A newer read, or a switch to
// another file, takes over from it.
//
// `loading` stays set on success: the caller clears it in the same synchronous
// step in which it applies the result. Clearing it here would leave a render
// between "not loading" and "content applied", and on first open the editor
// would be created with the empty buffer.
async function readTextFromDisk(): Promise<boolean> {
  const generation = fileGeneration
  const token = ++textReadToken
  loading.value = true
  // On first open the query may have just started reading the file itself;
  // join that read. Otherwise always read again, so the editor never opens on
  // a copy that was cached while no panel was watching the file.
  await (!loaded.value && textQuery.isLoading.value ? textQuery.refresh() : textQuery.refetch())
  return generation === fileGeneration && token === textReadToken
}

// `forceApply` replaces the editor content with the disk content even over
// unsaved edits (the user chose Reload); the first load always does. The
// request stays pending until a read is actually applied, so it survives being
// overtaken by another read or held back by a save.
async function loadTextContent(options: { forceApply?: boolean; notifyOnError?: boolean } = {}) {
  if (options.forceApply || !loaded.value) forceApplyNextRead = true
  if (!(await readTextFromDisk())) return
  loading.value = false
  loaded.value = true
  reconcileText()
  const error = textQuery.error.value
  if (error && options.notifyOnError !== false && !isContainerFileNotFound(error)) {
    toast.error(resolveApiErrorMessage(error, t('bots.files.readFailed')))
  }
}

async function pollBinaryFile(signal: AbortSignal) {
  const { data } = await getBotsByBotIdContainerFs({
    path: { bot_id: props.botId },
    query: { path: filePath.value },
    signal,
    throwOnError: true,
  })
  if (signal.aborted) return
  const nextFingerprint = fileMetadataFingerprint(data)
  const hasHealthyBinary = diskState.value === 'available' && (
    isImage.value
      ? Boolean(imageUrl.value)
      : (documentBlob.value !== null || documentOversizedBytes.value !== null)
  )
  if (!binaryMetadataFingerprint.value && hasHealthyBinary) {
    binaryMetadataFingerprint.value = nextFingerprint
    return
  }
  if (hasHealthyBinary && nextFingerprint === binaryMetadataFingerprint.value) return
  const previousFingerprint = binaryMetadataFingerprint.value
  if (isImage.value) await loadImageBlob({ notifyOnError: false })
  else await loadDocumentBlob({ notifyOnError: false })
  if (signal.aborted) return
  binaryMetadataFingerprint.value = diskState.value === 'available' ? nextFingerprint : previousFingerprint
}

async function pollExternalBinary() {
  if (!props.visible || !props.botId || (!isImage.value && !isDocument.value) || !loaded.value || loading.value || externalPollController) return

  const controller = new AbortController()
  externalPollController = controller
  try {
    await pollBinaryFile(controller.signal)
  } catch (error) {
    if (controller.signal.aborted) return
    if (isContainerFileNotFound(error)) {
      cleanupImageUrl()
      documentBlob.value = null
      documentOversizedBytes.value = null
      diskState.value = 'deleted'
      loaded.value = true
      observedExternalRevision.value = 'deleted'
      binaryMetadataFingerprint.value = ''
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

useVisiblePoll(pollExternalBinary, EXTERNAL_FILE_POLL_MS, {
  enabled: () => Boolean(props.visible && props.botId && (isImage.value || isDocument.value)),
})

function applyReadError(error: unknown, notifyOnError?: boolean) {
  loaded.value = true
  if (isContainerFileNotFound(error)) {
    diskState.value = 'deleted'
    dropToChipForBadDiskState()
    return
  }
  diskState.value = 'stale'
  dropToChipForBadDiskState()
  if (notifyOnError !== false) toast.error(resolveApiErrorMessage(error, t('bots.files.readFailed')))
}

function markBinaryLoaded(size?: number) {
  binaryMetadataFingerprint.value = size !== undefined && !loaded.value && props.file.modTime
    ? fileMetadataFingerprint({ path: filePath.value, isDir: false, size, modTime: props.file.modTime })
    : ''
  lastLoadedAt.value = Date.now()
  loaded.value = true
  diskState.value = 'available'
  if (conflictState.value !== 'compare') conflictState.value = 'none'
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
    markBinaryLoaded(blob.size)
  } catch (error) {
    if (controller.signal.aborted) return
    cleanupImageUrl()
    applyReadError(error, options.notifyOnError)
  } finally {
    if (url) URL.revokeObjectURL(url)
    if (activeReadController === controller) {
      activeReadController = null
      loading.value = false
    }
  }
}

async function loadDocumentBlob(options: { notifyOnError?: boolean } = {}) {
  const kind = documentKind.value
  if (!kind) return
  activeReadController?.abort()
  const controller = new AbortController()
  activeReadController = controller
  loading.value = true
  try {
    const result = await fetchBoundedBlob({
      target: { url: '/bots/{bot_id}/container/fs/download', path: { bot_id: props.botId }, query: { path: filePath.value } },
      maxBytes: getDocumentPreviewMaxBytes(kind),
      mimeType: getDocumentMimeType(kind),
      signal: controller.signal,
    })
    if (controller.signal.aborted) return
    documentBlob.value = result.ok ? result.blob : null
    documentOversizedBytes.value = result.ok ? null : result.maxBytes
    markBinaryLoaded(result.ok ? result.blob.size : undefined)
  } catch (error) {
    if (controller.signal.aborted) return
    documentBlob.value = null
    documentOversizedBytes.value = null
    applyReadError(error, options.notifyOnError)
  } finally {
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
    const hasConflict = observedExternalRevision.value !== '' || detectSaveConflict({
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
  const snapshot = {
    botId: props.botId,
    path: filePath.value,
    content: content.value,
    revision: baseRevision.value,
    generation: fileGeneration,
  }
  savingContent.value = snapshot.content
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
    const sendBaseline = !behavior.bypassConflictGuard && snapshot.revision !== ''
    const requestBody = sendBaseline
      ? { path: snapshot.path, content: snapshot.content, expectedRevision: snapshot.revision }
      : { path: snapshot.path, content: snapshot.content }
    const { data } = await postBotsByBotIdContainerFsWrite({
      path: { bot_id: snapshot.botId },
      body: requestBody,
      throwOnError: true,
    })
    if (snapshot.generation !== fileGeneration) return false
    originalContent.value = snapshot.content
    baseRevision.value = data.revision ?? snapshot.revision
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
    // Other panels showing this file get the saved content at once, and a read
    // that was already in flight can no longer bring back the pre-save bytes.
    syncContainerTextFileCache(queryCache, snapshot.botId, snapshot.path, {
      content: snapshot.content,
      revision: baseRevision.value,
      size: utf8ByteLength(snapshot.content),
    })
    toast.success(t('bots.files.saveSuccess'))
    emit('saved')
    return !isDirty.value
  } catch (error) {
    if (snapshot.generation !== fileGeneration) return false
    if (apiErrorStatus(error) === 409) {
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
    if (snapshot.generation === fileGeneration) savingContent.value = null
    saving.value = false
    // Disk state that arrived during the save was held back; apply it now.
    reconcileText()
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

watch([() => props.botId, () => props.file.path], () => {
  fileGeneration++
  savingContent.value = null
  externalPollController?.abort()
  externalPollController = null
  // Tear down any in-flight load and the compare snapshot before the new
  // path's loaders kick off — otherwise a slow previous-path read could
  // resolve onto the new path's empty buffer and silently surface a chip on a
  // file that's never even been loaded.
  activeReadController?.abort()
  activeReadController = null
  compareRequest++
  loading.value = false
  cleanupImageUrl()
  documentBlob.value = null
  documentOversizedBytes.value = null
  content.value = ''
  originalContent.value = ''
  baseRevision.value = ''
  diskState.value = 'available'
  conflictState.value = 'none'
  compareDiskContent.value = ''
  compareDiskRevision.value = ''
  compareStale.value = false
  lastLoadedAt.value = 0
  observedExternalRevision.value = ''
  binaryMetadataFingerprint.value = ''
  pendingExternalRefresh = false
  diskUnreadable = false
  forceApplyNextRead = false
  loaded.value = false
  if (isText.value) {
    void loadTextContent()
  } else if (isImage.value) {
    void loadImageBlob()
  } else if (isDocument.value) {
    // A document is only downloaded for a panel that is showing it.
    if (props.visible) void loadDocumentBlob()
    else pendingExternalRefresh = true
  }
}, { immediate: true })

function reloadFromDisk(options: { forceApply?: boolean; notifyOnError?: boolean } = {}) {
  if (isText.value) return loadTextContent(options)
  if (isImage.value) return loadImageBlob(options)
  if (isDocument.value) return loadDocumentBlob(options)
  return Promise.resolve()
}

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
  if (!props.visible) {
    // A hidden text file needs no reminder: its stat poll resumes with the
    // panel and picks the change up.
    if (!isText.value) pendingExternalRefresh = true
    return
  }
  void reloadFromDisk({ notifyOnError: false })
})

watch(() => props.visible, (visible) => {
  if (!visible) {
    externalPollController?.abort()
    externalPollController = null
    // A hidden panel does not keep downloading a document (up to tens of MB);
    // the download starts over when the panel is shown again.
    if (isDocument.value && activeReadController) {
      activeReadController.abort()
      activeReadController = null
      loading.value = false
      pendingExternalRefresh = true
    }
    return
  }
  if (pendingExternalRefresh) {
    pendingExternalRefresh = false
    void reloadFromDisk({ notifyOnError: false })
    return
  }
  void pollExternalBinary()
})

async function acceptExternalChange() {
  // Reload from the Compare toolbar means "apply the disk side and dismiss
  // the diff" — keeping the user in Compare with the OLD compareDiskContent
  // pointing at the previous snapshot and the new content from this load
  // would render a meaningless diff against itself.
  const wasInCompare = conflictState.value === 'compare'
  await reloadFromDisk({ forceApply: true })
  if (wasInCompare && diskState.value === 'available') {
    compareRequest++
    compareDiskContent.value = ''
    compareDiskRevision.value = ''
    compareStale.value = false
    conflictState.value = 'none'
  }
}

// The disk side of Compare is always what the disk holds now, read through the
// shared query, so the diff and the staleness check that follows refer to the
// same revision.
async function openCompare() {
  if (!isText.value) return
  const request = ++compareRequest
  if (!(await readTextFromDisk())) return
  loading.value = false
  if (request !== compareRequest) return
  const error = textQuery.error.value
  const snapshot = textQuery.data.value
  if (error || !snapshot) {
    // A missing file falls back to the deleted notice, where "Save to restore"
    // is reachable; any other failure to the "couldn't reload" notice.
    reconcileText()
    if (error && !isContainerFileNotFound(error)) {
      toast.error(resolveApiErrorMessage(error, t('bots.files.compareLoadFailed')))
    }
    return
  }
  // Without unsaved edits there is nothing to compare: the disk content has
  // just been applied and the notice is gone.
  reconcileText()
  if (conflictState.value === 'none') return
  compareDiskContent.value = snapshot.content
  compareDiskRevision.value = snapshot.revision
  compareStale.value = false
  conflictState.value = 'compare'
}

function exitCompare() {
  compareRequest++
  compareStale.value = false
  conflictState.value = 'chip'
}

// Save on Cmd/Ctrl+S via the shared keyboard layer. Even if a conflict is
// detected handleSave returns false — but we still report `true` to the
// keyboard layer because we've already shown the conflict UI; falling through
// to browser save would be confusing.
useKeyboardCommand(appKeyboardCommands.saveActiveFile, () => {
  if (props.keyboardActive === false) return false
  if (saving.value) return true
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

watch(
  () => props.visible && conflictState.value === 'chip',
  (shouldTick) => {
    if (nowTickInterval) {
      clearInterval(nowTickInterval)
      nowTickInterval = null
    }
    if (shouldTick) {
      nowTick.value = Date.now()
      nowTickInterval = setInterval(() => { nowTick.value = Date.now() }, 60_000)
    }
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  fileGeneration++
  if (nowTickInterval) clearInterval(nowTickInterval)
  nowTickInterval = null
  externalPollController?.abort()
  externalPollController = null
  activeReadController?.abort()
  cleanupImageUrl()
  documentBlob.value = null
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
      <!-- Outside the v-if chain below, and only hidden while Compare is open:
           unmounting the editor would discard its undo history, which Compare
           is always entered with unsaved edits or a pending conflict. -->
      <CodeEditor
        v-if="editorMounted"
        v-show="conflictState !== 'compare'"
        ref="codeEditorRef"
        v-model="content"
        :filename="filename"
        :readonly="readonly"
        :theme="surfaceTheme"
        :theme-type="surfaceThemeType"
        :font-size="surfaceFontSize"
        :line-height="surfaceLineHeight"
        :font-family="surfaceFontFamily"
        class="h-full"
      />

      <!-- Compare view: diff editor replacing the main pane -->
      <div
        v-if="conflictState === 'compare'"
        class="flex h-full flex-col"
      >
        <DiffTitleBar>
          <span class="min-w-0 truncate">
            <!-- Left pane is the disk side (stream-diffs `original`), right is
                 the editor's own text, so the title reads in that order. -->
            <span>{{ t('bots.files.compare.disk') }} ↔ {{ t('bots.files.compare.yours') }}</span>
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
              @click="openCompare"
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
        <CodeDiff
          :original="compareDiskContent"
          :modified="content"
          :filename="filename"
          :theme="surfaceTheme"
          :theme-type="surfaceThemeType"
          :font-size="surfaceFontSize"
          :line-height="surfaceLineHeight"
          :font-family="surfaceFontFamily"
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

      <PanePlaceholder v-else-if="(isImage || isDocument) && diskState === 'deleted'">
        <template #icon>
          <FileX class="size-12 text-destructive opacity-40" />
        </template>
        {{ t(isImage ? 'bots.files.imageDeleted' : 'bots.files.documentDeleted') }}
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

      <DocumentPreview
        v-else-if="isDocument && documentKind && documentBlob"
        :key="`${botId}:${filePath}`"
        :blob="documentBlob"
        :kind="documentKind"
        :filename="filename"
        :active="visible"
      >
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
      </DocumentPreview>

      <PanePlaceholder v-else>
        <template #icon>
          <File class="size-12 opacity-30" />
        </template>
        {{ isDocument && documentOversizedBytes !== null
          ? t('bots.files.previewTooLarge', { size: formatFileSize(documentOversizedBytes) })
          : isDocument && diskState === 'stale'
            ? t('bots.files.previewFailed')
            : t('bots.files.previewNotAvailable') }}
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
