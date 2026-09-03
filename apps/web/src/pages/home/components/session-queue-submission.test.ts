import { describe, expect, it, vi } from 'vitest'
import { SessionQueueSubmissionGate, type SessionQueueSubmissionInput } from './session-queue-submission'

const input: SessionQueueSubmissionInput = {
  botId: 'bot-1',
  sessionId: 'session-1',
  mode: 'steer',
  text: 'change direction',
}

const invocation1 = '00000000-0000-4000-8000-000000000001' as const
const invocation2 = '00000000-0000-4000-8000-000000000002' as const
const invocation3 = '00000000-0000-4000-8000-000000000003' as const

describe('SessionQueueSubmissionGate', () => {
  it('allows only one in-flight submission', () => {
    const createInvocationId = vi.fn(() => invocation1)
    const gate = new SessionQueueSubmissionGate(createInvocationId)

    expect(gate.begin(input)?.invocationId).toBe(invocation1)
    expect(gate.begin(input)).toBeNull()
    expect(createInvocationId).toHaveBeenCalledOnce()
  })

  it('reuses the invocation id when the same failed submission is retried', () => {
    const createInvocationId = vi.fn()
      .mockReturnValueOnce(invocation1)
      .mockReturnValueOnce(invocation2)
    const gate = new SessionQueueSubmissionGate(createInvocationId)

    const first = gate.begin(input)!
    gate.fail(first)

    expect(gate.begin(input)?.invocationId).toBe(invocation1)
    expect(createInvocationId).toHaveBeenCalledOnce()
  })

  it('uses a new identity after success or for a different queue input', () => {
    const createInvocationId = vi.fn()
      .mockReturnValueOnce(invocation1)
      .mockReturnValueOnce(invocation2)
      .mockReturnValueOnce(invocation3)
    const gate = new SessionQueueSubmissionGate(createInvocationId)

    const first = gate.begin(input)!
    gate.succeed(first)
    const second = gate.begin(input)!
    expect(second.invocationId).toBe(invocation2)

    gate.fail(second)
    expect(gate.begin({ ...input, mode: 'follow-up' })?.invocationId).toBe(invocation3)
  })
})
