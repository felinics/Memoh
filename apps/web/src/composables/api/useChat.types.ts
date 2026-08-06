import type { BotsBot } from '@memohai/sdk'

export type Bot = BotsBot

export interface SessionSummary {
  id: string
  bot_id: string
  route_id?: string
  channel_type?: string
  type?: string
  session_mode?: string
  runtime_type?: string
  title: string
  metadata?: Record<string, unknown>
  runtime_metadata?: Record<string, unknown>
  parent_session_id?: string
  /** Immutable bot-workdir binding; empty for unbound sessions. */
  workdir_id?: string
  created_at?: string
  updated_at?: string
  route_metadata?: Record<string, unknown>
  route_conversation_type?: string
}

// Bot-wide activity SSE: `/bots/{bot_id}/sessions/events`. Carries identifier
// + minimal metadata for sidebar live-sort; never includes message bodies.
export interface SessionTouchedEvent {
  type: 'session_touched'
  session_id: string
  updated_at?: string
}

export interface SessionTitleChangedEvent {
  type: 'session_title_changed'
  session_id: string
  title: string
}

export interface SessionCreatedEvent {
  type: 'session_created'
  session_id: string
  // `type` here is the session kind (chat | discuss | acp_agent), already
  // filtered server-side to user-facing types.
  session_type?: string
  title?: string
}

export interface BotSessionActivityDroppedEvent {
  type: 'dropped'
  count?: number
}

export interface BotSessionActivityPingEvent {
  type: 'ping'
}

export type BotSessionActivityEvent =
  | SessionTouchedEvent
  | SessionTitleChangedEvent
  | SessionCreatedEvent
  | BotSessionActivityDroppedEvent
  | BotSessionActivityPingEvent

export interface FetchMessagesOptions {
  limit?: number
  before?: string
  beforeMessageId?: string
  session_id?: string
}

export interface ChatAttachment {
  type: string
  base64: string
  mime?: string
  name?: string
}

export interface RequestedSkillSelection {
  name: string
  display_name?: string
  description?: string
  source_kind?: string
  state?: string
}

export interface RequestedSkillRequest {
  name: string
}

export interface CommandActionListItem {
  id?: string
  title: string
  description?: string
  kind?: string
}

export interface CommandActionResult {
  kind: string
  title?: string
  text?: string
  items?: CommandActionListItem[]
}

export interface CommandActionError {
  code: string
  message: string
}

export interface CommandEventResponse {
  type: 'command_result' | 'command_error'
  invocation_id?: string
  composer_scope?: string
  session_id?: string
  action_id?: string
  terminal: boolean
  result?: CommandActionResult
  error?: CommandActionError
}

export interface UIAttachment {
  id?: string
  type: string
  path?: string
  url?: string
  base64?: string
  name?: string
  content_hash?: string
  bot_id?: string
  mime?: string
  size?: number
  storage_key?: string
  metadata?: Record<string, unknown>
}

export interface UIReplyRef {
  message_id?: string
  sender?: string
  preview?: string
  attachments?: UIAttachment[]
}

export interface UIForwardRef {
  message_id?: string
  from_user_id?: string
  from_conversation_id?: string
  sender?: string
  date?: number
}

export interface UITextMessage {
  id: number
  type: 'text'
  content: string
}

export interface UIReasoningMessage {
  id: number
  type: 'reasoning'
  content: string
}

export interface UIToolMessage {
  id: number
  type: 'tool'
  name: string
  input: unknown
  output?: unknown
  tool_call_id: string
  running: boolean
  progress?: unknown[]
  approval?: UIToolApproval
  execution_location?: UIExecutionLocation
  user_input?: UIUserInput
  background_task?: UIBackgroundTask
}

export interface UIExecutionLocation {
  kind: string
  name: string
}

export interface UIBackgroundTask {
  event?: string
  task_id?: string
  bot_id?: string
  session_id?: string
  command?: string
  agent_id?: string
  agent_session_id?: string
  status?: string
  stream?: string
  chunk?: string
  tail?: string
  output_file?: string
  output_tail?: string
  exit_code?: number
  duration?: string
  stalled?: boolean
}

export interface UIToolApproval {
  approval_id: string
  short_id?: number
  status: string
  decision_reason?: string
  can_approve?: boolean
}

export interface UIUserInput {
  user_input_id: string
  short_id?: number
  status: string
  questions?: UIUserInputQuestion[]
  answers?: UIUserInputAnswer[]
  can_respond?: boolean
}

export interface UIUserInputAnswer {
  question_id: string
  question: string
  selected?: UIUserInputOption[]
  custom_text?: string
  text?: string
  skipped?: boolean
}

export interface UIUserInputQuestion {
  id: string
  text: string
  kind: 'single_select' | 'multi_select' | 'text'
  options?: UIUserInputOption[]
  allow_custom?: boolean
  placeholder?: string
}

export interface UIUserInputOption {
  id: string
  label: string
  description?: string
}

export interface UIAttachmentsMessage {
  id: number
  type: 'attachments'
  attachments: UIAttachment[]
}

export interface UIErrorMessage {
  id: number
  type: 'error'
  content: string
}

export type UIMessage = UITextMessage | UIReasoningMessage | UIToolMessage | UIAttachmentsMessage | UIErrorMessage

export interface UISkillActivationSkill {
  name: string
  display_name?: string
  description?: string
  source_kind?: string
  state?: string
}

export interface UISkillActivation {
  skills?: UISkillActivationSkill[]
  prompt?: string
}

export interface UIUserTurn {
  turn_id: string
  role: 'user'
  text: string
  user_message_kind?: string
  skill_activation?: UISkillActivation
  attachments?: UIAttachment[]
  reply?: UIReplyRef
  forward?: UIForwardRef
  timestamp: string
  platform?: string
  sender_display_name?: string
  sender_avatar_url?: string
  sender_user_id?: string
  external_message_id?: string
  id?: string
}

export interface UIAssistantTurn {
  turn_id: string
  role: 'assistant'
  messages: UIMessage[]
  timestamp: string
  platform?: string
  external_message_id?: string
  id?: string
}

export interface UISystemTurn {
  turn_id: string
  role: 'system'
  kind?: 'background_task' | string
  background_task?: UIBackgroundTask
  timestamp: string
  platform?: string
  id?: string
}

export type UITurn = UIUserTurn | UIAssistantTurn | UISystemTurn

// Turn events are named by the server's run_id. Events that precede the run —
// acceptance, rejection, session creation, and validation errors — are named by
// the invocation_id the client sent, since that is the only name shared yet.
export interface UIStreamRunAcceptedEvent {
  type: 'run_accepted'
  run_id: string
  invocation_id: string
  session_id: string
  turn_id: string
  // A replay reserved no new observable position; its subscription snapshot is
  // authoritative, so duplicate acceptances may omit the cursor.
  epoch?: string
  seq?: number
  // Set when this acceptance names a run the invocation had already started,
  // which is how a redelivered send avoids producing a second turn.
  duplicate?: boolean
}

export interface UIStreamRunRejectedEvent {
  type: 'run_rejected'
  invocation_id: string
  session_id: string
  code: string
  message: string
}

export interface UIStreamErrorEvent {
  type: 'error'
  run_id?: string
  invocation_id?: string
  session_id?: string
  message: string
  feedback?: unknown
}

export interface UIStreamSessionCreatedEvent {
  type: 'session_created'
  invocation_id: string
  session_id: string
}

export type RuntimeRunStatus =
  | 'admitting'
  | 'running'
  | 'waiting_decision'
  | 'aborting'
  | 'completed'
  | 'aborted'
  | 'errored'
  | 'lost'

export interface RuntimeCursor {
  epoch: string
  seq: number
}

export interface RuntimeSteerState {
  id: string
  status: string
  text?: string
  error?: string
  created_at: string
  updated_at: string
}

export interface RuntimeRunOperation {
  kind: 'retry' | 'edit'
  replace_from_message_id: string
  replacement_user_turn?: UIUserTurn
}

export interface RuntimeCurrentRunView {
  run_id: string
  turn_id: string
  generation: string
  status: RuntimeRunStatus
  owner_id?: string
  owner_lease_expires_at?: string
  started_at: string
  updated_at: string
  messages: UIMessage[]
  request_user_turn?: UIUserTurn
  error?: string
  steer?: RuntimeSteerState
  operation?: RuntimeRunOperation
}

export interface RuntimeSnapshot {
  bot_id: string
  session_id: string
  epoch: string
  seq: number
  current_run_view?: RuntimeCurrentRunView
  updated_at: string
}

export interface RuntimeCurrentRunPatch {
  run_id: string
  status?: RuntimeRunStatus
  error?: string
  steer?: RuntimeSteerState
  updated_at?: string
  owner_lease_expires_at?: string
}

export interface RuntimeMessageAppend {
  id: number
  type: 'text' | 'reasoning'
  content: string
}

export interface RuntimeProgressAppend {
  id: number
  progress: unknown
  input?: unknown
}

export interface RuntimeDelta {
  current_run_view?: RuntimeCurrentRunView
  run?: RuntimeCurrentRunPatch
  message_appends?: RuntimeMessageAppend[]
  progress_appends?: RuntimeProgressAppend[]
  message_upserts?: UIMessage[]
  reset_messages?: boolean
}

export interface UIRuntimeSnapshotEvent {
  type: 'runtime_snapshot'
  session_id: string
  epoch: string
  seq: number
  snapshot: RuntimeSnapshot
}

export interface UIRuntimeDeltaEvent {
  type: 'runtime_delta'
  session_id: string
  epoch: string
  seq: number
  delta: RuntimeDelta
}

export interface UIRuntimeDroppedEvent {
  type: 'runtime_dropped'
  session_id: string
  epoch: string
  seq: number
  message?: string
}

export interface UIControlAckEvent {
  type: 'control_ack'
  session_id: string
  run_id: string
  control: 'abort' | 'tool_approval_response' | 'user_input_response'
  control_id: string
  applied: boolean
  code?: string
}

export type UIRuntimeEvent =
  | UIRuntimeSnapshotEvent
  | UIRuntimeDeltaEvent
  | UIRuntimeDroppedEvent

export type UIStreamEvent =
  | UIStreamRunAcceptedEvent
  | UIStreamRunRejectedEvent
  | UIStreamErrorEvent
  | UIStreamSessionCreatedEvent
  | UIRuntimeEvent
  | UIControlAckEvent
  | CommandEventResponse

export type UIStreamEventHandler = (event: UIStreamEvent) => void
