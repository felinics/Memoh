import type {
  RuntimeCurrentRunView,
  UIControlAckEvent,
  UIToolApproval,
  UIUserInput,
  WSClientMessage,
  WSUserInputAnswer,
} from '@/composables/api/useChat'
import type { ChatViewTarget } from './types'
import type { createTranscriptController } from './transcript'
import { isRuntimeRunActive } from './runtime-projection'

type Transcript = ReturnType<typeof createTranscriptController>
type DecisionKind = 'tool_approval_response' | 'user_input_response'

interface PendingDecision {
  controlId: string
  decisionId: string
  kind: DecisionKind
  botId: string
  sessionId: string
  runId: string
  rollback: () => void
  optimistic: () => void
}

interface PendingAbort {
  controlId: string
  sessionId: string
  runId: string
}

export interface ChatDecisionDeps {
  normalizeTarget: (target?: ChatViewTarget) => ChatViewTarget
  transcriptForTarget: (target?: ChatViewTarget) => Transcript
  currentRun: (sessionId: string) => RuntimeCurrentRunView | null
  // True when a socket handle exists for the bot; messages queue until open,
  // so this is not limited to fully-connected sockets.
  ensureWebSocket: (botId: string) => boolean
  send: (botId: string, message: WSClientMessage) => boolean
  createControlId: () => string
  connectionLostMessage: () => string
  resolveErrorMessage: (error: unknown, fallback: string) => string
  showError: (message: string) => void
}

function pendingKey(kind: DecisionKind, decisionId: string) {
  return `${kind}\u0000${decisionId}`
}

export function createChatDecisions(deps: ChatDecisionDeps) {
  const pendingByControlId = new Map<string, PendingDecision>()
  const pendingControlByDecision = new Map<string, string>()
  const pendingAborts = new Map<string, PendingAbort>()

  function begin(pending: PendingDecision): boolean {
    const key = pendingKey(pending.kind, pending.decisionId)
    if (pendingByControlId.has(pending.controlId) || pendingControlByDecision.has(key)) {
      return false
    }
    pendingByControlId.set(pending.controlId, pending)
    pendingControlByDecision.set(key, pending.controlId)
    return true
  }

  function finish(controlId: string, rollback: boolean) {
    const pending = pendingByControlId.get(controlId.trim())
    if (!pending) return
    pendingByControlId.delete(pending.controlId)
    pendingControlByDecision.delete(pendingKey(pending.kind, pending.decisionId))
    if (rollback) pending.rollback()
  }

  function handleControlAck(event: UIControlAckEvent): boolean {
    const pendingAbort = pendingAborts.get(event.control_id.trim())
    if (pendingAbort) {
      const matches = event.control === 'abort'
        && pendingAbort.sessionId === event.session_id.trim()
        && pendingAbort.runId === event.run_id.trim()
      if (!matches) return false
      pendingAborts.delete(pendingAbort.controlId)
      if (!event.applied && event.code) {
        deps.showError(deps.resolveErrorMessage(
          { code: event.code },
          'The response was not applied.',
        ))
      }
      return true
    }

    const pending = pendingByControlId.get(event.control_id.trim())
    if (!pending) return false
    const matches = pending.kind === event.control
      && pending.sessionId === event.session_id.trim()
      && pending.runId === event.run_id.trim()
    if (!matches) return false
    if (!event.applied && event.code) {
      finish(pending.controlId, true)
      deps.showError(deps.resolveErrorMessage(
        { code: event.code },
        'The response was not applied.',
      ))
    } else if (!event.applied) {
      finish(pending.controlId, false)
    }
    return true
  }

  function trackAbort(controlId: string, sessionId: string, runId: string) {
    const control = controlId.trim()
    const session = sessionId.trim()
    const run = runId.trim()
    if (!control || !session || !run) return
    pendingAborts.set(control, {
      controlId: control,
      sessionId: session,
      runId: run,
    })
  }

  function observeRun(sessionId: string, run: RuntimeCurrentRunView | null) {
    const sid = sessionId.trim()
    if (!sid) return
    for (const pending of [...pendingByControlId.values()]) {
      if (pending.sessionId !== sid) continue
      if (!run || run.run_id !== pending.runId) {
        finish(pending.controlId, false)
        continue
      }
      if (!isRuntimeRunActive(run.status)) {
        finish(pending.controlId, false)
        continue
      }
      if (pending.kind === 'tool_approval_response') {
        const approval = run.messages
          .filter(message => message.type === 'tool')
          .map(message => message.approval)
          .find(value => value?.approval_id === pending.decisionId)
        if (approval && (approval.status !== 'pending' || approval.can_approve === false)) {
          finish(pending.controlId, false)
        } else {
          pending.optimistic()
        }
        continue
      }
      const userInput = run.messages
        .filter(message => message.type === 'tool')
        .map(message => message.user_input)
        .find(value => value?.user_input_id === pending.decisionId)
      if (userInput && userInput.status !== 'pending') finish(pending.controlId, false)
      else pending.optimistic()
    }
  }

  async function respondToolApproval(
    approval: UIToolApproval,
    decision: 'approve' | 'reject',
    target?: ChatViewTarget,
    optionId?: string,
    reason?: string,
  ): Promise<boolean> {
    const viewTarget = deps.normalizeTarget(target)
    const botId = viewTarget.botId.trim()
    const sessionId = viewTarget.sessionId?.trim() ?? ''
    const decisionId = approval.approval_id?.trim() ?? ''
    const run = deps.currentRun(sessionId)
    if (!botId || !sessionId || !decisionId || !run) return false
    if (approval.status !== 'pending' || approval.can_approve === false) return false
    if (!deps.ensureWebSocket(botId)) {
      deps.showError(deps.connectionLostMessage())
      return false
    }

    const transcript = deps.transcriptForTarget(viewTarget)
    const previous = transcript.snapshotToolApprovalStates(decisionId)
    const controlId = deps.createControlId()
    if (!begin({
      controlId,
      decisionId,
      kind: 'tool_approval_response',
      botId,
      sessionId,
      runId: run.run_id,
      rollback: () => transcript.restoreToolApprovalStates(previous),
      optimistic: () => transcript.markToolApprovalDecision(
        decisionId,
        decision === 'approve' ? 'approved' : 'rejected',
      ),
    })) return false

    transcript.markToolApprovalDecision(
      decisionId,
      decision === 'approve' ? 'approved' : 'rejected',
    )
    try {
      if (!deps.send(botId, {
        type: 'tool_approval_response',
        session_id: sessionId,
        run_id: run.run_id,
        decision_id: decisionId,
        control_id: controlId,
        decision,
        option_id: optionId,
        reason: reason?.trim() || undefined,
      })) throw new Error('WebSocket is not connected')
    } catch (error) {
      finish(controlId, true)
      deps.showError(deps.resolveErrorMessage(error, 'Failed to send tool approval response.'))
      return false
    }
    return true
  }

  async function respondUserInput(
    userInput: UIUserInput,
    payload: { answers?: WSUserInputAnswer[]; canceled?: boolean; reason?: string },
    target?: ChatViewTarget,
  ): Promise<void> {
    const viewTarget = deps.normalizeTarget(target)
    const botId = viewTarget.botId.trim()
    const sessionId = viewTarget.sessionId?.trim() ?? ''
    const decisionId = userInput.user_input_id?.trim() ?? ''
    const run = deps.currentRun(sessionId)
    if (!botId || !sessionId || !decisionId || !run) return
    if (userInput.status !== 'pending' || userInput.can_respond === false) return
    if (!deps.ensureWebSocket(botId)) {
      deps.showError(deps.connectionLostMessage())
      return
    }

    const transcript = deps.transcriptForTarget(viewTarget)
    const previous = transcript.snapshotUserInputStates(decisionId)
    const controlId = deps.createControlId()
    if (!begin({
      controlId,
      decisionId,
      kind: 'user_input_response',
      botId,
      sessionId,
      runId: run.run_id,
      rollback: () => transcript.restoreUserInputStates(previous),
      optimistic: () => transcript.markUserInputDecision(
        decisionId,
        payload.canceled ? 'canceled' : 'submitted',
        payload.answers,
      ),
    })) return

    transcript.markUserInputDecision(
      decisionId,
      payload.canceled ? 'canceled' : 'submitted',
      payload.answers,
    )
    try {
      if (!deps.send(botId, {
        type: 'user_input_response',
        session_id: sessionId,
        run_id: run.run_id,
        decision_id: decisionId,
        control_id: controlId,
        answers: payload.answers,
        canceled: payload.canceled === true,
        reason: payload.reason,
      })) throw new Error('WebSocket is not connected')
    } catch (error) {
      finish(controlId, true)
      deps.showError(deps.resolveErrorMessage(error, 'Failed to send user input response.'))
    }
  }

  function reset() {
    pendingByControlId.clear()
    pendingControlByDecision.clear()
    pendingAborts.clear()
  }

  return {
    respondToolApproval,
    respondUserInput,
    trackAbort,
    handleControlAck,
    observeRun,
    reset,
  }
}
