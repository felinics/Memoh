import {
  fetchSSEProblem,
  localizeSSEErrorEvent,
  normalizeSSEFailure,
  type SSEErrorEvent,
} from './sse-error'

export interface JsonSseRequestOptions {
  headers: { Accept: 'text/event-stream' }
  signal: AbortSignal | undefined
  fetch: typeof fetchSSEProblem
  onSseError: (error: unknown) => void
  responseValidator: (data: unknown) => Promise<void>
  sseMaxRetryAttempts: 1
}

export interface StreamJsonSseConfig<TEvent extends { type: string }> {
  signal?: AbortSignal
  invalidEventMessage: string
  failureMessage: string
  isEvent: (value: unknown) => value is TEvent
  open: (request: JsonSseRequestOptions) => Promise<{ stream: unknown }>
  normalizeEvent?: (event: TEvent) => TEvent
}

/**
 * Shared JSON-SSE consumption loop for workspace App and Dependency streams:
 * attaches Problem Details connection error handling (`fetchSSEProblem`),
 * validates every frame, localizes `error` SSE envelopes by stable code, and
 * normalizes mid-stream transport errors after the iterator ends.
 */
export async function* streamJsonSse<TEvent extends { type: string }>(
  config: StreamJsonSseConfig<TEvent>,
): AsyncGenerator<TEvent, void, unknown> {
  let streamError: unknown
  const request: JsonSseRequestOptions = {
    headers: { Accept: 'text/event-stream' },
    signal: config.signal,
    fetch: fetchSSEProblem,
    onSseError: (error: unknown) => {
      streamError = error
    },
    responseValidator: async (data: unknown) => {
      if (!config.isEvent(data)) throw new Error(config.invalidEventMessage)
    },
    sseMaxRetryAttempts: 1,
  }

  const result = await config.open(request)

  for await (const raw of result.stream as AsyncGenerator<unknown, void, unknown>) {
    if (!config.isEvent(raw)) throw new Error(config.invalidEventMessage)
    const localized = raw.type === 'error'
      ? (localizeSSEErrorEvent(raw as unknown as SSEErrorEvent) as unknown as TEvent)
      : raw
    yield config.normalizeEvent ? config.normalizeEvent(localized) : localized
  }

  if (streamError) {
    throw normalizeSSEFailure(streamError, config.failureMessage)
  }
}
