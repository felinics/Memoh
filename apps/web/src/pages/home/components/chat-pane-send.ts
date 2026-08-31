import type { ChatViewTarget, SendMessageResult } from '@/store/chat-list'

export interface ChatPaneSendContext {
  readonly target: ChatViewTarget
  readonly composerScope: string
}

export function captureChatPaneSendContext(
  target: ChatViewTarget,
  composerScope: string,
): ChatPaneSendContext {
  return Object.freeze({
    target: Object.freeze({ ...target }),
    composerScope: composerScope || 'chat',
  })
}

export function matchesChatPaneSendContext(
  context: ChatPaneSendContext,
  target: ChatViewTarget,
  composerScope: string,
): boolean {
  return context.target.botId === target.botId
    && context.target.sessionId === target.sessionId
    && context.target.viewId === target.viewId
    && context.composerScope === (composerScope || 'chat')
}

// composerHasNoModel gates both the send button and the keyboard send path, so
// it lives here rather than inline in the pane: the two call sites must agree,
// and this is the only piece of that decision worth testing on its own.
//
// Only a native composer can be model-less in a way that blocks sending. An ACP
// agent supplies its own default model, so an empty selection there is normal.
export function composerHasNoModel(
  activeUsesExternalAgentComposer: boolean,
  selectedModelId: string,
): boolean {
  return !activeUsesExternalAgentComposer && !selectedModelId.trim()
}

// pinnedSubagentModelId reads the model a subagent was spawned on off its
// session. The composer sends model_id with every message, so a subagent
// session that opened on the bot's default would silently move the agent onto
// another model the first time a human talks to it — and the picker would still
// read as "the default", hiding the switch.
//
// An empty result means "no pinned model to honor": a session that is not a
// subagent, one spawned before the model was recorded, or a model since deleted.
// The caller then keeps the bot default, which is what those cases already did.
export function pinnedSubagentModelId(
  sessionType: string | undefined,
  sessionMetadata: Record<string, unknown>,
  availableModelIds: readonly string[],
): string {
  if (sessionType !== 'subagent') return ''
  const id = String(sessionMetadata.model_uuid ?? '').trim()
  if (!id) return ''
  return availableModelIds.includes(id) ? id : ''
}

const ACP_STALE_CONFIG_CODES = new Set([
  'acp.model_unavailable',
  'acp.reasoning_effort_unavailable',
  // Feedback-family code (underscore form): the runtime dropped or replaced
  // the command set between admission and prompt; refresh the registry so the
  // picker stops offering the stale command.
  'acp_agent_command_stale',
])

export function shouldRefreshACPComposerConfig(
  result: SendMessageResult,
  activeUsesExternalAgentComposer: boolean,
): boolean {
  return !result.ok
    && activeUsesExternalAgentComposer
    && typeof result.errorCode === 'string'
    && ACP_STALE_CONFIG_CODES.has(result.errorCode)
}
