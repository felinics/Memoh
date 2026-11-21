import type {
  ChatAttachment,
  RequestedSkillSelection,
  SessionSummary,
  UIAttachment,
  UIAttachmentsMessage,
  UIErrorMessage,
  UIForwardRef,
  UIReasoningMessage,
  UINoticeMessage,
  UICommandMessage,
  UIStatusMessage,
  UIReplyRef,
  UISkillActivation,
  UITextMessage,
  UIToolApproval,
  UIToolMessage,
  UIUserInput,
} from '@/composables/api/useChat.types'
import type { BotAgentRuntime, DirectBotAgentRuntime } from '@/utils/bot-agent'

/**
 * Prefix of the synthetic turn_id the runtime projection assigns to a live
 * steer that has no durable history turn yet. Shared by projection, merge, and
 * scroll anchoring so the convention is defined once.
 */
export const RUNTIME_STEER_TURN_PREFIX = 'queue-steer:'

export function isRuntimeSteerTurnId(turnId: string | undefined | null): boolean {
  return typeof turnId === 'string' && turnId.startsWith(RUNTIME_STEER_TURN_PREFIX)
}

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
export type NoticeBlock = UINoticeMessage

export interface ToolCallBlock extends UIToolMessage {
  toolCallId: string
  toolName: string
  result: unknown | null
  done: boolean
  approval?: UIToolApproval
  userInput?: UIUserInput
  backgroundTask?: BackgroundTask
}

export type ContentBlock = TextBlock | ThinkingBlock | ToolCallBlock | AttachmentBlock | ErrorBlock | NoticeBlock | UICommandMessage | UIStatusMessage

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
      isExternalAgent: boolean
      isPendingExternalAgent: false
      metadata: Record<string, unknown>
      explicitSelection: boolean
    }
  | {
      kind: 'draft-external-agent'
      sessionId: null
      session: null
      runtimeType: 'acp_agent' | DirectBotAgentRuntime
      isExternalAgent: true
      isPendingExternalAgent: true
      metadata: Record<string, unknown>
      explicitSelection: boolean
    }
  | {
      kind: 'draft-native'
      sessionId: null
      session: null
      runtimeType: 'model'
      isExternalAgent: false
      isPendingExternalAgent: false
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
  // Immutable turn-level sequence. Both the settled (REST history) path and
  // live runtime frames carry one; a steer's turn stays unnumbered only until
  // its step commits.
  turnPosition?: number
  // True once the turn has been read back from durable history. Distinct from
  // turnPosition, which a live turn now also carries: pagination needs a turn
  // the database can address by id, not merely one that knows its place.
  settled?: boolean
  runtimeRunId?: string
  runtimeContinuation?: boolean
  // Set from the frame's steer turn ids, not read off the turn id's shape: a
  // steer is named at claim time now, so it carries an ordinary turn id.
  runtimeSteer?: boolean
  // Set by createOptimisticUserTurn / createOptimisticAssistantTurn and
  // cleared as soon as the server twin replaces the optimistic row in
  // mergeMessages. mergeMessages keys off this flag to decide which side of
  // a (optimistic, server) pair to drop, so any new code path that creates a
  // client-only turn before the server acknowledges it MUST set this.
  __optimistic?: boolean
}

export interface ChatAssistantTurn {
  runtimeForkable?: boolean
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
  settled?: boolean
  runtimeRunId?: string
  runtimeContinuation?: boolean
  // See ChatUserTurn.runtimeSteer.
  runtimeSteer?: boolean
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
  settled?: boolean
}

export type ChatMessage = ChatUserTurn | ChatAssistantTurn | ChatSystemTurn

/**
 * A user turn the queue steered into a run that was already running.
 *
 * The marker is set from the frame's steer turn ids. Testing the turn id for
 * the `queue-steer:` prefix only ever worked while a steer had no durable name;
 * once the server names one at claim time it carries an ordinary turn id, and
 * every caller keyed on the prefix silently stopped matching.
 */
export function isRuntimeSteerUserTurn(
  message: { role: string, runtimeSteer?: boolean },
): boolean {
  return message.role === 'user' && message.runtimeSteer === true
}

/**
 * A user turn admitted from a durable follow-up continuation run.
 *
 * Runtime continuation turns are intentionally distinct from queue steer
 * turns: both are runtime-owned inputs, but only the former represents a
 * follow-up item being handed off to a new run.
 */
export function isRuntimeContinuationUserTurn(
  message: {
    role: string
    turnId?: string
    runtimeRunId?: string
    runtimeContinuation?: boolean
    runtimeSteer?: boolean
  },
): boolean {
  return message.role === 'user'
    && Boolean(message.runtimeRunId?.trim())
    && message.runtimeContinuation === true
    && Boolean(message.turnId?.trim())
    && !isRuntimeSteerUserTurn(message)
}

export type SendMessageStage = 'startup' | 'stream'

export interface SendMessageResult {
  ok: boolean
  /** A real chat message completed, rather than a locally handled command. */
  messageSent?: boolean
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
  /** Defaults to true. Actions that do not consume a composer draft opt out. */
  restoreDraftOnFailure?: boolean
  /** Called after command handling, before creating a session or sending a message. */
  onBeforeMessageSend?: () => void
  /** The server has finished this turn's preference write, before generation ends. */
  onModelPreferenceSettled?: () => void
  /** Called immediately before a real chat turn is appended or dispatched. */
  onBeforeTurnAppend?: (target: ChatViewTarget) => void
  /** Called when that turn is rolled back after a startup-stage failure. */
  onTurnAppendAborted?: () => void
}

export interface ChatWorkspaceTargetSnapshot {
  target_id: string
  kind?: string
  name?: string
}

export type ChatWorkspaceTargetSelectionSource = 'unset' | 'default' | 'session' | 'user'

export interface ExternalAgentSessionInput {
  /** Explicit permission override for this draft; persisted on first send. */
  permissionMode?: string
  planMode?: boolean
  /** Persisted Agent instance selected for this session. */
  botAgentId?: string
  /** Runtime owned by the selected Agent. Omitted by legacy ACP callers. */
  runtime?: BotAgentRuntime
  /** Temporary ACP provider identity stored in BotAgent metadata. */
  agentId: string
  sessionMode?: 'chat' | 'discuss'
  projectPath?: string
  title?: string
  /** ACP model and reasoning effort picked before the session exists; sent with its creation. */
  acpModelId?: string
  acpReasoningEffort?: string
}
