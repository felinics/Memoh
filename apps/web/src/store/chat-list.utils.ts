export function assignInPlace<T extends object>(target: T, source: T): void {
  for (const key of Object.keys(target)) {
    if (!(key in source)) delete (target as Record<string, unknown>)[key]
  }
  Object.assign(target, source)
}

export function upsertById<T extends { id: number }>(items: T[], incoming: T): T[] {
  const existing = items.find(item => item.id === incoming.id)
  if (existing === undefined) {
    items.push(incoming)
    items.sort((a, b) => a.id - b.id)
    return items
  }
  assignInPlace(existing, incoming)
  return items
}

interface ReconcileByIdOptions<T> {
  keyOfExisting?: (item: T) => unknown
  keyOfIncoming?: (item: T) => unknown
  merge?: (current: T, incoming: T) => void
}

export function reconcileById<T extends { id: PropertyKey }>(
  target: T[],
  incoming: T[],
  options: ReconcileByIdOptions<T> = {},
): T[] {
  const keyOfExisting = options.keyOfExisting ?? ((item: T) => item.id)
  const keyOfIncoming = options.keyOfIncoming ?? ((item: T) => item.id)
  const merge = options.merge ?? assignInPlace
  const byKey = new Map<unknown, T>()
  for (const item of target) byKey.set(keyOfExisting(item), item)
  const next = incoming.map((item) => {
    const current = byKey.get(keyOfIncoming(item))
    if (current === undefined) return item
    merge(current, item)
    return current
  })
  target.splice(0, target.length, ...next)
  return target
}

export function sortByRecency<T extends { updated_at?: string; created_at?: string }>(items: T[]): T[] {
  const key = (item: T) => item.updated_at ?? item.created_at ?? ''
  return [...items].sort((a, b) => {
    const ka = key(a)
    const kb = key(b)
    return ka < kb ? 1 : ka > kb ? -1 : 0
  })
}

export type SidebarSessionMode = 'recent' | 'schedule' | 'agent'

interface SessionDescriptorShape {
  type?: string
  session_mode?: string
  runtime_type?: string
}

export function normalizedSessionMode(session: SessionDescriptorShape): string {
  const mode = (session.session_mode ?? '').trim()
  if (mode) return mode
  const legacyType = (session.type ?? '').trim()
  if (legacyType === 'acp_agent') return 'chat'
  return legacyType || 'chat'
}

export function normalizedRuntimeType(session: SessionDescriptorShape): string {
  const runtimeType = (session.runtime_type ?? '').trim()
  if (runtimeType) return runtimeType
  return (session.type ?? '').trim() === 'acp_agent' ? 'acp_agent' : 'model'
}

// Default visible label for a channel-bound session that has no title: a group
// shows its conversation (group) name, a DM the peer's display name. Discuss
// sessions never get a title generated server-side (only the chat turn path
// runs maybeGenerateSessionTitle), so without this fallback they would pile up
// in Recents as indistinguishable "Untitled Session" rows. The name comes from
// the channel route metadata that EnrichThreads projects onto session list
// responses; every render surface (sidebar row, workspace tab, session picker)
// funnels through here so the fallback chain stays identical.
export function routeConversationLabel(session: { route_metadata?: Record<string, unknown> | null } | null | undefined): string {
  const meta = session?.route_metadata ?? {}
  return (meta.conversation_name as string ?? '').trim()
    || (meta.sender_display_name as string ?? '').trim()
    || (meta.sender_username as string ?? '').trim()
    || ''
}

export function isSessionVisibleInSidebarMode(session: SessionDescriptorShape, mode: SidebarSessionMode): boolean {  switch (mode) {
    case 'recent': {
      const sessionMode = normalizedSessionMode(session)
      return sessionMode === 'chat' || sessionMode === 'discuss'
    }
    case 'schedule':
      return normalizedSessionMode(session) === 'schedule'
    case 'agent':
      return normalizedRuntimeType(session) === 'acp_agent'
    default:
      return false
  }
}

export function latestOutputLine(tail?: string): string {
  if (!tail) return ''
  for (const line of tail.split('\n').reverse()) {
    for (const segment of line.split('\r').reverse()) {
      const candidate = segment.trim()
      if (candidate) return candidate
    }
  }
  return ''
}

export type TurnSegment<T> =
  | { kind: 'rail'; key: string; blocks: T[] }
  | { kind: 'flow'; key: string; block: T }

const PROCESS_BLOCK_TYPES = new Set(['reasoning', 'tool'])

// Group a turn's blocks into segments by their immutable `type`: maximal runs of
// process blocks (reasoning/tool) become one recessed "rail"; text/error/attachment
// blocks break out as standalone "flow" segments. Keying by the segment's first
// block id keeps every segment stable as the turn streams (blocks only append at
// the tail), so no block ever reparents — which is what prevents remount/stall.
export function segmentTurnBlocks<T extends { id: number; type: string }>(blocks: T[]): TurnSegment<T>[] {
  const segments: TurnSegment<T>[] = []
  let rail: { kind: 'rail'; key: string; blocks: T[] } | null = null
  for (const block of blocks) {
    if (PROCESS_BLOCK_TYPES.has(block.type)) {
      if (rail === null) {
        rail = { kind: 'rail', key: `rail:${block.id}`, blocks: [] }
        segments.push(rail)
      }
      rail.blocks.push(block)
    } else {
      rail = null
      segments.push({ kind: 'flow', key: `flow:${block.id}`, block })
    }
  }
  return segments
}

export type RailItem<T> =
  | { kind: 'block'; key: string; block: T }
  | { kind: 'cluster'; key: string; tools: T[] }

interface FoldableToolShape {
  type: string
  done?: boolean
  approval?: { status?: string } | null
  userInput?: { status?: string } | null
  backgroundTask?: { status?: string } | null
}

// A settled tool folds into a cluster only if it needs nothing further and is
// no longer live. A tool awaiting approval/user input must stay solo so its
// inline controls aren't buried in a collapsed cluster; a tool with a running
// background task must stay solo so its live status line stays visible.
function isFoldableTool(block: FoldableToolShape): boolean {
  if (block.type !== 'tool' || block.done !== true) return false
  if (block.approval?.status === 'pending' || block.userInput?.status === 'pending') return false
  const bgStatus = (block.backgroundTask?.status ?? '').trim().toLowerCase()
  if (bgStatus === 'running' || bgStatus === 'stalled') return false
  return true
}

// Fold maximal runs of >=2 consecutive *settled* tool calls into a single
// cluster; reasoning blocks, in-progress tools, and tools awaiting interaction
// always render solo (and break a run). When `keepOpen` is set (the turn is
// still streaming) nothing folds — every tool renders solo — so streaming never
// reparents a tool into a cluster (which would remount it and reintroduce the
// stall). Runs fold only once the turn has settled.
export function clusterRailBlocks<T extends FoldableToolShape & { id: number }>(
  blocks: T[],
  keepOpen = false,
): RailItem<T>[] {
  const items: RailItem<T>[] = []
  let run: T[] = []

  const flush = () => {
    if (run.length === 0) return
    if (keepOpen || run.length < 2) {
      for (const tool of run) items.push({ kind: 'block', key: `block:${tool.id}`, block: tool })
    } else {
      items.push({ kind: 'cluster', key: `cluster:${run[0]!.id}`, tools: run })
    }
    run = []
  }

  for (const block of blocks) {
    if (isFoldableTool(block)) {
      run.push(block)
    } else {
      flush()
      items.push({ kind: 'block', key: `block:${block.id}`, block })
    }
  }
  flush()
  return items
}

export function distinctToolNames<T extends { toolName?: string }>(tools: T[]): string[] {
  const seen = new Set<string>()
  const names: string[] = []
  for (const tool of tools) {
    const name = tool.toolName ?? ''
    if (name && !seen.has(name)) {
      seen.add(name)
      names.push(name)
    }
  }
  return names
}

function isUnsettledRailBlock(block: FoldableToolShape): boolean {
  if (block.type !== 'tool') return false
  if (block.done !== true) return true
  if (block.approval?.status === 'pending' || block.userInput?.status === 'pending') return true
  const bg = (block.backgroundTask?.status ?? '').trim().toLowerCase()
  return bg === 'running' || bg === 'stalled'
}

// Once a turn settles, an interleaved thinking↔tool rail can be collapsed whole
// into a single summary line (clustering only consecutive tools can't tame an
// alternating trace). Only collapse a segment of >=2 blocks where nothing still
// needs attention or is live — those must stay visible.
export function canSummarizeRailSegment<T extends FoldableToolShape & { id: number }>(blocks: T[]): boolean {
  if (blocks.length < 2) return false
  return !blocks.some(isUnsettledRailBlock)
}

// Tally a settled rail segment for its one-line summary: how many thinking steps,
// how many tool calls, and the distinct tool names (first-seen order) for icons.
export function summarizeRailSegment<T extends { type: string; toolName?: string }>(blocks: T[]): {
  thinkingCount: number
  toolCount: number
  toolNames: string[]
} {
  let thinkingCount = 0
  let toolCount = 0
  const seen = new Set<string>()
  const toolNames: string[] = []
  for (const block of blocks) {
    if (block.type === 'reasoning') {
      thinkingCount++
    } else if (block.type === 'tool') {
      toolCount++
      const name = block.toolName ?? ''
      if (name && !seen.has(name)) {
        seen.add(name)
        toolNames.push(name)
      }
    }
  }
  return { thinkingCount, toolCount, toolNames }
}

// A tool whose background task is still running keeps a live status row that
// must never be hidden — so a rail segment containing one is rendered fully
// (no rolling / prior-chip collapse) while the turn streams.
export function segmentHasLiveBg<T extends { type: string; backgroundTask?: { status?: string } | null }>(blocks: T[]): boolean {
  return blocks.some((block) => {
    if (block.type !== 'tool') return false
    const status = (block.backgroundTask?.status ?? '').trim().toLowerCase()
    return status === 'running' || status === 'stalled'
  })
}

export interface RailGroup<T> {
  kind: 'think' | 'tools'
  blocks: T[]
}

// While a turn streams, the active rail shows only its *current* phase live:
// the trailing thinking block, or the trailing tool run (rolled through one
// slot). Everything before it ("prior") collapses to a summary chip. Group the
// segment into consecutive think / tool-run groups and split off the last one.
export function splitActiveRail<T extends { type: string }>(blocks: T[]): {
  prior: T[]
  current: RailGroup<T> | null
} {
  const groups: RailGroup<T>[] = []
  for (const block of blocks) {
    if (block.type === 'reasoning') {
      groups.push({ kind: 'think', blocks: [block] })
    } else {
      const last = groups[groups.length - 1]
      if (last && last.kind === 'tools') last.blocks.push(block)
      else groups.push({ kind: 'tools', blocks: [block] })
    }
  }
  if (groups.length === 0) return { prior: [], current: null }
  const prior: T[] = []
  for (let i = 0; i < groups.length - 1; i++) prior.push(...groups[i]!.blocks)
  return { prior, current: groups[groups.length - 1]! }
}

export interface BgTaskBeacon {
  taskId: string
  phase: 'active' | 'done'
  visible: boolean
  latestLine: string
}

export interface BgTaskPill {
  tone: 'running' | 'done'
  count: number
  latestLine: string
}

// Decide the floating "tasks running" pill from the set of tracked background
// tasks: only off-screen tasks justify a pill (an on-screen one is already
// visible). Running tasks win; a recently-completed off-screen task shows a
// brief done pill instead.
export function computeBgTaskPill(beacons: BgTaskBeacon[]): BgTaskPill | null {
  const offscreen = beacons.filter(beacon => !beacon.visible)
  const running = offscreen.filter(beacon => beacon.phase === 'active')
  if (running.length > 0) {
    return { tone: 'running', count: running.length, latestLine: running[running.length - 1]!.latestLine }
  }
  const done = offscreen.filter(beacon => beacon.phase === 'done')
  if (done.length > 0) {
    return { tone: 'done', count: done.length, latestLine: '' }
  }
  return null
}

// Lightweight markdown→text for an inline preview: drop fenced code, unwrap
// inline code / links / images, strip heading / blockquote / list markers and
// emphasis, flatten table pipes. Not a full parser — just enough to keep a
// one-line peek free of raw structural markers (single `_` is left alone so
// snake_case identifiers survive).
function stripMarkdown(md: string): string {
  return md
    .replace(/```[\s\S]*?```/g, ' ')
    .replace(/`([^`]+)`/g, '$1')
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/^\s{0,3}#{1,6}\s+/gm, '')
    .replace(/^\s*>+\s?/gm, '')
    .replace(/^\s*[-*+]\s+/gm, '')
    .replace(/^\s*\d+\.\s+/gm, '')
    .replace(/(\*\*|\*|__|~~)/g, '')
    .replace(/\|/g, ' ')
}

// Pick the calm one-line "thinking peek": the latest COMPLETE sentence of the
// reasoning as plain semantic text. While a new sentence is mid-write we keep
// showing the last finished one (no hard mid-word cut, no raw markdown); only
// before any sentence completes do we show the in-progress fragment. This is
// what makes the peek read as comfortable prose instead of a truncated,
// marker-laden raw line.
export function thinkingPeek(content?: string): string {
  if (!content) return ''
  const segments = stripMarkdown(content)
    .split(/(?<=[.!?。！？])\s+|\n+/)
    .map(segment => segment.replace(/\s+/g, ' ').trim())
    .filter(Boolean)
  if (segments.length === 0) return ''
  for (let i = segments.length - 1; i >= 0; i--) {
    if (/[.!?。！？]$/.test(segments[i]!)) return segments[i]!
  }
  return segments[segments.length - 1]!
}

// Provisional title shown in the sidebar before the server's title model has
// generated the real title. Falls back to the truncated first line of the
// prompt instead of "Untitled". Strips markdown across the whole prompt
// (so a complete code fence is dropped, not just its opening line), takes the
// first remaining line, collapses whitespace, and caps at 50 runes.
const provisionalTitleMaxRunes = 50

const markdownPatterns = [
  { re: /```[^]*?```/g, replace: ' ' },
  { re: /`([^`]+)`/g, replace: '$1' },
  { re: /!\[([^\]]*)\]\([^)]*\)/g, replace: '$1' },
  { re: /\[([^\]]+)\]\([^)]*\)/g, replace: '$1' },
  { re: /^\s{0,3}#{1,6}\s+/gm, replace: '' },
  { re: /^\s*>+\s?/gm, replace: '' },
  { re: /^\s*[-*+]\s+/gm, replace: '' },
  { re: /^\s*\d+\.\s+/gm, replace: '' },
  { re: /(\*\*|\*|__|~~)/g, replace: '' },
  { re: /\|/g, replace: ' ' },
]

function stripMarkdownForTitle(md: string): string {
  let text = md
  for (const { re, replace } of markdownPatterns) {
    text = text.replace(re, replace)
  }
  return text
}

export function provisionalSessionTitle(userQuery: string): string {
  const stripped = stripMarkdownForTitle(userQuery).trim()
  if (!stripped) return ''
  const firstLine = stripped.split('\n')[0]!.replace(/\s+/g, ' ').trim()
  if (!firstLine) return ''
  const runes = Array.from(firstLine)
  if (runes.length <= provisionalTitleMaxRunes) return firstLine
  return `${runes.slice(0, provisionalTitleMaxRunes).join('')}…`
}
