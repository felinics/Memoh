import { ref, type Ref } from 'vue'
import type { UIMessage } from '@/composables/api/useChat'

// fs-change beacon — the store-side signal that tells file-manager surfaces
// "an agent just mutated the workspace filesystem, refresh yourself."
//
// Extracted from the chat store as a plain composable factory (deps in,
// surface out, no Pinia): the chat store instantiates one and spreads its
// public surface into the store return, so consumers keep reading
// `chatStore.fsChangedAt` etc. unchanged.

export interface FsChangeBatch {
  at: number
  // null = unknown / wildcard (exec completion, manual refresh, user-driven mutation)
  paths: ReadonlySet<string> | null
}

export type FsToolKind = 'write' | 'edit' | 'apply_patch' | 'exec'

// Rich metadata for fs-mutating tool calls that landed on a known absolute
// path. Stored per-path so the file viewer can show context (which agent, when,
// what was written) and so the Compare flow can diff against the agent's
// content without an extra round-trip.
export interface FsChangeEvent {
  at: number
  path: string
  kind: FsToolKind
  toolCallId: string
  sessionId: string
  writeContent?: string
  editOldText?: string
  editNewText?: string
}

export interface FsChangeBeaconDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
}

// Routes a server-pushed fs_changed payload into the beacon: null/undefined
// is a wildcard ("something changed, scope unknown"), a non-empty list marks
// each touched path, and an empty list is a no-op (the server never sends
// one; treat it as noise rather than a full refresh).
export function applyFsChangedPaths(
  markFsChanged: (path?: string | null) => void,
  paths: readonly string[] | null | undefined,
) {
  if (paths == null) {
    markFsChanged()
    return
  }
  for (const path of paths) {
    markFsChanged(path)
  }
}

export function createFsChangeBeacon({ currentBotId, sessionId }: FsChangeBeaconDeps) {
  // Bumps every time a fs-mutating tool call (write/edit/apply_patch/exec) finishes for the
  // current bot. File-manager components watch this to refresh their listings
  // and any open file viewers without polling. Trailing fixed-delay throttle so
  // a burst of edits within one window collapses into one refresh. Each batch
  // carries the set of paths touched in that window (or null = wildcard, for
  // exec and other unknown-impact triggers) so consumers can filter by path.
  const fsChangedAt = ref(0)
  const lastFsChange = ref<FsChangeBatch | null>(null)
  // Most recent rich event per absolute path. Powers the file-viewer chip's
  // "who did what" context and the Compare view's diff baseline. Wildcard
  // events (exec / apply_patch / relative paths) are intentionally absent —
  // those still fire fsChangedAt but contribute no per-path metadata.
  const lastFsEvents = ref<Map<string, FsChangeEvent>>(new Map())
  const FS_MUTATING_TOOLS = new Set(['write', 'edit', 'apply_patch', 'exec'])
  const FS_CHANGED_DEBOUNCE_MS = 150
  let fsChangedBumpTimer: ReturnType<typeof setTimeout> | null = null
  let pendingFsPaths: Set<string> | null = new Set()
  let pendingFsEvents = new Map<string, FsChangeEvent>()
  // Bot at the moment the in-flight batch started. If currentBotId changes
  // before the timer fires, the batch belongs to the old bot and we drop it
  // rather than leak it into the new bot's UI.
  let pendingFsBotId: string | null = null
  // Tool calls we've already bumped (or seen at load time) for the current
  // bot. Prevents double-bumping when a tool first arrives via the WS stream
  // and then re-appears via the stream-end / message_created refresh path.
  const seenFsToolCallIds = new Set<string>()

  function markFsChanged(path?: string | null) {
    if (path === undefined || path === null) {
      pendingFsPaths = null
    } else if (pendingFsPaths !== null) {
      pendingFsPaths.add(path)
    }
    if (fsChangedBumpTimer != null) return
    pendingFsBotId = currentBotId.value
    fsChangedBumpTimer = setTimeout(() => {
      fsChangedBumpTimer = null
      const recordedBotId = pendingFsBotId
      const paths = pendingFsPaths
      const events = pendingFsEvents
      pendingFsBotId = null
      pendingFsPaths = new Set()
      pendingFsEvents = new Map()
      if (recordedBotId !== currentBotId.value) return
      const at = Date.now()
      lastFsChange.value = { at, paths }
      fsChangedAt.value = at
      if (events.size > 0) {
        const next = new Map(lastFsEvents.value)
        for (const [p, ev] of events) next.set(p, ev)
        lastFsEvents.value = next
      }
    }, FS_CHANGED_DEBOUNCE_MS)
  }

  function cancelPendingFsBump() {
    if (fsChangedBumpTimer != null) {
      clearTimeout(fsChangedBumpTimer)
      fsChangedBumpTimer = null
    }
    pendingFsPaths = new Set()
    pendingFsEvents = new Map()
    pendingFsBotId = null
  }

  function affectsPath(path: string): boolean {
    const change = lastFsChange.value
    if (!change) return false
    if (change.paths === null) return true
    return change.paths.has(path)
  }

  function fsEventForPath(path: string): FsChangeEvent | null {
    return lastFsEvents.value.get(path) ?? null
  }

  function extractToolMessagePath(message: UIMessage): string | null {
    if (message.type !== 'tool') return null
    const input = message.input
    if (typeof input !== 'object' || input === null) return null
    const path = (input as Record<string, unknown>).path
    if (typeof path !== 'string' || !path) return null
    // Only emit absolute paths as path-targeted hints. Viewer filePaths are
    // always absolute (the FS list API normalizes them); a relative path here
    // can't be safely compared without knowing the agent's cwd, so fall through
    // to wildcard and let every viewer decide whether to refresh.
    if (!path.startsWith('/')) return null
    return path
  }

  function buildFsChangeEvent(message: UIMessage, path: string, callId: string): FsChangeEvent | null {
    if (message.type !== 'tool') return null
    const input = message.input
    const event: FsChangeEvent = {
      at: Date.now(),
      path,
      kind: message.name as FsToolKind,
      toolCallId: callId,
      sessionId: (sessionId.value ?? '').trim(),
    }
    if (typeof input === 'object' && input !== null) {
      const rec = input as Record<string, unknown>
      if (message.name === 'write' && typeof rec.content === 'string') {
        event.writeContent = rec.content
      } else if (message.name === 'edit') {
        if (typeof rec.old_text === 'string') event.editOldText = rec.old_text
        if (typeof rec.new_text === 'string') event.editNewText = rec.new_text
      }
    }
    return event
  }

  function bumpFsChangedAtIfFsMutation(message: UIMessage) {
    if (message.type !== 'tool') return
    if (message.running) return
    if (!FS_MUTATING_TOOLS.has(message.name)) return
    const callId = message.tool_call_id?.trim() ?? ''
    if (callId && seenFsToolCallIds.has(callId)) return
    if (callId) seenFsToolCallIds.add(callId)
    // write / edit carry their target `path` in input. apply_patch can target
    // many files (multi-path parsing belongs to the view layer, not the store)
    // and exec is opaque — both fall back to wildcard.
    const path = (message.name === 'write' || message.name === 'edit')
      ? extractToolMessagePath(message)
      : null
    if (path) {
      const event = buildFsChangeEvent(message, path, callId)
      if (event) pendingFsEvents.set(path, event)
    }
    markFsChanged(path)
  }

  // Full reset for auth-clear / user-scope teardown: everything goes,
  // including the fsChangedAt watermark.
  function resetFsBeacon() {
    cancelPendingFsBump()
    fsChangedAt.value = 0
    lastFsChange.value = null
    lastFsEvents.value = new Map()
    seenFsToolCallIds.clear()
  }

  // Bot-switch clear: drops batches/events/seen-ids but deliberately does NOT
  // zero fsChangedAt — consumers watch it for *changes*, and rewinding the
  // watermark on a mere bot switch could re-trigger stale comparisons. This
  // asymmetry mirrors the original inline clears in the chat store.
  function clearFsForBotSwitch() {
    cancelPendingFsBump()
    lastFsChange.value = null
    lastFsEvents.value = new Map()
    seenFsToolCallIds.clear()
  }

  return {
    fsChangedAt,
    lastFsChange,
    lastFsEvents,
    markFsChanged,
    cancelPendingFsBump,
    affectsPath,
    fsEventForPath,
    bumpFsChangedAtIfFsMutation,
    resetFsBeacon,
    clearFsForBotSwitch,
  }
}
