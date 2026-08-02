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
  activeUsesACPComposer: boolean,
  selectedModelId: string,
): boolean {
  return !activeUsesACPComposer && !selectedModelId.trim()
}

const ACP_STALE_CONFIG_CODES = new Set([
  'acp.model_unavailable',
  'acp.reasoning_effort_unavailable',
])

export function shouldRefreshACPComposerConfig(
  result: SendMessageResult,
  activeUsesACPComposer: boolean,
): boolean {
  return !result.ok
    && activeUsesACPComposer
    && typeof result.errorCode === 'string'
    && ACP_STALE_CONFIG_CODES.has(result.errorCode)
}
