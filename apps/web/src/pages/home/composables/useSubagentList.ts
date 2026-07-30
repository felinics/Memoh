import { computed } from 'vue'
import { useQuery } from '@pinia/colada'
import { fetchSessions } from '@/composables/api/useChat'
import { useChatStore } from '@/store/chat-list'
import { useWorkspaceTabsStore } from '@/store/workspace-tabs'
import type { SessionSummary } from '@/composables/api/useChat'
import type { ContentBlock, ToolCallBlock } from '@/store/chat-list'
import { useChatViewTarget } from './useChatViewContext'

export interface SubagentSession {
  id: string
  agentId: string
  title: string
}

const SUBAGENT_PAGE_SIZE = 50
const SUBAGENT_TOOL_NAMES = new Set(['spawn_agent', 'send_message'])
const ACTIVE_BACKGROUND_STATUSES = new Set(['queued', 'running', 'stalled'])

export function useSubagentList() {
  const chatStore = useChatStore()
  const workspaceTabs = useWorkspaceTabsStore()
  const target = useChatViewTarget()
  const currentBotId = computed(() => target.value.botId || null)
  const sessionId = computed(() => target.value.sessionId)
  const messages = computed(() => chatStore.chatView(target.value).transcript.messages)

  const activeSubagentTasks = computed<SubagentSession[]>(() => {
    const seen = new Set<string>()
    const items: SubagentSession[] = []
    for (const message of messages.value) {
      if (message.role !== 'assistant') continue
      for (const block of message.messages) {
        if (!isActiveSubagentTool(block)) continue
        const agentSessionId = block.backgroundTask?.agentSessionId?.trim()
        if (!agentSessionId || seen.has(agentSessionId)) continue
        seen.add(agentSessionId)
        const agentId = block.backgroundTask?.agentId?.trim() || pickString(block.input, 'id') || agentSessionId
        items.push({
          id: agentSessionId,
          agentId,
          title: agentId,
        })
      }
    }
    return items
  })

  const activeSubagentKey = computed(() => activeSubagentTasks.value.map(item => item.id).join('\u0000'))

  const { data } = useQuery({
    key: () => ['session-subagents', currentBotId.value ?? '', sessionId.value ?? '', activeSubagentKey.value],
    query: async () => {
      const botId = currentBotId.value?.trim()
      const parentSessionId = sessionId.value?.trim()
      if (!botId || !parentSessionId || activeSubagentTasks.value.length === 0) return []
      const { items } = await fetchSessions(botId, {
        types: ['subagent'],
        parentSessionId,
        limit: SUBAGENT_PAGE_SIZE,
      })
      return items.map(toSubagentSession)
    },
    enabled: () => Boolean(currentBotId.value && sessionId.value && activeSubagentTasks.value.length > 0),
    refetchOnWindowFocus: false,
  })

  const subagents = computed<SubagentSession[]>(() => {
    const sessionsById = new Map((data.value ?? []).map(item => [item.id, item]))
    return activeSubagentTasks.value.map((task) => {
      const session = sessionsById.get(task.id)
      return session ?? task
    })
  })

  function navigateToSession(subagentSessionId: string) {
    if (!subagentSessionId || !currentBotId.value) return
    const agent = subagents.value.find(item => item.id === subagentSessionId)
    workspaceTabs.openSessionChatFromView({
      viewId: target.value.viewId,
      sessionId: subagentSessionId,
      title: agent?.title || agent?.agentId,
    })
  }

  return { subagents, navigateToSession }
}

function toSubagentSession(session: SessionSummary): SubagentSession {
  const agentId = typeof session.metadata?.agent_id === 'string' && session.metadata.agent_id.trim()
    ? session.metadata.agent_id.trim()
    : session.id
  const title = session.title?.trim() || agentId
  return {
    id: session.id,
    agentId,
    title,
  }
}

function isActiveSubagentTool(block: ContentBlock): block is ToolCallBlock {
  if (block.type !== 'tool') return false
  if (!SUBAGENT_TOOL_NAMES.has(block.toolName)) return false
  if (block.done === true) return false
  const status = block.backgroundTask?.status?.trim().toLowerCase()
  if (status && !ACTIVE_BACKGROUND_STATUSES.has(status)) return false
  return Boolean(block.backgroundTask?.agentSessionId?.trim())
}

function pickString(input: unknown, key: string): string {
  if (!input || typeof input !== 'object') return ''
  const value = (input as Record<string, unknown>)[key]
  return typeof value === 'string' ? value.trim() : ''
}
