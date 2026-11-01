import { randomUUID } from '@/utils/uuid'
import {
  getBotsByBotIdAcpOptions,
  getBots,
  getBotsByBotIdSessions,
  getBotsByBotIdSessionsBySessionId,
  postBotsByBotIdSessions,
  postBotsByBotIdSessionsBySessionIdFork,
  postBotsByBotIdSessionsBySessionIdAcpRuntime,
  deleteBotsByBotIdSessionsBySessionId,
  patchBotsByBotIdSessionsBySessionId,
  getBotsByBotIdSessionsModelPreferenceSeed,
  patchBotsByBotIdSessionsBySessionIdAcpRuntimeMode,
  patchBotsByBotIdSessionsBySessionIdAcpRuntimeModel,
  patchBotsByBotIdSessionsBySessionIdAcpRuntimeReasoning,
  getBotsByBotIdSessionsBySessionIdQueue,
  postBotsByBotIdSessionsBySessionIdSteerQueue,
  postBotsByBotIdSessionsBySessionIdFollowUpQueue,
  postBotsByBotIdSessionsBySessionIdFollowUpQueueByItemIdSteer,
  putBotsByBotIdSessionsBySessionIdSteerQueueReorder,
  putBotsByBotIdSessionsBySessionIdFollowUpQueueReorder,
  patchBotsByBotIdSessionsBySessionIdSteerQueueByItemId,
  patchBotsByBotIdSessionsBySessionIdFollowUpQueueByItemId,
  deleteBotsByBotIdSessionsBySessionIdSteerQueueByItemId,
  deleteBotsByBotIdSessionsBySessionIdFollowUpQueueByItemId,
} from '@memohai/sdk'
import type {
  AcpagentRuntimeStatus,
  HandlersFollowUpQueueItemResponse,
  HandlersSessionQueueResponse,
  HandlersSteerQueueItemResponse,
} from '@memohai/sdk'
import type { Bot, SessionSummary } from './useChat.types'

export interface CreateSessionOptions {
  botAgentId?: string
  title?: string
  type?: string
  sessionMode?: string
  runtimeType?: string
  metadata?: Record<string, unknown>
  runtimeMetadata?: Record<string, unknown>
  /**
   * Bot workdir to bind the session to. Immutable after creation: the
   * workdir pins the session's workspace target and working directory.
   */
  workdirId?: string
  /**
   * First-send picker pair (issue #879). Carried only when the pair has an
   * explicit source (user pick / remembered session); when omitted the
   * session is born with NULL preference columns and follows the bot default.
   * The server reconciles both before the INSERT.
   */
  preferredChatModelId?: string
  preferredReasoningEffort?: string
}

export interface ACPAgentOptionsQuery {
  agentId: string
  // The Agent instance whose setup the options come from.
  botAgentId?: string
  // The project the conversation will run in: an agent offers commands and
  // settings by its working directory.
  projectPath?: string
  // The model whose reasoning efforts to report; the agent's current one
  // when omitted.
  modelId?: string
}

/**
 * The two queues share one wire shape for the fields the composer renders.
 * The server keeps them as separate response types; the union here is only a
 * read-side convenience and never crosses back into a request.
 */
export type SessionQueueItem = Pick<
  HandlersSteerQueueItemResponse & HandlersFollowUpQueueItemResponse,
  'item_id' | 'status' | 'position' | 'text'
>

export type SessionQueuesResponse = HandlersSessionQueueResponse

export function queueItemText(item: SessionQueueItem): string {
  return item.text ?? ''
}

const queuePath = (botId: string, sessionId: string) => ({ bot_id: botId.trim(), session_id: sessionId.trim() })

export async function enqueueSteerQueue(botId: string, sessionId: string, text: string, invocationId = randomUUID()): Promise<SessionQueueItem> {
  const { data } = await postBotsByBotIdSessionsBySessionIdSteerQueue({
    path: queuePath(botId, sessionId),
    body: { invocation_id: invocationId, text },
    throwOnError: true,
  })
  return data
}

export async function fetchSessionQueues(botId: string, sessionId: string): Promise<SessionQueuesResponse> {
  const { data } = await getBotsByBotIdSessionsBySessionIdQueue({ path: queuePath(botId, sessionId), throwOnError: true })
  return data ?? {}
}

export async function enqueueFollowUpQueue(botId: string, sessionId: string, text: string, invocationId = randomUUID()): Promise<SessionQueueItem> {
  const { data } = await postBotsByBotIdSessionsBySessionIdFollowUpQueue({
    path: queuePath(botId, sessionId),
    body: { invocation_id: invocationId, text },
    throwOnError: true,
  })
  return data
}

export async function promoteFollowUpQueueItemToSteer(botId: string, sessionId: string, itemId: string): Promise<SessionQueueItem> {
  const { data } = await postBotsByBotIdSessionsBySessionIdFollowUpQueueByItemIdSteer({
    path: { ...queuePath(botId, sessionId), item_id: itemId.trim() },
    throwOnError: true,
  })
  return data
}

export async function updateSteerQueueItem(botId: string, sessionId: string, itemId: string, text: string): Promise<SessionQueueItem> {
  const { data } = await patchBotsByBotIdSessionsBySessionIdSteerQueueByItemId({
    path: { ...queuePath(botId, sessionId), item_id: itemId.trim() },
    body: { text },
    throwOnError: true,
  })
  return data
}

export async function updateFollowUpQueueItem(botId: string, sessionId: string, itemId: string, text: string): Promise<SessionQueueItem> {
  const { data } = await patchBotsByBotIdSessionsBySessionIdFollowUpQueueByItemId({
    path: { ...queuePath(botId, sessionId), item_id: itemId.trim() },
    body: { text },
    throwOnError: true,
  })
  return data
}

export async function deleteSteerQueueItem(botId: string, sessionId: string, itemId: string): Promise<void> {
  await deleteBotsByBotIdSessionsBySessionIdSteerQueueByItemId({ path: { ...queuePath(botId, sessionId), item_id: itemId.trim() }, throwOnError: true })
}

export async function deleteFollowUpQueueItem(botId: string, sessionId: string, itemId: string): Promise<void> {
  await deleteBotsByBotIdSessionsBySessionIdFollowUpQueueByItemId({ path: { ...queuePath(botId, sessionId), item_id: itemId.trim() }, throwOnError: true })
}

export async function reorderSteerQueue(botId: string, sessionId: string, itemId: string, beforeId: string): Promise<SessionQueueItem[]> {
  const { data } = await putBotsByBotIdSessionsBySessionIdSteerQueueReorder({
    path: queuePath(botId, sessionId),
    body: { item: { item_id: itemId }, before: { item_id: beforeId } },
    throwOnError: true,
  })
  return data?.items ?? []
}

export async function reorderFollowUpQueue(botId: string, sessionId: string, itemId: string, beforeId: string): Promise<SessionQueueItem[]> {
  const { data } = await putBotsByBotIdSessionsBySessionIdFollowUpQueueReorder({
    path: queuePath(botId, sessionId),
    body: { item: { item_id: itemId }, before: { item_id: beforeId } },
    throwOnError: true,
  })
  return data?.items ?? []
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

/**
 * Pages the sidebar's session lists (Recents and workdir folders). Sessions
 * that schedule runs created are left out: the Schedule panel lists them under
 * their schedule. A schedule-created session that a schedule still runs in
 * stays, since that schedule's card opens it as an ordinary conversation.
 */
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
      hide_schedule_runs: true,
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
        workdir_id: options?.workdirId?.trim() || undefined,
        preferred_chat_model_id: options?.preferredChatModelId?.trim() || undefined,
        preferred_reasoning_effort: options?.preferredReasoningEffort?.trim() || undefined,
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

// Picker pair persistence (issue #879): best-effort PATCH from the composer.
// The caller treats failures as silent — the next sent message writes the
// resolved pair back server-side, so a dropped PATCH only loses the
// pick-until-send window.
export async function updateSessionModelPreference(botId: string, sessionId: string, modelId: string, reasoningEffort: string, expectedRevision: string): Promise<SessionSummary> {
  const { data } = await patchBotsByBotIdSessionsBySessionId({
    path: { bot_id: botId.trim(), session_id: sessionId.trim() },
    body: {
      expected_model_preference_revision: expectedRevision,
      preferred_chat_model_id: modelId,
      preferred_reasoning_effort: reasoningEffort,
    },
    throwOnError: true,
  })
  return data as SessionSummary
}

export interface ModelPreferenceSeed {
  model_id?: string
  reasoning_effort?: string
}

// Welcome composer seed (issue #879): the pair of the bot's most recent
// native session. Empty fields mean "no seed" — fall back to the bot default.
export async function fetchModelPreferenceSeed(botId: string): Promise<ModelPreferenceSeed> {
  const { data } = await getBotsByBotIdSessionsModelPreferenceSeed({
    path: { bot_id: botId.trim() },
    throwOnError: true,
  })
  return data as ModelPreferenceSeed
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

// fetchACPAgentOptions reads what a new conversation with the agent could
// choose: models, reasoning efforts, modes and commands.
export async function fetchACPAgentOptions(botId: string, options: ACPAgentOptionsQuery): Promise<AcpagentRuntimeStatus> {
  const { data } = await getBotsByBotIdAcpOptions({
    path: { bot_id: botId.trim() },
    query: {
      acp_agent_id: options.agentId.trim(),
      bot_agent_id: options.botAgentId?.trim() || undefined,
      project_path: options.projectPath?.trim() || undefined,
      model_id: options.modelId?.trim() || undefined,
    },
    throwOnError: true,
  })
  return data as AcpagentRuntimeStatus
}

export async function deleteSession(botId: string, sessionId: string): Promise<void> {
  await deleteBotsByBotIdSessionsBySessionId({
    path: { bot_id: botId.trim(), session_id: sessionId.trim() },
    throwOnError: true,
  })
}
