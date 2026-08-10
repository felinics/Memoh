import { useUserStore } from '@/store/user'
import type {
  Bot,
  RequestedSkillRequest,
  RequestedSkillSelection,
  UIAttachment,
  UIForwardRef,
  UIReplyRef,
  UISkillActivation,
  UIToolApproval,
  UIUserInput,
  UIUserTurn,
} from '@/composables/api/useChat'
import type { ChatMessage, ChatUserTurn, ToolCallBlock } from './chat/types'

// Stateless normalization/merge helpers shared by the chat store. Everything
// here is pure with respect to the chat store — no refs, no maps, no session
// state — so the store file stays focused on state machines and these can be
// unit-tested directly. `resolveIsSelf` is the one function that reaches for
// another Pinia store (user), but it does so lazily at call time, which only
// ever happens inside an active store context.

export const nextId = () => `${Date.now()}-${Math.floor(Math.random() * 1000)}`

export const isPendingBot = (bot: Bot | null | undefined) =>
  bot?.status === 'creating' || bot?.status === 'deleting'

export function normalizeTimestamp(value?: string): string {
  const raw = (value ?? '').trim()
  if (!raw) return new Date().toISOString()
  const parsed = new Date(raw)
  return Number.isNaN(parsed.getTime()) ? new Date().toISOString() : parsed.toISOString()
}

export function resolveIsSelf(turn: UIUserTurn): boolean {
  const platform = (turn.platform ?? '').trim().toLowerCase()
  if (!platform || platform === 'local') return true
  const senderUserId = (turn.sender_user_id ?? '').trim()
  if (!senderUserId) return false
  const userStore = useUserStore()
  const currentUserId = (userStore.userInfo.id ?? '').trim()
  if (!currentUserId) return false
  return senderUserId === currentUserId
}

export function normalizeAttachment(att: UIAttachment): UIAttachment {
  return { ...att }
}

export function normalizeReplyRef(reply?: UIReplyRef): UIReplyRef | undefined {
  if (!reply) return undefined
  const normalized = {
    message_id: (reply.message_id ?? '').trim(),
    sender: (reply.sender ?? '').trim(),
    preview: (reply.preview ?? '').trim(),
    attachments: (reply.attachments ?? []).map(normalizeAttachment),
  }
  return normalized.message_id || normalized.sender || normalized.preview || normalized.attachments.length ? normalized : undefined
}

export function normalizeForwardRef(forward?: UIForwardRef): UIForwardRef | undefined {
  if (!forward) return undefined
  const normalized = {
    message_id: (forward.message_id ?? '').trim(),
    from_user_id: (forward.from_user_id ?? '').trim(),
    from_conversation_id: (forward.from_conversation_id ?? '').trim(),
    sender: (forward.sender ?? '').trim(),
    date: typeof forward.date === 'number' && Number.isFinite(forward.date) ? forward.date : undefined,
  }
  return normalized.message_id || normalized.from_user_id || normalized.from_conversation_id || normalized.sender || normalized.date
    ? normalized
    : undefined
}

export function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' ? value as Record<string, unknown> : {}
}

export function pickString(obj: Record<string, unknown>, ...keys: string[]): string {
  for (const key of keys) {
    const value = obj[key]
    if (typeof value === 'string' && value.trim()) return value.trim()
  }
  return ''
}

export function pickRawString(obj: Record<string, unknown>, ...keys: string[]): string {
  for (const key of keys) {
    const value = obj[key]
    if (typeof value === 'string' && value.length > 0) return value
  }
  return ''
}

export function structuredToolResult(result: unknown): Record<string, unknown> {
  const record = asRecord(result)
  const camel = asRecord(record.structuredContent)
  if (Object.keys(camel).length > 0) return camel
  const snake = asRecord(record.structured_content)
  return Object.keys(snake).length > 0 ? snake : record
}

export function taskIdFromToolBlock(block: ToolCallBlock): string {
  if (block.backgroundTask?.taskId) return block.backgroundTask.taskId
  const structured = structuredToolResult(block.result)
  const result = asRecord(block.result)
  return pickString(structured, 'task_id', 'taskId') || pickString(result, 'task_id', 'taskId')
}

export function skillActivationTextFromRaw(text: string, activation: UISkillActivation | undefined): string {
  const value = text.trim()
  if (!value || !activation) return value
  if (value.startsWith('The user activated the following skill for this turn without an additional prompt:')) {
    return ''
  }
  if (!value.startsWith('/')) return value
  const [head = '', ...rest] = value.split(/\s+/)
  const selector = head.replace(/^\//, '').split('@')[0]?.trim() ?? ''
  const matchesSkill = (activation.skills ?? []).some(skill => selector === skill.name?.trim())
  return matchesSkill ? rest.join(' ').trim() : ''
}

export function sortChatMessages(items: ChatMessage[]): ChatMessage[] {
  return [...items].sort((a, b) => {
    const at = Date.parse(a.timestamp)
    const bt = Date.parse(b.timestamp)
    if (!Number.isNaN(at) && !Number.isNaN(bt) && at !== bt) return at - bt
    return a.id.localeCompare(b.id)
  })
}

// Optimistic turns set `__optimistic: true` at construction
// (createOptimisticUserTurn / createOptimisticAssistantTurn). Server-derived
// turns from fetchMessagesUI and SSE never carry this flag, so an opaque id
// shape (numeric, UUID, slug) is irrelevant here.
export function isOptimisticTurn(turn: ChatMessage): boolean {
  return '__optimistic' in turn && turn.__optimistic === true
}

export function createInvocationId(): string {
  const randomUUID = globalThis.crypto?.randomUUID
  if (typeof randomUUID === 'function') return randomUUID.call(globalThis.crypto)
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
}

export function cloneToolApprovalState(approval: UIToolApproval): UIToolApproval {
  return { ...approval }
}

export function isPendingApproval(approval?: UIToolApproval) {
  return approval?.status?.trim().toLowerCase() === 'pending'
}

export function isSameApproval(left?: UIToolApproval, right?: UIToolApproval) {
  const leftId = left?.approval_id?.trim()
  const rightId = right?.approval_id?.trim()
  return Boolean(leftId && rightId && leftId === rightId)
}

export function mergeApprovalState(existing?: UIToolApproval, incoming?: UIToolApproval) {
  if (!incoming) return existing
  if (isSameApproval(existing, incoming) && !isPendingApproval(existing) && isPendingApproval(incoming)) {
    return existing
  }
  return incoming
}

export function cloneUserInputState(userInput: UIUserInput): UIUserInput {
  return {
    ...userInput,
    questions: userInput.questions?.map(question => ({
      ...question,
      options: question.options?.map(option => ({ ...option })),
    })),
    answers: userInput.answers?.map(answer => ({
      ...answer,
      selected: answer.selected?.map(option => ({ ...option })),
    })),
  }
}

export function normalizeRequestedSkills(items?: RequestedSkillSelection[]): RequestedSkillSelection[] {
  if (!items?.length) return []
  const out: RequestedSkillSelection[] = []
  const seen = new Set<string>()
  for (const item of items) {
    const name = item.name?.trim()
    if (!name) continue
    const key = name
    if (seen.has(key)) continue
    seen.add(key)
    out.push({
      name,
      display_name: item.display_name?.trim() || undefined,
      description: item.description?.trim() || undefined,
      source_kind: item.source_kind?.trim() || undefined,
      state: item.state?.trim() || undefined,
    })
  }
  return out
}

export function requestedSkillRequestsForWire(items: RequestedSkillSelection[]): RequestedSkillRequest[] {
  return items.map(item => ({
    name: item.name,
  }))
}

export function cloneRequestedSkills(items: RequestedSkillSelection[]): RequestedSkillSelection[] {
  return items.map(item => ({ ...item }))
}

export function serverMessageId(turn: ChatMessage): string {
  return (turn.serverId ?? turn.id).trim()
}

export function hasUserAttachments(turn: ChatMessage): turn is ChatUserTurn {
  return turn.role === 'user' && turn.attachments.length > 0
}
