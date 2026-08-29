export type SessionQueueSubmissionMode = 'steer' | 'follow-up'
export type SessionQueueInvocationId = `${string}-${string}-${string}-${string}-${string}`

export interface SessionQueueSubmissionInput {
  botId: string
  sessionId: string
  mode: SessionQueueSubmissionMode
  text: string
}

export interface SessionQueueSubmission {
  key: string
  invocationId: SessionQueueInvocationId
}

function submissionKey(input: SessionQueueSubmissionInput): string {
  return JSON.stringify([input.botId, input.sessionId, input.mode, input.text])
}

/**
 * Gives one composer gesture one durable idempotency identity. A second event
 * cannot enter while the first request is in flight, and an uncertain failure
 * retains its identity so retrying the same input replays instead of enqueueing
 * a second durable item.
 */
export class SessionQueueSubmissionGate {
  private active: SessionQueueSubmission | null = null
  private retry: SessionQueueSubmission | null = null
  private readonly createInvocationId: () => SessionQueueInvocationId

  constructor(createInvocationId: () => SessionQueueInvocationId = () => crypto.randomUUID()) {
    this.createInvocationId = createInvocationId
  }

  begin(input: SessionQueueSubmissionInput): SessionQueueSubmission | null {
    if (this.active) return null

    const key = submissionKey(input)
    const submission = this.retry?.key === key
      ? this.retry
      : { key, invocationId: this.createInvocationId() }
    this.active = submission
    return submission
  }

  succeed(submission: SessionQueueSubmission): void {
    if (this.active !== submission) return
    this.active = null
    this.retry = null
  }

  fail(submission: SessionQueueSubmission): void {
    if (this.active !== submission) return
    this.active = null
    this.retry = submission
  }
}
