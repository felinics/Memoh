import type {
  ChatAttachment,
  RequestedSkillSelection,
  SessionSummary,
  UIAttachment,
  UIAttachmentsMessage,
  UIErrorMessage,
  UIForwardRef,
  UIReasoningMessage,
  UIReplyRef,
  UISkillActivation,
  UITextMessage,
  UIToolApproval,
  UIToolMessage,
  UIUserInput,
} from '@/composables/api/useChat.types'

export interface BackgroundTask {
  taskId: string
  status: string
  event?: string
  botId?: string
  sessionId?: string
  command?: string
  agentId?: string
  agentSessionId?: string
  outputFile?: string
  outputTail?: string
  stream?: string
  chunk?: string
  exitCode?: number
  duration?: string
  stalled?: boolean
}

export type TextBlock = UITextMessage
export type ThinkingBlock = UIReasoningMessage
export type AttachmentItem = UIAttachment
export type AttachmentBlock = UIAttachmentsMessage
export type ErrorBlock = UIErrorMessage

export interface ToolCallBlock extends UIToolMessage {
  toolCallId: string
  toolName: string
  result: unknown | null
  done: boolean
  approval?: UIToolApproval
  userInput?: UIUserInput
  backgroundTask?: BackgroundTask
}

export type ContentBlock = TextBlock | ThinkingBlock | ToolCallBlock | AttachmentBlock | ErrorBlock

export interface ChatViewTarget {
  botId: string
  sessionId: string | null
  viewId: string
}

export type ActiveChatTarget =
  | {
      kind: 'session'
      sessionId: string
      session: SessionSummary | null
      runtimeType: string
      isACP: boolean
      isPendingACP: false
      metadata: Record<string, unknown>
      explicitSelection: boolean
    }
  | {
      kind: 'draft-acp'
      sessionId: null
      session: null
      runtimeType: 'acp_agent'
      isACP: true
      isPendingACP: true
      metadata: Record<string, unknown>
      explicitSelection: boolean
    }
  | {
      kind: 'draft-native'
      sessionId: null
      session: null
      runtimeType: 'model'
      isACP: false
      isPendingACP: false
      metadata: Record<string, unknown>
      explicitSelection: boolean
    }

export interface ChatUserTurn {
  id: string
  serverId?: string
  role: 'user'
  text: string
  userMessageKind?: string
  skillActivation?: UISkillActivation
  attachments: AttachmentItem[]
  reply?: UIReplyRef
  forward?: UIForwardRef
  timestamp: string
  platform?: string
  senderDisplayName?: string
  senderAvatarUrl?: string
  senderUserId?: string
  externalMessageId?: string
  streaming: boolean
  isSelf: boolean
  invocationId?: string
  turnId?: string
  // Immutable turn-level sequence from the settled (REST history) path.
  // Live turns do not carry one until their settled twin arrives.
  turnPosition?: number
  runtimeRunId?: string
  // Set by createOptimisticUserTurn / createOptimisticAssistantTurn and
  // cleared as soon as the server twin replaces the optimistic row in
  // mergeMessages. mergeMessages keys off this flag to decide which side of
  // a (optimistic, server) pair to drop, so any new code path that creates a
  // client-only turn before the server acknowledges it MUST set this.
  __optimistic?: boolean
}

export interface ChatAssistantTurn {
  id: string
  serverId?: string
  role: 'assistant'
  messages: ContentBlock[]
  timestamp: string
  platform?: string
  externalMessageId?: string
  streaming: boolean
  invocationId?: string
  turnId?: string
  turnPosition?: number
  runtimeRunId?: string
  // See ChatUserTurn.__optimistic.
  __optimistic?: boolean
}

export interface ChatSystemTurn {
  id: string
  serverId?: string
  role: 'system'
  kind: 'background_task'
  backgroundTask: BackgroundTask
  timestamp: string
  platform?: string
  streaming: boolean
  turnId?: string
  turnPosition?: number
}

export type ChatMessage = ChatUserTurn | ChatAssistantTurn | ChatSystemTurn

export type SendMessageStage = 'startup' | 'stream'

export interface SendMessageResult {
  ok: boolean
  stage?: SendMessageStage
  error?: string
  errorCode?: string
  restoreInput?: string
  restoreAttachments?: ChatAttachment[]
  restoreRequestedSkills?: RequestedSkillSelection[]
  composerScope?: string
}

export interface SendMessageOptions {
  target?: ChatViewTarget
  modelId?: string
  reasoningEffort?: string
  workspaceTargetId?: string
  requestedSkills?: RequestedSkillSelection[]
  composerScope?: string
  /** Called immediately before a real chat turn is appended or dispatched. */
  onBeforeTurnAppend?: () => void
  /** Called when that turn is rolled back after a startup-stage failure. */
  onTurnAppendAborted?: () => void
}

export interface ChatWorkspaceTargetSnapshot {
  target_id: string
  kind?: string
  name?: string
}

export type ChatWorkspaceTargetSelectionSource = 'unset' | 'default' | 'session' | 'user'

export interface ACPAgentSessionInput {
  /** Persisted Agent instance selected for this session. */
  botAgentId?: string
  /** Temporary ACP provider identity stored in BotAgent metadata. */
  agentId: string
  sessionMode?: 'chat' | 'discuss'
  projectPath?: string
  projectMode?: string
  title?: string
  /** Warm pre-session runtime to bind to the created session. */
  runtimeId?: string
}
