export type ApprovalResponseOutcome = 'succeeded' | 'failed' | 'canceled' | 'expired'

export interface ApprovalResponse {
  readonly invocationId: string
  readonly approvalId: string
  readonly botId: string
  readonly sessionId: string
  readonly silent: boolean
}

export interface BeginApprovalResponseInput {
  invocationId: string
  approvalId: string
  botId: string
  sessionId: string
  silent: boolean
  rollback?: () => void
}

interface PendingApprovalResponse extends ApprovalResponse {
  startedAt: number
  cancelExpiry?: () => void
  rollback?: () => void
}

type ScheduleExpiry = (callback: () => void, delayMs: number) => () => void

interface ApprovalResponseTrackerDeps {
  rollbackApproval: (approvalId: string) => void
  now?: () => number
  ttlMs?: number
  terminalHistoryLimit?: number
  scheduleExpiry?: ScheduleExpiry
  onExpired?: (response: ApprovalResponse) => void
}

const DEFAULT_TTL_MS = 2 * 60 * 1000
const DEFAULT_TERMINAL_HISTORY_LIMIT = 512

const defaultScheduleExpiry: ScheduleExpiry = (callback, delayMs) => {
  const timer = setTimeout(callback, delayMs)
  return () => clearTimeout(timer)
}

export function createApprovalResponseTracker({
  rollbackApproval,
  now = Date.now,
  ttlMs = DEFAULT_TTL_MS,
  terminalHistoryLimit = DEFAULT_TERMINAL_HISTORY_LIMIT,
  scheduleExpiry = defaultScheduleExpiry,
  onExpired = () => {},
}: ApprovalResponseTrackerDeps) {
  const responses = new Map<string, PendingApprovalResponse>()
  const terminalResponseIds = new Set<string>()

  function rememberTerminalResponse(invocationId: string) {
    terminalResponseIds.add(invocationId)
    if (terminalResponseIds.size <= terminalHistoryLimit) return
    const oldest = terminalResponseIds.values().next().value
    if (oldest) terminalResponseIds.delete(oldest)
  }

  function expireResponse(invocationId: string) {
    const response = responses.get(invocationId)
    if (!response) return
    const remaining = ttlMs - (now() - response.startedAt)
    if (remaining > 0) {
      response.cancelExpiry = scheduleExpiry(() => expireResponse(invocationId), remaining)
      return
    }
    const expired = settleApprovalResponse(invocationId, 'expired')
    if (expired) onExpired(expired)
  }

  function expireStaleResponses() {
    const currentTime = now()
    for (const [invocationId, response] of responses) {
      if (currentTime - response.startedAt < ttlMs) continue
      expireResponse(invocationId)
    }
  }

  function hasPendingApprovalResponse(approvalId: string): boolean {
    const id = approvalId.trim()
    if (!id) return false
    expireStaleResponses()
    for (const response of responses.values()) {
      if (response.approvalId === id) return true
    }
    return false
  }

  function beginApprovalResponse(input: BeginApprovalResponseInput): boolean {
    const invocationId = input.invocationId.trim()
    const approvalId = input.approvalId.trim()
    const botId = input.botId.trim()
    const sessionId = input.sessionId.trim()
    if (!invocationId || !approvalId || !botId || !sessionId) return false
    expireStaleResponses()
    if (responses.has(invocationId) || hasPendingApprovalResponse(approvalId)) return false
    if (terminalResponseIds.has(invocationId)) return false
    const response: PendingApprovalResponse = {
      invocationId,
      approvalId,
      botId,
      sessionId,
      silent: input.silent,
      startedAt: now(),
      rollback: input.rollback,
    }
    responses.set(invocationId, response)
    response.cancelExpiry = scheduleExpiry(() => expireResponse(invocationId), ttlMs)
    return true
  }

  function getApprovalResponse(invocationId: string): ApprovalResponse | undefined {
    return responses.get(invocationId.trim())
  }

  function settleApprovalResponse(invocationId: string, outcome: ApprovalResponseOutcome): ApprovalResponse | undefined {
    const id = invocationId.trim()
    const response = responses.get(id)
    if (!response) return undefined
    responses.delete(id)
    response.cancelExpiry?.()
    rememberTerminalResponse(id)
    if (outcome === 'failed' || outcome === 'expired') {
      if (response.rollback) response.rollback()
      else rollbackApproval(response.approvalId)
    }
    return response
  }

  function pendingApprovalResponses(): ApprovalResponse[] {
    return [...responses.values()]
  }

  function pendingApprovalResponsesForSession(botId: string, sessionId: string): ApprovalResponse[] {
    const bid = botId.trim()
    const sid = sessionId.trim()
    if (!bid || !sid) return []
    return pendingApprovalResponses().filter(response => response.botId === bid && response.sessionId === sid)
  }

  function discardAllApprovalResponses(): ApprovalResponse[] {
    const pending = pendingApprovalResponses()
    for (const response of pending) settleApprovalResponse(response.invocationId, 'canceled')
    return pending
  }

  function isTerminalApprovalResponse(invocationId: string | undefined): boolean {
    const id = invocationId?.trim()
    return Boolean(id && terminalResponseIds.has(id))
  }

  function resetApprovalResponses() {
    for (const response of responses.values()) response.cancelExpiry?.()
    responses.clear()
    terminalResponseIds.clear()
  }

  return {
    hasPendingApprovalResponse,
    beginApprovalResponse,
    getApprovalResponse,
    settleApprovalResponse,
    pendingApprovalResponses,
    pendingApprovalResponsesForSession,
    discardAllApprovalResponses,
    isTerminalApprovalResponse,
    resetApprovalResponses,
  }
}
