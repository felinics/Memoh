import {
  getBots,
  getBotsByBotIdAcpRuntimesByRuntimeId,
  deleteBotsByBotIdAcpRuntimesByRuntimeId,
  getBotsByBotIdSessions,
  getBotsByBotIdSessionsBySessionId,
  postBotsByBotIdAcpRuntimes,
  postBotsByBotIdSessions,
  postBotsByBotIdSessionsBySessionIdFork,
  postBotsByBotIdSessionsBySessionIdAcpRuntime,
  deleteBotsByBotIdSessionsBySessionId,
  patchBotsByBotIdAcpRuntimesByRuntimeIdModel,
  patchBotsByBotIdAcpRuntimesByRuntimeIdMode,
  patchBotsByBotIdAcpRuntimesByRuntimeIdReasoning,
  patchBotsByBotIdSessionsBySessionId,
  patchBotsByBotIdSessionsBySessionIdAcpRuntimeMode,
  patchBotsByBotIdSessionsBySessionIdAcpRuntimeModel,
  patchBotsByBotIdSessionsBySessionIdAcpRuntimeReasoning,
} from '@memohai/sdk'
import type { AcpagentRuntimeStatus } from '@memohai/sdk'
import type { Bot, SessionSummary } from './useChat.types'

export interface CreateSessionOptions {
  botAgentId?: string
  title?: string
  type?: string
  sessionMode?: string
  runtimeType?: string
  metadata?: Record<string, unknown>
  runtimeMetadata?: Record<string, unknown>
  /** Warm pre-session ACP runtime to bind at creation time. */
  acpRuntimeId?: string
  /**
   * Bot workdir to bind the session to. Immutable after creation: the
   * workdir pins the session's workspace target and working directory.
   */
  workdirId?: string
}

export interface CreateACPRuntimeOptions {
  agentId: string
  projectPath?: string
}

export async function fetchBots(): Promise<Bot[]> {
  const { data } = await getBots({ throwOnError: true })
  return data?.items ?? []
}

export interface FetchSessionsOptions {
  types?: string[]
  parentSessionId?: string
  /**
   * Only sessions bound to this workdir. The literal `none` selects the
   * unbound bucket. Pages independently of the unfiltered timeline, so a
   * folder can reach chats older than the loaded global pages.
   */
  workdirId?: string
  limit?: number
  cursor?: string
}

export interface FetchSessionsResult {
  items: SessionSummary[]
  nextCursor: string | null
}

const DEFAULT_SESSION_TYPES = ['chat', 'discuss', 'acp_agent', 'schedule']
const DEFAULT_SESSION_PAGE_SIZE = 50

export async function fetchSessions(botId: string, options?: FetchSessionsOptions): Promise<FetchSessionsResult> {
  const id = botId.trim()
  if (!id) return { items: [], nextCursor: null }
  const types = (options?.types ?? DEFAULT_SESSION_TYPES).map(t => t.trim()).filter(Boolean)
  const parentSessionId = options?.parentSessionId?.trim() ?? ''
  const workdirId = options?.workdirId?.trim() ?? ''
  const cursor = options?.cursor?.trim() ?? ''
  const { data } = await getBotsByBotIdSessions({
    path: { bot_id: id },
    query: {
      types: types.join(','),
      ...(parentSessionId ? { parent_session_id: parentSessionId } : {}),
      ...(workdirId ? { workdir_id: workdirId } : {}),
      limit: options?.limit ?? DEFAULT_SESSION_PAGE_SIZE,
      ...(cursor ? { cursor } : {}),
    },
    throwOnError: true,
  })
  const payload = data as { items?: SessionSummary[]; next_cursor?: string } | undefined
  return {
    items: payload?.items ?? [],
    nextCursor: payload?.next_cursor?.trim() || null,
  }
}

export async function fetchSession(botId: string, sessionId: string): Promise<SessionSummary> {
  const { data } = await getBotsByBotIdSessionsBySessionId({
    path: { bot_id: botId.trim(), session_id: sessionId.trim() },
    throwOnError: true,
  })
  return data as SessionSummary
}

export async function createSession(botId: string, options?: string | CreateSessionOptions): Promise<SessionSummary> {
  const id = botId.trim()
  if (!id) throw new Error('bot id is required')
  const body = typeof options === 'string'
    ? { title: options, channel_type: 'local' }
    : {
        title: options?.title ?? '',
        bot_agent_id: options?.botAgentId?.trim() || undefined,
        channel_type: 'local',
        type: options?.type,
        session_mode: options?.sessionMode,
        runtime_type: options?.runtimeType,
        metadata: options?.metadata,
        runtime_metadata: options?.runtimeMetadata,
        acp_runtime_id: options?.acpRuntimeId?.trim() || undefined,
        workdir_id: options?.workdirId?.trim() || undefined,
      }
  const { data } = await postBotsByBotIdSessions({
    path: { bot_id: id },
    body,
    throwOnError: true,
  })
  return data as SessionSummary
}

export interface ForkSessionOptions {
  title?: string
}

export async function forkSessionFromTurn(botId: string, sessionId: string, turnId: string, options?: ForkSessionOptions): Promise<SessionSummary> {
  const bid = botId.trim()
  const sid = sessionId.trim()
  const tid = turnId.trim()
  const title = options?.title?.trim() ?? ''
  if (!bid) throw new Error('bot id is required')
  if (!sid) throw new Error('session id is required')
  if (!tid) throw new Error('turn id is required')
  const { data } = await postBotsByBotIdSessionsBySessionIdFork({
    path: { bot_id: bid, session_id: sid },
    body: {
      turn_id: tid,
      ...(title ? { title } : {}),
    },
    throwOnError: true,
  })
  return data as SessionSummary
}

export async function updateSessionTitle(botId: string, sessionId: string, title: string): Promise<SessionSummary> {
  const { data } = await patchBotsByBotIdSessionsBySessionId({
    path: { bot_id: botId.trim(), session_id: sessionId.trim() },
    body: { title },
    throwOnError: true,
  })
  return data as SessionSummary
}

export interface UpdateSessionAgentOptions {
  botAgentId?: string
  type?: string
  sessionMode?: string
  runtimeType?: string
  metadata?: Record<string, unknown>
  runtimeMetadata?: Record<string, unknown>
}

export async function updateSessionAgent(botId: string, sessionId: string, options: UpdateSessionAgentOptions): Promise<SessionSummary> {
  const { data } = await patchBotsByBotIdSessionsBySessionId({
    path: { bot_id: botId.trim(), session_id: sessionId.trim() },
    body: {
      bot_agent_id: options.botAgentId,
      type: options.type,
      session_mode: options.sessionMode,
      runtime_type: options.runtimeType,
      metadata: options.metadata,
      runtime_metadata: options.runtimeMetadata,
    },
    throwOnError: true,
  })
  return data as SessionSummary
}

export async function ensureACPRuntime(botId: string, sessionId: string): Promise<AcpagentRuntimeStatus> {
  const { data } = await postBotsByBotIdSessionsBySessionIdAcpRuntime({
    path: { bot_id: botId.trim(), session_id: sessionId.trim() },
    throwOnError: true,
  })
  return data as AcpagentRuntimeStatus
}

export async function setACPRuntimeModel(botId: string, sessionId: string, modelId: string): Promise<AcpagentRuntimeStatus> {
  const { data } = await patchBotsByBotIdSessionsBySessionIdAcpRuntimeModel({
    path: { bot_id: botId.trim(), session_id: sessionId.trim() },
    body: { model_id: modelId },
    throwOnError: true,
  })
  return data as AcpagentRuntimeStatus
}

export async function setACPRuntimeMode(botId: string, sessionId: string, modeId: string): Promise<AcpagentRuntimeStatus> {
  const { data } = await patchBotsByBotIdSessionsBySessionIdAcpRuntimeMode({
    path: { bot_id: botId.trim(), session_id: sessionId.trim() },
    body: { mode_id: modeId },
    throwOnError: true,
  })
  return data as AcpagentRuntimeStatus
}

export async function setACPRuntimeReasoning(botId: string, sessionId: string, effort: string): Promise<AcpagentRuntimeStatus> {
  const { data } = await patchBotsByBotIdSessionsBySessionIdAcpRuntimeReasoning({
    path: { bot_id: botId.trim(), session_id: sessionId.trim() },
    body: { reasoning_effort: effort.trim() },
    throwOnError: true,
  })
  return data as AcpagentRuntimeStatus
}

export async function createACPRuntime(botId: string, options: CreateACPRuntimeOptions): Promise<AcpagentRuntimeStatus> {
  const { data } = await postBotsByBotIdAcpRuntimes({
    path: { bot_id: botId.trim() },
    body: {
      acp_agent_id: options.agentId.trim(),
      project_path: options.projectPath?.trim(),
    },
    throwOnError: true,
  })
  return data as AcpagentRuntimeStatus
}

export async function fetchACPRuntimeByID(botId: string, runtimeId: string): Promise<AcpagentRuntimeStatus> {
  const { data } = await getBotsByBotIdAcpRuntimesByRuntimeId({
    path: { bot_id: botId.trim(), runtime_id: runtimeId.trim() },
    throwOnError: true,
  })
  return data as AcpagentRuntimeStatus
}

export async function setACPRuntimeModelByID(botId: string, runtimeId: string, modelId: string): Promise<AcpagentRuntimeStatus> {
  const { data } = await patchBotsByBotIdAcpRuntimesByRuntimeIdModel({
    path: { bot_id: botId.trim(), runtime_id: runtimeId.trim() },
    // An empty model_id resets the runtime to the agent default model.
    body: { model_id: modelId.trim() },
    throwOnError: true,
  })
  return data as AcpagentRuntimeStatus
}

export async function setACPRuntimeModeByID(botId: string, runtimeId: string, modeId: string): Promise<AcpagentRuntimeStatus> {
  const { data } = await patchBotsByBotIdAcpRuntimesByRuntimeIdMode({
    path: { bot_id: botId.trim(), runtime_id: runtimeId.trim() },
    body: { mode_id: modeId },
    throwOnError: true,
  })
  return data as AcpagentRuntimeStatus
}

export async function setACPRuntimeReasoningByID(botId: string, runtimeId: string, effort: string): Promise<AcpagentRuntimeStatus> {
  const { data } = await patchBotsByBotIdAcpRuntimesByRuntimeIdReasoning({
    path: { bot_id: botId.trim(), runtime_id: runtimeId.trim() },
    body: { reasoning_effort: effort.trim() },
    throwOnError: true,
  })
  return data as AcpagentRuntimeStatus
}

export async function closeACPRuntime(botId: string, runtimeId: string): Promise<void> {
  await deleteBotsByBotIdAcpRuntimesByRuntimeId({
    path: { bot_id: botId.trim(), runtime_id: runtimeId.trim() },
    throwOnError: true,
  })
}

export async function deleteSession(botId: string, sessionId: string): Promise<void> {
  await deleteBotsByBotIdSessionsBySessionId({
    path: { bot_id: botId.trim(), session_id: sessionId.trim() },
    throwOnError: true,
  })
}
