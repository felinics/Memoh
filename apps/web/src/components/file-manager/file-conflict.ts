// Pure helpers that back the file-viewer's "agent updated this file" chip and
// its save-baseline guard. Extracted so the corner cases (kind detection, line
// counts, save-conflict detection across debounce/load timing) can be tested
// without spinning up a Vue component.

export type FsToolKind = 'write' | 'edit' | 'apply_patch' | 'exec'

export interface FsChangeEventLike {
  at: number
  path?: string
  kind: FsToolKind
  toolCallId?: string
  sessionId?: string
  writeContent?: string
  editOldText?: string
  editNewText?: string
}

export interface ChipContext {
  agentName: string | null
  kind: FsToolKind | null
  occurredAt: number | null
  // For write: total line count of the new content.
  newLineCount: number | null
  // For edit: line counts in the old/new snippet so we can show +N / -M.
  addedLines: number | null
  removedLines: number | null
}

export const EMPTY_CHIP_CONTEXT: ChipContext = {
  agentName: null,
  kind: null,
  occurredAt: null,
  newLineCount: null,
  addedLines: null,
  removedLines: null,
}

function countLines(text: string | undefined | null): number | null {
  if (typeof text !== 'string') return null
  if (text === '') return 0
  return text.split('\n').length
}

export function deriveChipContext(
  event: FsChangeEventLike | null,
  agentName: string | null | undefined,
): ChipContext {
  const name = typeof agentName === 'string' && agentName.trim() !== ''
    ? agentName.trim()
    : null
  if (!event) {
    return { ...EMPTY_CHIP_CONTEXT, agentName: name }
  }
  return {
    agentName: name,
    kind: event.kind,
    occurredAt: event.at,
    newLineCount: countLines(event.writeContent),
    addedLines: countLines(event.editNewText),
    removedLines: countLines(event.editOldText),
  }
}

export interface SaveConflictArgs {
  // chatStore.fsChangedAt — last bumped timestamp (0 if never bumped).
  lastFsChangeAt: number
  // viewer-local: timestamp when the current content was last loaded from
  // server. 0 before the first successful load.
  lastLoadedAt: number
  // chatStore.affectsPath(filePath): whether the latest batch covers this
  // viewer's path (paths set hit OR wildcard).
  affects: boolean
}

// Returns true when the user pressing Save would overwrite a workspace change
// that arrived after we last fetched. Matches VS Code's pre-save mtime check.
export function detectSaveConflict(a: SaveConflictArgs): boolean {
  if (!a.affects) return false
  if (a.lastFsChangeAt <= 0) return false
  if (a.lastLoadedAt <= 0) return false
  return a.lastFsChangeAt > a.lastLoadedAt
}

export type ConflictState = 'none' | 'chip' | 'compare'

export type PolledTextChangeAction =
  | 'ignore'
  | 'apply'
  | 'show-chip'
  | 'mark-compare-stale'

export interface ResolvePolledTextChangeArgs {
  loaded: boolean
  loading: boolean
  saving: boolean
  currentRevision: string | null | undefined
  nextRevision: string | null | undefined
  isDirty: boolean
  conflictState: ConflictState
}

export function resolvePolledTextChange(a: ResolvePolledTextChangeArgs): PolledTextChangeAction {
  const currentRevision = a.currentRevision?.trim() ?? ''
  const nextRevision = a.nextRevision?.trim() ?? ''
  if (!a.loaded || a.loading || a.saving || !nextRevision) return 'ignore'
  if (currentRevision === nextRevision) return 'ignore'
  if (a.conflictState === 'compare') return 'mark-compare-stale'
  if (a.isDirty) return 'show-chip'
  return 'apply'
}

export interface FileMetadataLike {
  path?: string | null
  isDir?: boolean | null
  size?: number | null
  modTime?: string | null
}

export function fileMetadataFingerprint(info: FileMetadataLike): string {
  return [
    info.path ?? '',
    info.isDir ? 'dir' : 'file',
    info.size ?? -1,
    info.modTime ?? '',
  ].join('\u0000')
}

export type DiskState = 'available' | 'stale' | 'deleted'

export type ChipButton =
  | { kind: 'compare' }
  | { kind: 'reload'; labelKey: 'reload' | 'tryAgain' }
  | { kind: 'forceSave'; labelKey: 'saveAnyway' | 'saveToRestore' }

export interface ResolveChipButtonsArgs {
  diskState: DiskState
  isText: boolean
  isDirty: boolean
}

// Decides the chip's action buttons for a given conflict surface. Mirrors VS
// Code's ORPHAN UX: when the file is gone we hide Reload (it would just 404
// again) and Compare (nothing to diff against), and re-label Save as
// "Save to restore" — POSTing the buffer recreates the file.
export function resolveChipButtons(a: ResolveChipButtonsArgs): ChipButton[] {
  const buttons: ChipButton[] = []
  if (a.diskState === 'deleted') {
    // Restore is available for both clean and dirty buffers: a clean buffer
    // still represents the last-known content, and recreating it from there is
    // usually what the user wants.
    if (a.isText) buttons.push({ kind: 'forceSave', labelKey: 'saveToRestore' })
    return buttons
  }
  if (a.diskState === 'stale') {
    buttons.push({ kind: 'reload', labelKey: 'tryAgain' })
    if (a.isText && a.isDirty) buttons.push({ kind: 'forceSave', labelKey: 'saveAnyway' })
    return buttons
  }
  // available
  if (a.isText) buttons.push({ kind: 'compare' })
  buttons.push({ kind: 'reload', labelKey: 'reload' })
  if (a.isText && a.isDirty) buttons.push({ kind: 'forceSave', labelKey: 'saveAnyway' })
  return buttons
}

export interface ResolveSaveBehaviorArgs {
  readonly: boolean
  saving: boolean
  isDirty: boolean
  force: boolean
  diskState: DiskState
}

export interface SaveBehaviorPlan {
  // 'noop' — return true without touching the network (read-only, or clean and
  //   the file still exists). 'block' — currently saving, refuse. 'proceed' —
  //   actually POST.
  outcome: 'noop' | 'block' | 'proceed'
  // True when the POST should skip the conflict guard (and the expectedRevision
  // header). Implied by user-explicit "Save anyway" / "Save to restore", or by
  // the deleted state (no concurrent disk version to race against).
  bypassConflictGuard: boolean
}

// Single source of truth for the save state machine; lets us cover the ORPHAN
// recreate path (deleted + clean) and the explicit force path with the same
// table of decisions instead of nested if blocks in handleSave.
export function resolveSaveBehavior(a: ResolveSaveBehaviorArgs): SaveBehaviorPlan {
  if (a.readonly) return { outcome: 'noop', bypassConflictGuard: false }
  if (a.saving) return { outcome: 'block', bypassConflictGuard: false }
  if (a.diskState === 'deleted') {
    // Even a clean buffer warrants a POST: that's how we resurrect the file.
    // Skip the conflict guard since there's no concurrent disk version.
    return { outcome: 'proceed', bypassConflictGuard: true }
  }
  if (!a.isDirty) return { outcome: 'noop', bypassConflictGuard: false }
  return { outcome: 'proceed', bypassConflictGuard: a.force }
}
