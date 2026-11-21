import { isDirectBotAgentRuntime, type DirectBotAgentRuntime } from '@/utils/bot-agent'

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

export function sortByRecency<T extends { updated_at?: string; created_at?: string }>(items: T[]): T[] {
  const key = (item: T) => item.updated_at ?? item.created_at ?? ''
  return [...items].sort((a, b) => {
    const ka = key(a)
    const kb = key(b)
    return ka < kb ? 1 : ka > kb ? -1 : 0
  })
}

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

export function isAgentRuntimeType(runtimeType: string | undefined): runtimeType is 'acp_agent' | DirectBotAgentRuntime {
  return runtimeType === 'acp_agent' || isDirectBotAgentRuntime(runtimeType)
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
