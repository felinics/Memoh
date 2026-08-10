import { describe, expect, it, vi } from 'vitest'
import type {
  RuntimeCurrentRunView,
  UIControlAckEvent,
  UIToolApproval,
  UIUserInput,
  WSClientMessage,
} from '@/composables/api/useChat'
import type { ChatViewTarget } from './types'
import type { createTranscriptController } from './transcript'
import { createChatDecisions } from './decisions'

type Transcript = ReturnType<typeof createTranscriptController>

function run(messages: RuntimeCurrentRunView['messages'] = []): RuntimeCurrentRunView {
  return {
    run_id: 'run-1',
    turn_id: 'turn-1',
    generation: 'generation-1',
    status: 'running',
    started_at: '2026-07-27T08:00:00Z',
    updated_at: '2026-07-27T08:00:00Z',
    messages,
  }
}

function setup(
  currentRun: RuntimeCurrentRunView | null = run(),
  options: {
    connected?: boolean
    sendResult?: boolean
    sendError?: Error
  } = {},
) {
  const sent: WSClientMessage[] = []
  const transcript = {
    snapshotToolApprovalStates: vi.fn(() => ['approval-snapshot']),
    restoreToolApprovalStates: vi.fn(),
    markToolApprovalDecision: vi.fn(),
    snapshotUserInputStates: vi.fn(() => ['input-snapshot']),
    restoreUserInputStates: vi.fn(),
    markUserInputDecision: vi.fn(),
  } as unknown as Transcript
  const showError = vi.fn()
  let controlSequence = 0
  const decisions = createChatDecisions({
    normalizeTarget: target => target ?? {
      botId: 'bot-1',
      sessionId: 'session-1',
      viewId: 'chat',
    },
    transcriptForTarget: () => transcript,
    currentRun: () => currentRun,
    ensureConnected: () => options.connected ?? true,
    send: (_botId, message) => {
      if (options.sendError) throw options.sendError
      sent.push(message)
      return options.sendResult ?? true
    },
    createControlId: () => `control-${++controlSequence}`,
    connectionLostMessage: () => 'connection lost',
    resolveErrorMessage: (_error, fallback) => fallback,
    showError,
  })
  return { decisions, sent, transcript, showError }
}

const target: ChatViewTarget = {
  botId: 'bot-1',
  sessionId: 'session-1',
  viewId: 'chat',
}

describe('chat decisions', () => {
  it('sends an approval as a control on the existing run and deduplicates it', async () => {
    const { decisions, sent, transcript } = setup()
    const approval: UIToolApproval = {
      approval_id: 'approval-1',
      short_id: 1,
      status: 'pending',
      can_approve: true,
    }

    await expect(decisions.respondToolApproval(approval, 'approve', target)).resolves.toBe(true)
    await expect(decisions.respondToolApproval(approval, 'approve', target)).resolves.toBe(false)

    expect(sent).toEqual([expect.objectContaining({
      type: 'tool_approval_response',
      session_id: 'session-1',
      run_id: 'run-1',
      decision_id: 'approval-1',
      control_id: 'control-1',
      decision: 'approve',
    })])
    expect(transcript.markToolApprovalDecision).toHaveBeenCalledWith('approval-1', 'approved')
  })

  it('rolls back a rejected control acknowledgement', async () => {
    const { decisions, transcript, showError } = setup()
    const approval: UIToolApproval = {
      approval_id: 'approval-1',
      status: 'pending',
      can_approve: true,
    }
    await decisions.respondToolApproval(approval, 'reject', target)

    expect(decisions.handleControlAck({
      type: 'control_ack',
      session_id: 'session-1',
      run_id: 'run-1',
      control: 'tool_approval_response',
      control_id: 'control-1',
      applied: false,
      code: 'decision_already_answered',
    } satisfies UIControlAckEvent)).toBe(true)
    expect(transcript.restoreToolApprovalStates).toHaveBeenCalledWith(['approval-snapshot'])
    expect(showError).toHaveBeenCalledWith('The response was not applied.')
  })

  it('matches abort acknowledgements by control, session and run', () => {
    const { decisions, showError } = setup()
    decisions.trackAbort('control-abort', 'session-1', 'run-1')

    expect(decisions.handleControlAck({
      type: 'control_ack',
      session_id: 'session-other',
      run_id: 'run-1',
      control: 'abort',
      control_id: 'control-abort',
      applied: false,
      code: 'runtime_control.target_mismatch',
    })).toBe(false)
    expect(showError).not.toHaveBeenCalled()

    expect(decisions.handleControlAck({
      type: 'control_ack',
      session_id: 'session-1',
      run_id: 'run-1',
      control: 'abort',
      control_id: 'control-abort',
      applied: false,
      code: 'runtime_control.target_mismatch',
    })).toBe(true)
    expect(showError).toHaveBeenCalledWith('The response was not applied.')
  })

  it('treats an unapplied acknowledgement without a code as a resolved no-op', async () => {
    const { decisions, transcript, showError } = setup()
    const approval: UIToolApproval = {
      approval_id: 'approval-1',
      status: 'pending',
      can_approve: true,
    }
    await decisions.respondToolApproval(approval, 'approve', target)

    expect(decisions.handleControlAck({
      type: 'control_ack',
      session_id: 'session-1',
      run_id: 'run-1',
      control: 'tool_approval_response',
      control_id: 'control-1',
      applied: false,
    })).toBe(true)
    expect(transcript.restoreToolApprovalStates).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
  })

  it('keeps an applied acknowledgement optimistic until projection confirms it', async () => {
    const current = run([{
      id: 1,
      type: 'tool',
      name: 'exec',
      input: {},
      tool_call_id: 'call-1',
      running: false,
      approval: {
        approval_id: 'approval-1',
        status: 'pending',
        can_approve: true,
      },
    }])
    const { decisions, transcript } = setup(current)
    const approval: UIToolApproval = {
      approval_id: 'approval-1',
      status: 'pending',
      can_approve: true,
    }
    await decisions.respondToolApproval(approval, 'approve', target)

    decisions.handleControlAck({
      type: 'control_ack',
      session_id: 'session-1',
      run_id: 'run-1',
      control: 'tool_approval_response',
      control_id: 'control-1',
      applied: true,
    })
    decisions.observeRun('session-1', current)
    expect(transcript.markToolApprovalDecision).toHaveBeenCalledTimes(2)

    const approvalMessage = current.messages[0]
    if (!approvalMessage || approvalMessage.type !== 'tool') {
      throw new Error('expected approval tool message')
    }
    current.messages[0] = {
      ...approvalMessage,
      approval: {
        approval_id: 'approval-1',
        status: 'approved' as const,
        can_approve: false,
      },
    }
    decisions.observeRun('session-1', current)
    decisions.observeRun('session-1', run())
    expect(transcript.markToolApprovalDecision).toHaveBeenCalledTimes(2)
    expect(transcript.restoreToolApprovalStates).not.toHaveBeenCalled()
  })

  it('does not mutate approval state while disconnected', async () => {
    const { decisions, sent, transcript, showError } = setup(run(), {
      connected: false,
    })
    const approval: UIToolApproval = {
      approval_id: 'approval-1',
      status: 'pending',
      can_approve: true,
    }

    await expect(decisions.respondToolApproval(approval, 'approve', target))
      .resolves.toBe(false)
    expect(sent).toEqual([])
    expect(transcript.markToolApprovalDecision).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('connection lost')
  })

  it('rolls back approval state when sending the control fails', async () => {
    const { decisions, transcript, showError } = setup(run(), {
      sendResult: false,
    })
    const approval: UIToolApproval = {
      approval_id: 'approval-1',
      status: 'pending',
      can_approve: true,
    }

    await expect(decisions.respondToolApproval(approval, 'approve', target))
      .resolves.toBe(false)
    expect(transcript.restoreToolApprovalStates).toHaveBeenCalledWith(['approval-snapshot'])
    expect(showError).toHaveBeenCalledWith('Failed to send tool approval response.')
  })

  it('sends user input with decision and control identities', async () => {
    const { decisions, sent, transcript } = setup()
    const userInput: UIUserInput = {
      user_input_id: 'input-1',
      status: 'pending',
      can_respond: true,
    }

    await decisions.respondUserInput(userInput, {
      answers: [{ question_id: 'q1', option_ids: ['yes'] }],
    }, target)

    expect(sent).toEqual([expect.objectContaining({
      type: 'user_input_response',
      session_id: 'session-1',
      run_id: 'run-1',
      decision_id: 'input-1',
      control_id: 'control-1',
    })])
    expect(transcript.markUserInputDecision).toHaveBeenCalledWith(
      'input-1',
      'submitted',
      [{ question_id: 'q1', option_ids: ['yes'] }],
    )
  })

  it('keeps a user-input decision optimistic while the run waits for it', async () => {
    const current = run([{
      id: 1,
      type: 'tool',
      name: 'ask_user',
      input: {},
      tool_call_id: 'call-input',
      running: false,
      user_input: {
        user_input_id: 'input-1',
        status: 'pending',
        can_respond: true,
      },
    }])
    current.status = 'waiting_decision'
    const { decisions, sent, transcript } = setup(current)
    const userInput: UIUserInput = {
      user_input_id: 'input-1',
      status: 'pending',
      can_respond: true,
    }
    const answer = [{ question_id: 'q1', option_ids: ['yes'] }]

    await decisions.respondUserInput(userInput, { answers: answer }, target)
    decisions.observeRun('session-1', current)
    await decisions.respondUserInput(userInput, { answers: answer }, target)

    expect(sent).toHaveLength(1)
    expect(transcript.markUserInputDecision).toHaveBeenCalledTimes(2)
    expect(transcript.restoreUserInputStates).not.toHaveBeenCalled()
  })

  it('marks canceled user input optimistically on the existing run', async () => {
    const { decisions, sent, transcript } = setup()
    const userInput: UIUserInput = {
      user_input_id: 'input-1',
      status: 'pending',
      can_respond: true,
    }

    await decisions.respondUserInput(userInput, { canceled: true }, target)

    expect(sent).toEqual([expect.objectContaining({
      type: 'user_input_response',
      run_id: 'run-1',
      canceled: true,
    })])
    expect(transcript.markUserInputDecision).toHaveBeenCalledWith('input-1', 'canceled', undefined)
  })

  it('does not create a decision command without an authoritative current run', async () => {
    const { decisions, sent } = setup(null)
    const approval: UIToolApproval = {
      approval_id: 'approval-1',
      status: 'pending',
      can_approve: true,
    }

    await expect(decisions.respondToolApproval(approval, 'approve', target)).resolves.toBe(false)
    expect(sent).toEqual([])
  })
})
