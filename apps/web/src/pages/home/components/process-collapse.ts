// Open/closed state for chain-of-process collapsibles (process groups, single
// tool detail rows, thinking blocks).
//
// The whole assistant turn is re-fetched and re-mounted once it finishes
// streaming, which would otherwise discard any expand/collapse the user did
// mid-stream (the classic "I opened it, then the turn ended and it snapped
// shut"). We keep the toggle here, keyed by a *stable* signature of the block
// (backend tool_call_id, or a content hash for reasoning) so the re-mounted
// "done" component recovers exactly what the user left open.
//
// Semantics: purely user-driven. Nothing auto-opens on stream start or
// auto-closes on completion — a CoP is collapsed until the user opens it, and
// then stays however they left it for the life of the session. Cross-reload it
// resets to collapsed (acceptable; matches "reduce info, focus on output").
const openState = new Map<string, boolean>()

export function getCollapseOpen(key: string): boolean {
  return key ? openState.get(key) ?? false : false
}

export function setCollapseOpen(key: string, open: boolean): void {
  if (key) openState.set(key, open)
}

export function migrateCollapseOpen(previousKey: string, key: string, open: boolean): void {
  if (previousKey && previousKey !== key) openState.delete(previousKey)
  setCollapseOpen(key, open)
}

function hash(value: string): string {
  let h = 0
  for (let i = 0; i < value.length; i += 1) {
    h = (h * 31 + value.charCodeAt(i)) | 0
  }
  return `${value.length}:${h}`
}

interface KeyableBlock {
  type: string
  id: number
  toolCallId?: string
  content?: string
}

// A block's stable identity across the stream→refetch boundary.
function blockSignature(block: KeyableBlock): string {
  if (block.type === 'tool') return `t:${block.toolCallId || block.id}`
  if (block.type === 'reasoning') return `r:${hash((block.content ?? '').trim())}`
  return `b:${block.id}`
}

export function toolCollapseKey(block: KeyableBlock): string {
  return blockSignature(block)
}

export function reasoningCollapseKey(content: string): string {
  return `r:${hash((content ?? '').trim())}`
}

// A group is identified by its first item — once a group has >= 2 items the
// first item is complete (a later one exists), so its signature is stable.
export function groupCollapseKey(items: KeyableBlock[]): string {
  const first = items[0]
  return first ? `g/${blockSignature(first)}` : ''
}
