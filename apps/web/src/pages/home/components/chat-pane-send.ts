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

// ── Composer (model, effort) pair draft (issue #879) ─────────────────────
// The welcome composer keeps its picked pair in a per-bot localStorage draft
// until the pair is durably handed to the server. The draft is consumed ONLY
// by a successful welcome send (REST createSession or the WS first message —
// both persist the pair with the session); it must survive everything else:
// opening a historical session, a hard refresh, a failed send (spec P2′).

// carriedPairForSource is the wire gate (spec §3.4): only explicit sources
// (user pick, remembered session) are carried on send/retry/edit/
// createSession; default-sourced and unset pairs are omitted so the server
// can tell "never picked" apart from "picked the default", and the session
// keeps following the bot default live.
export function carriedPairForSource(
  source: string,
  modelId: string,
  reasoningEffort: string,
): { modelId: string, reasoningEffort: string } {
  if (source !== 'user' && source !== 'session') return { modelId: '', reasoningEffort: '' }
  return { modelId: modelId.trim(), reasoningEffort: reasoningEffort.trim() }
}

export function composerPairDraftKey(botId: string): string {
  return `memoh:composer-pair:${botId.trim()}`
}

export interface ComposerPairDraft {
  model_id: string
  reasoning_effort: string
}

// All three draft operations swallow storage failures (private mode, quota):
// the draft is a persistence scaffold, never the state of record.
export function readComposerPairDraft(botId: string): ComposerPairDraft | null {
  try {
    const raw = localStorage.getItem(composerPairDraftKey(botId))
    if (!raw) return null
    const parsed = JSON.parse(raw) as { model_id?: string, reasoning_effort?: string }
    return parsed.model_id ? { model_id: parsed.model_id, reasoning_effort: parsed.reasoning_effort ?? '' } : null
  } catch {
    return null
  }
}

export function writeComposerPairDraft(botId: string, draft: ComposerPairDraft): void {
  try {
    localStorage.setItem(composerPairDraftKey(botId), JSON.stringify(draft))
  } catch { /* private mode / quota: the draft simply doesn't persist */ }
}

export function clearComposerPairDraft(botId: string): void {
  try {
    localStorage.removeItem(composerPairDraftKey(botId))
  } catch { /* ignore */ }
}

// welcomeSendConsumedDraft is the ONLY moment the draft may be cleared: the
// welcome send succeeded, so the pair now lives server-side. Clearing any
// earlier — e.g. on a welcome→session repoint — would wipe an unsent pick
// when the user merely opens a historical session (spec P2′). A failed send
// keeps the draft: the pick was never persisted.
export function welcomeSendConsumedDraft(
  target: { sessionId?: string | null },
  result: { ok: boolean, messageSent?: boolean },
): boolean {
  return result.ok && result.messageSent === true && !(target.sessionId ?? '').trim()
}
