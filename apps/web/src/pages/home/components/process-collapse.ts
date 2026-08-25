import { processBlockKey } from './process-block-key'

// Open/closed state for chain-of-process collapsibles (process groups, single
// tool detail rows, thinking blocks).
//
// The whole assistant turn is re-fetched and re-mounted once it finishes
// streaming, which would otherwise discard any expand/collapse the user did
// mid-stream (the classic "I opened it, then the turn ended and it snapped
// shut"). We keep the toggle here, keyed by a *stable* signature of the block
// (assistant render id + backend block id) so the re-mounted "done" component
// recovers exactly what the user left open without confusing mutable content
// for identity.
//
// Semantics: purely user-driven. Nothing auto-opens on stream start or
// auto-closes on completion — a CoP is collapsed until the user opens it, and
// then stays however they left it for the life of the session. Cross-reload it
// resets to collapsed (acceptable; matches "reduce info, focus on output").
const MAX_OPEN_STATES = 2048
// This is session-only UI state, so keep recent toggles without growing for
// the full lifetime of a long-running desktop renderer.
const openState = new Map<string, boolean>()

export function getCollapseOpen(key: string): boolean | undefined {
  return key ? openState.get(key) : undefined
}

export function setCollapseOpen(key: string, open: boolean): void {
  if (!key) return
  openState.delete(key)
  openState.set(key, open)
  while (openState.size > MAX_OPEN_STATES) {
    const oldest = openState.keys().next().value
    if (oldest === undefined) break
    openState.delete(oldest)
  }
}

interface KeyableBlock {
  type: string
  id: number
}

export function toolCollapseKey(messageId: string, block: KeyableBlock): string {
  const key = processBlockKey(messageId, block)
  return key ? `tool/${key}` : ''
}

export function reasoningCollapseKey(messageId: string, block: KeyableBlock): string {
  const key = processBlockKey(messageId, block)
  return key ? `reasoning/${key}` : ''
}

// A group is identified by its first item. Its backend id is stable even while
// reasoning content and tool payloads continue to grow.
export function groupCollapseKey(messageId: string, items: KeyableBlock[]): string {
  const first = items[0]
  if (!first) return ''
  const key = processBlockKey(messageId, first)
  return key ? `group/${key}` : ''
}
