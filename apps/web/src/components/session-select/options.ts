import type { SessionSession } from '@memohai/sdk'
import type { SearchableSelectOption } from '@/components/searchable-select-popover/index.vue'
import type { BotWorkdir } from '@/composables/api/useWorkdirs'
import type { SessionKind } from './session-kind-icon.vue'
import { acpAgentDisplayName, normalizeACPAgentID } from '@/utils/acp'
import { normalizedRuntimeType, normalizedSessionMode, routeConversationLabel } from '@/store/chat-list.utils'

// The per-row mark the picker paints beside a session title.
export interface SessionMark {
  kind: SessionKind
  agentId: string
  label: string
}

// Copy the picker needs but cannot look up itself — kept as data so the
// grouping stays a pure function.
export interface SessionSelectLabels {
  untitled: string
  recents: string
  unavailableFolder: string
  schedule: string
  agent: string
}

export const NO_MARK: SessionMark = { kind: 'chat', agentId: '', label: '' }

export function sessionTitle(session: SessionSession, labels: SessionSelectLabels): string {
  return (session.title ?? '').trim() || routeConversationLabel(session) || labels.untitled
}

export function sessionMark(session: SessionSession, labels: SessionSelectLabels): SessionMark {
  const agentId = normalizeACPAgentID(
    session.runtime_metadata?.acp_agent_id ?? session.metadata?.acp_agent_id,
  )
  // Schedule wins over the ACP mark here, unlike the sidebar row: this picker
  // exists to tell schedule-owned sessions apart from ordinary chats, and a
  // schedule session that happens to run an agent is still a schedule session.
  if (normalizedSessionMode(session) === 'schedule') {
    return { kind: 'schedule', agentId, label: labels.schedule }
  }
  if (normalizedRuntimeType(session) === 'acp_agent') {
    return { kind: 'acp', agentId, label: acpAgentDisplayName(agentId, labels.agent) }
  }
  return NO_MARK
}

// buildSessionOptions groups a bot's sessions the way the sidebar lists them:
// unfiled sessions first — headerless while there is nothing to contrast them
// with — then one group per live folder, in the folder list's own order. A
// session bound to a folder that is gone (archived, deleted) keeps its own
// group rather than silently joining the unfiled bucket.
export function buildSessionOptions(
  sessions: SessionSession[],
  workdirs: BotWorkdir[],
  labels: SessionSelectLabels,
): SearchableSelectOption[] {
  const nameById = new Map(workdirs.flatMap(wd => (wd.id ? [[wd.id, wd.name ?? '']] : [])))
  const buckets = new Map<string, SessionSession[]>([['', []]])
  for (const workdir of workdirs) {
    if (workdir.id && !workdir.archived) buckets.set(workdir.id, [])
  }
  for (const session of sessions) {
    const key = (session.workdir_id ?? '').trim()
    const bucket = buckets.get(key)
    if (bucket) bucket.push(session)
    else buckets.set(key, [session])
  }

  const groups = [...buckets].filter(([, items]) => items.length > 0)
  const hasFolders = groups.some(([key]) => key !== '')

  const options: SearchableSelectOption[] = []
  for (const [key, items] of groups) {
    const groupLabel = key === ''
      ? (hasFolders ? labels.recents : '')
      : (nameById.get(key) || labels.unavailableFolder)
    for (const session of items) {
      const label = sessionTitle(session, labels)
      options.push({
        value: session.id ?? '',
        label,
        group: key,
        groupLabel,
        keywords: [label, groupLabel, session.id ?? ''],
        meta: sessionMark(session, labels),
      })
    }
  }
  return options
}
