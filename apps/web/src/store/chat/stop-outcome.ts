import type { ChatAttachment, RequestedSkillSelection } from '@/composables/api/useChat'
import { cloneRequestedSkills } from '../chat-list.normalize'
import type { RuntimeIntegrationDeps } from './runtime-integration'
import type { ChatSendDeps } from './send'
import type {
  ChatAssistantTurn,
  ChatUserTurn,
  ChatViewTarget,
  SendMessageOptions,
  SendMessageResult,
} from './types'

// The user's stop of a turn that has shown no output yet. The server keeps
// no history for a run stopped that early, and only the server knows whether
// output had already started. So the send stays open until the server ends
// the run (awaitStopOutcome), and the reloaded history then decides whether
// the draft goes back to the composer (settleStoppedSend).

// How long a stop waits for the server to end the run before the send ends
// locally. Ending a run normally takes well under a second.
const STOP_CONFIRM_TIMEOUT_MS = 15_000

// Keeps the stopped send open. A turn with output, a retry or edit, and a
// first send the server has not confirmed end at once as before; so does a
// stop the server does not answer in time.
export function awaitStopOutcome(
  deps: Pick<RuntimeIntegrationDeps, 'assistantStreams' | 'hasVisibleAssistantBlocks' | 'firstSend'>,
  invocationId: string,
  abortError: Error,
): boolean {
  const stream = deps.assistantStreams.getAssistantStream(invocationId)
  if (
    !stream
    || stream.replacesTurn
    || deps.hasVisibleAssistantBlocks(stream.assistantTurn)
    || deps.firstSend.isAwaitingConfirmation(invocationId)
    || !deps.assistantStreams.markStopping(invocationId)
  ) return false
  setTimeout(() => {
    if (!deps.assistantStreams.getAssistantStream(invocationId)?.stopping) return
    deps.assistantStreams.clearStopping(invocationId)
    deps.assistantStreams.rejectAssistantStream(invocationId, abortError)
  }, STOP_CONFIRM_TIMEOUT_MS)
  return true
}

interface StoppedSend {
  botId: string
  targetSessionId: string
  // A first send the server confirmed: its session exists only for this send.
  firstSend: boolean
  viewTarget: ChatViewTarget
  assistantTurn: ChatAssistantTurn | null
  userTurn: ChatUserTurn | null
  composerScope: string
  text: string
  attachments?: ChatAttachment[]
  requestedSkills: RequestedSkillSelection[]
  options: SendMessageOptions
}

// Runs once the server has ended a send whose stop waited. A send the history
// kept stays as it is; one it dropped goes back to the composer without an
// error, and a first send also drops the session it created, which returns
// the pane to welcome. Returns null when the send stays, or when the turn
// showed output after all and ends as an ordinary stop.
export async function settleStoppedSend(
  deps: Pick<ChatSendDeps,
    | 'currentBotId' | 'sessionId' | 'transcriptForTarget' | 'refreshCurrentSession'
    | 'removeTurnFromSession'
    | 'cleanupFailedDeferredSession' | 'rememberStartupSendFailure'>,
  send: StoppedSend,
): Promise<SendMessageResult | null> {
  const { botId, targetSessionId, assistantTurn, userTurn, options } = send
  // The server can answer a stop with an error frame, which leaves an error
  // block on the turn. That block is not model output, so it does not count.
  const hasOutput = assistantTurn?.messages.some(block => block.type !== 'status' && block.type !== 'error')
  if (!assistantTurn || !userTurn || hasOutput) return null
  deps.removeTurnFromSession(botId, targetSessionId, assistantTurn)
  deps.removeTurnFromSession(botId, targetSessionId, userTurn)
  const turnId = assistantTurn.turnId?.trim() ?? ''
  if (targetSessionId && turnId) {
    try {
      await deps.refreshCurrentSession(botId, targetSessionId)
      const transcript = deps.transcriptForTarget({ ...send.viewTarget, botId, sessionId: targetSessionId })
      if (transcript.findTurnByTurnId(turnId, 'user')) return null
    } catch {
      // Without the history the outcome is unknown. Keeping the send avoids
      // handing back a draft the server may have run.
      return null
    }
  }
  const currentBotId = (deps.currentBotId.value ?? '').trim()
  const onSession = (deps.sessionId.value ?? '').trim() === targetSessionId
  if (send.firstSend && targetSessionId) {
    await deps.cleanupFailedDeferredSession(botId, targetSessionId, send.composerScope)
  }
  options.onTurnAppendAborted?.()
  const restore = {
    restoreInput: send.text,
    restoreAttachments: send.attachments,
    restoreRequestedSkills: cloneRequestedSkills(send.requestedSkills),
  }
  if (options.restoreDraftOnFailure !== false && currentBotId === botId && (send.firstSend || onSession)) {
    deps.rememberStartupSendFailure({
      botId,
      sessionId: send.firstSend ? '' : targetSessionId,
      composerScope: send.composerScope,
      error: '',
      ...restore,
    })
  }
  return { ok: false, stage: 'startup', error: '', ...restore, composerScope: send.composerScope }
}
