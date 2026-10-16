import { client } from '@memohai/sdk/client'
import { parseMemohError, renderI18nMessage, resolveApiErrorMessage } from '@/utils/api-error'

export interface SSEErrorEvent {
  type: 'error'
  code?: string
  args?: Record<string, unknown>
  detail?: string
  message: string
  request_id?: string
}

export function isSSEErrorEvent(value: unknown): value is SSEErrorEvent {
  if (!value || typeof value !== 'object') return false
  const event = value as Record<string, unknown>
  return event.type === 'error'
    && typeof event.message === 'string'
    && (event.detail === undefined || typeof event.detail === 'string')
    && (event.code === undefined || typeof event.code === 'string')
    && (event.args === undefined || (!!event.args && typeof event.args === 'object' && !Array.isArray(event.args)))
    && (event.request_id === undefined || typeof event.request_id === 'string')
}

export function localizeSSEErrorEvent<T extends SSEErrorEvent>(event: T): T {
  return {
    ...event,
    message: resolveApiErrorMessage(event, renderI18nMessage('errors.internal')),
  }
}

export function normalizeSSEFailure(error: unknown, fallback: string): unknown {
  if (parseMemohError(error)) return error
  if (error instanceof Error) return error
  if (typeof error === 'string' && error.trim()) return new Error(error)
  // Legacy servers reject with `{"message": ...}` and no code; keep the object
  // so callers can still read its message and the status fetchSSEProblem added.
  if (error && typeof error === 'object') {
    const record = error as Record<string, unknown>
    if (typeof record.message === 'string' || typeof record.status === 'number') return error
  }
  return new Error(fallback)
}

// The generated SSE client does not decode a non-2xx response body before
// reporting the connection failure. Decode Problem Details at this shared
// boundary so every SSE endpoint preserves the same structured error contract.
export const fetchSSEProblem: typeof fetch = async (input, init) => {
  const configuredFetch = client.getConfig().fetch ?? globalThis.fetch
  const response = await configuredFetch(input, init)
  if (response.ok) return response

  let body: unknown
  try {
    body = await response.clone().json()
  } catch {
    return response
  }
  if (body && typeof body === 'object' && !Array.isArray(body)) {
    throw { ...body, status: response.status }
  }
  return response
}
