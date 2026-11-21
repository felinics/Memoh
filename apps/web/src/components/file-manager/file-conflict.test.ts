import { describe, expect, it } from 'vitest'
import {
  deriveChipContext,
  detectSaveConflict,
  EMPTY_CHIP_CONTEXT,
  fileMetadataFingerprint,
  resolveChipButtons,
  resolvePolledTextChange,
  resolveSaveBehavior,
  type FsChangeEventLike,
} from './file-conflict'

describe('deriveChipContext', () => {
  it('returns the empty shape (with agentName) when no event is provided', () => {
    expect(deriveChipContext(null, 'Bot')).toEqual({
      ...EMPTY_CHIP_CONTEXT,
      agentName: 'Bot',
    })
  })

  it('trims and normalizes blank agent names to null', () => {
    expect(deriveChipContext(null, '').agentName).toBe(null)
    expect(deriveChipContext(null, '   ').agentName).toBe(null)
    expect(deriveChipContext(null, undefined).agentName).toBe(null)
    expect(deriveChipContext(null, null).agentName).toBe(null)
    expect(deriveChipContext(null, '  My Bot  ').agentName).toBe('My Bot')
  })

  it('reports newLineCount for a write event from its writeContent', () => {
    const event: FsChangeEventLike = {
      at: 1000,
      kind: 'write',
      writeContent: 'line one\nline two\nline three',
    }
    const ctx = deriveChipContext(event, 'Bot')
    expect(ctx.kind).toBe('write')
    expect(ctx.occurredAt).toBe(1000)
    expect(ctx.newLineCount).toBe(3)
    expect(ctx.addedLines).toBe(null)
    expect(ctx.removedLines).toBe(null)
  })

  it('reports a single-line file as newLineCount=1 (no trailing newline)', () => {
    const ctx = deriveChipContext({ at: 1, kind: 'write', writeContent: 'one line' }, 'Bot')
    expect(ctx.newLineCount).toBe(1)
  })

  it('reports newLineCount=0 for an empty write (truncate to empty)', () => {
    const ctx = deriveChipContext({ at: 1, kind: 'write', writeContent: '' }, 'Bot')
    expect(ctx.newLineCount).toBe(0)
  })

  it('reports addedLines/removedLines for an edit event from old/new text', () => {
    const event: FsChangeEventLike = {
      at: 1000,
      kind: 'edit',
      editOldText: 'a\nb',
      editNewText: 'A\nB\nC',
    }
    const ctx = deriveChipContext(event, 'Bot')
    expect(ctx.kind).toBe('edit')
    expect(ctx.addedLines).toBe(3)
    expect(ctx.removedLines).toBe(2)
    expect(ctx.newLineCount).toBe(null)
  })

  it('handles edit events with missing old or new text gracefully', () => {
    const ctx = deriveChipContext({ at: 1, kind: 'edit', editNewText: 'x' }, 'Bot')
    expect(ctx.addedLines).toBe(1)
    expect(ctx.removedLines).toBe(null)
  })
})

describe('detectSaveConflict', () => {
  it('returns false when the latest fs change doesn\'t affect this path', () => {
    expect(detectSaveConflict({
      lastFsChangeAt: 2000,
      lastLoadedAt: 1000,
      affects: false,
    })).toBe(false)
  })

  it('returns false when nothing has ever bumped fsChangedAt', () => {
    expect(detectSaveConflict({
      lastFsChangeAt: 0,
      lastLoadedAt: 1000,
      affects: true,
    })).toBe(false)
  })

  it('returns false when the file has never been loaded (defensive)', () => {
    expect(detectSaveConflict({
      lastFsChangeAt: 2000,
      lastLoadedAt: 0,
      affects: true,
    })).toBe(false)
  })

  it('returns false when the bump happened before our last load', () => {
    expect(detectSaveConflict({
      lastFsChangeAt: 1000,
      lastLoadedAt: 2000,
      affects: true,
    })).toBe(false)
  })

  it('returns false when the bump happened at the same tick as our load', () => {
    expect(detectSaveConflict({
      lastFsChangeAt: 2000,
      lastLoadedAt: 2000,
      affects: true,
    })).toBe(false)
  })

  it('returns true when the bump happened after our last load and affects us', () => {
    expect(detectSaveConflict({
      lastFsChangeAt: 3000,
      lastLoadedAt: 2000,
      affects: true,
    })).toBe(true)
  })
})

describe('resolvePolledTextChange', () => {
  it('ignores unchanged revisions', () => {
    expect(resolvePolledTextChange({
      loaded: true,
      loading: false,
      saving: false,
      currentRevision: 'sha256:a',
      nextRevision: 'sha256:a',
      isDirty: false,
      conflictState: 'none',
    })).toBe('ignore')
  })

  it('applies changed revisions over a clean editor', () => {
    expect(resolvePolledTextChange({
      loaded: true,
      loading: false,
      saving: false,
      currentRevision: 'sha256:a',
      nextRevision: 'sha256:b',
      isDirty: false,
      conflictState: 'none',
    })).toBe('apply')
  })

  it('surfaces the external-change chip for dirty editors', () => {
    expect(resolvePolledTextChange({
      loaded: true,
      loading: false,
      saving: false,
      currentRevision: 'sha256:a',
      nextRevision: 'sha256:b',
      isDirty: true,
      conflictState: 'none',
    })).toBe('show-chip')
  })

  it('marks Compare stale instead of replacing its disk side', () => {
    expect(resolvePolledTextChange({
      loaded: true,
      loading: false,
      saving: false,
      currentRevision: 'sha256:a',
      nextRevision: 'sha256:b',
      isDirty: true,
      conflictState: 'compare',
    })).toBe('mark-compare-stale')
  })

  it('does not poll-apply while initial loading or saving owns the buffer', () => {
    const base = {
      loaded: true,
      loading: false,
      saving: false,
      currentRevision: 'sha256:a',
      nextRevision: 'sha256:b',
      isDirty: false,
      conflictState: 'none' as const,
    }

    expect(resolvePolledTextChange({ ...base, loaded: false })).toBe('ignore')
    expect(resolvePolledTextChange({ ...base, loading: true })).toBe('ignore')
    expect(resolvePolledTextChange({ ...base, saving: true })).toBe('ignore')
  })
})

describe('fileMetadataFingerprint', () => {
  it('changes when file metadata that reflects disk content changes', () => {
    const before = fileMetadataFingerprint({
      path: '/data/app.ts',
      isDir: false,
      size: 10,
      modTime: '2026-06-19T06:00:00Z',
    })

    expect(fileMetadataFingerprint({
      path: '/data/app.ts',
      isDir: false,
      size: 11,
      modTime: '2026-06-19T06:00:00Z',
    })).not.toBe(before)
    expect(fileMetadataFingerprint({
      path: '/data/app.ts',
      isDir: false,
      size: 10,
      modTime: '2026-06-19T06:00:01Z',
    })).not.toBe(before)
  })

  it('keeps file and directory metadata distinct', () => {
    expect(fileMetadataFingerprint({
      path: '/data/build',
      isDir: false,
      size: 0,
      modTime: '2026-06-19T06:00:00Z',
    })).not.toBe(fileMetadataFingerprint({
      path: '/data/build',
      isDir: true,
      size: 0,
      modTime: '2026-06-19T06:00:00Z',
    }))
  })
})

describe('resolveChipButtons', () => {
  it('shows Compare + Reload (no save) when available + clean text', () => {
    const buttons = resolveChipButtons({ diskState: 'available', isText: true, isDirty: false })
    expect(buttons.map(b => b.kind)).toEqual(['compare', 'reload'])
    expect(buttons[1]).toMatchObject({ kind: 'reload', labelKey: 'reload' })
  })

  it('shows Compare + Reload + Save anyway when available + dirty text', () => {
    const buttons = resolveChipButtons({ diskState: 'available', isText: true, isDirty: true })
    expect(buttons.map(b => b.kind)).toEqual(['compare', 'reload', 'forceSave'])
    expect(buttons[2]).toMatchObject({ kind: 'forceSave', labelKey: 'saveAnyway' })
  })

  it('hides Compare for non-text (no diff to render)', () => {
    const buttons = resolveChipButtons({ diskState: 'available', isText: false, isDirty: false })
    expect(buttons.map(b => b.kind)).toEqual(['reload'])
  })

  it('shows only Save to restore when deleted + text (any dirty)', () => {
    const cleanButtons = resolveChipButtons({ diskState: 'deleted', isText: true, isDirty: false })
    expect(cleanButtons).toHaveLength(1)
    expect(cleanButtons[0]).toMatchObject({ kind: 'forceSave', labelKey: 'saveToRestore' })

    const dirtyButtons = resolveChipButtons({ diskState: 'deleted', isText: true, isDirty: true })
    expect(dirtyButtons).toHaveLength(1)
    expect(dirtyButtons[0]).toMatchObject({ kind: 'forceSave', labelKey: 'saveToRestore' })
  })

  it('shows no buttons when deleted + non-text (image — no buffer to write back)', () => {
    expect(resolveChipButtons({ diskState: 'deleted', isText: false, isDirty: false }))
      .toEqual([])
  })

  it('shows Try again + Save anyway when stale + dirty text', () => {
    const buttons = resolveChipButtons({ diskState: 'stale', isText: true, isDirty: true })
    expect(buttons.map(b => b.kind)).toEqual(['reload', 'forceSave'])
    expect(buttons[0]).toMatchObject({ kind: 'reload', labelKey: 'tryAgain' })
    expect(buttons[1]).toMatchObject({ kind: 'forceSave', labelKey: 'saveAnyway' })
  })

  it('shows only Try again when stale + clean (nothing to save)', () => {
    const buttons = resolveChipButtons({ diskState: 'stale', isText: true, isDirty: false })
    expect(buttons.map(b => b.kind)).toEqual(['reload'])
    expect(buttons[0]).toMatchObject({ kind: 'reload', labelKey: 'tryAgain' })
  })

  it('hides Compare in stale state (we don\'t know what is on disk)', () => {
    const buttons = resolveChipButtons({ diskState: 'stale', isText: true, isDirty: false })
    expect(buttons.map(b => b.kind)).not.toContain('compare')
  })

  it('shows only Try again when stale + non-text (no force-save on binaries)', () => {
    const clean = resolveChipButtons({ diskState: 'stale', isText: false, isDirty: false })
    expect(clean.map(b => b.kind)).toEqual(['reload'])
    expect(clean[0]).toMatchObject({ kind: 'reload', labelKey: 'tryAgain' })

    // isDirty on a non-text buffer should not unlock Save anyway — the
    // dirty-flag is essentially unreachable for images, but the contract
    // still has to suppress the action button.
    const dirty = resolveChipButtons({ diskState: 'stale', isText: false, isDirty: true })
    expect(dirty.map(b => b.kind)).toEqual(['reload'])
  })

  it('shows only Reload when available + non-text + dirty (no Save anyway on binaries)', () => {
    const buttons = resolveChipButtons({ diskState: 'available', isText: false, isDirty: true })
    expect(buttons.map(b => b.kind)).toEqual(['reload'])
    expect(buttons[0]).toMatchObject({ kind: 'reload', labelKey: 'reload' })
  })
})

describe('resolveSaveBehavior', () => {
  const base = {
    readonly: false,
    saving: false,
    isDirty: true,
    force: false,
    diskState: 'available' as const,
  }

  it('noop when readonly', () => {
    expect(resolveSaveBehavior({ ...base, readonly: true })).toEqual({
      outcome: 'noop',
      bypassConflictGuard: false,
    })
  })

  it('block when already saving', () => {
    expect(resolveSaveBehavior({ ...base, saving: true })).toEqual({
      outcome: 'block',
      bypassConflictGuard: false,
    })
  })

  it('proceed with guard on the normal dirty-save path', () => {
    expect(resolveSaveBehavior(base)).toEqual({
      outcome: 'proceed',
      bypassConflictGuard: false,
    })
  })

  it('proceed without guard when force=true (explicit Save anyway)', () => {
    expect(resolveSaveBehavior({ ...base, force: true })).toEqual({
      outcome: 'proceed',
      bypassConflictGuard: true,
    })
  })

  it('noop when clean + available + no force (nothing to do)', () => {
    expect(resolveSaveBehavior({ ...base, isDirty: false })).toEqual({
      outcome: 'noop',
      bypassConflictGuard: false,
    })
  })

  it('proceed without guard when diskState=deleted (recreate semantics)', () => {
    // Even with a clean buffer we recreate the file — VS Code's ORPHAN save
    // behavior.
    expect(resolveSaveBehavior({ ...base, isDirty: false, diskState: 'deleted' })).toEqual({
      outcome: 'proceed',
      bypassConflictGuard: true,
    })

    expect(resolveSaveBehavior({ ...base, isDirty: true, diskState: 'deleted' })).toEqual({
      outcome: 'proceed',
      bypassConflictGuard: true,
    })
  })

  it('readonly takes precedence over deleted (cannot write a read-only file even to recreate)', () => {
    expect(resolveSaveBehavior({ ...base, readonly: true, diskState: 'deleted' })).toEqual({
      outcome: 'noop',
      bypassConflictGuard: false,
    })
  })

  it('block takes precedence over deleted (in-flight save still blocks)', () => {
    expect(resolveSaveBehavior({ ...base, saving: true, diskState: 'deleted' })).toEqual({
      outcome: 'block',
      bypassConflictGuard: false,
    })
  })

  it('proceed in stale state when dirty (allow user to push their version)', () => {
    expect(resolveSaveBehavior({ ...base, diskState: 'stale' })).toEqual({
      outcome: 'proceed',
      bypassConflictGuard: false,
    })
    expect(resolveSaveBehavior({ ...base, diskState: 'stale', force: true })).toEqual({
      outcome: 'proceed',
      bypassConflictGuard: true,
    })
  })

  it('noop in stale state when clean (we don\'t recreate, just nothing to do)', () => {
    expect(resolveSaveBehavior({ ...base, isDirty: false, diskState: 'stale' })).toEqual({
      outcome: 'noop',
      bypassConflictGuard: false,
    })
  })
})
