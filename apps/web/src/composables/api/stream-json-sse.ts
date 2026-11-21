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
  signal?: AbortSignal | null
  invalidEventMessage: string
  failureMessage: string
  isEvent: (value: unknown) => value is TEvent
  open: (request: JsonSseRequestOptions) => Promise<{ stream: unknown }>
  normalizeEvent?: (event: TEvent) => TEvent
}

/**
 * Opens one JSON-SSE stream immediately and wraps the returned generator with
 * Problem Details error decoding (`fetchSSEProblem`), frame validation, stale
 * error clearing on valid frames, `error` envelope localization, and
 * mid-stream failure normalization.
 */
export async function openJsonSseStream<TEvent extends { type: string }>(
  config: StreamJsonSseConfig<TEvent>,
): Promise<{ stream: AsyncGenerator<TEvent, void, unknown> }> {
  let streamError: unknown
  const request: JsonSseRequestOptions = {
    headers: { Accept: 'text/event-stream' },
    signal: config.signal ?? undefined,
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

  return {
    stream: (async function* () {
      for await (const raw of result.stream as AsyncGenerator<unknown, void, unknown>) {
        if (!config.isEvent(raw)) throw new Error(config.invalidEventMessage)
        streamError = undefined
        const localized = raw.type === 'error'
          ? (localizeSSEErrorEvent(raw as unknown as SSEErrorEvent) as unknown as TEvent)
          : raw
        yield config.normalizeEvent ? config.normalizeEvent(localized) : localized
      }

      if (streamError) {
        throw normalizeSSEFailure(streamError, config.failureMessage)
      }
    })(),
  }
}

/**
 * Shared JSON-SSE consumption loop for lazy generator callers such as
 * workspace App and Dependency streams.
 */
export async function* streamJsonSse<TEvent extends { type: string }>(
  config: StreamJsonSseConfig<TEvent>,
): AsyncGenerator<TEvent, void, unknown> {
  const { stream } = await openJsonSseStream(config)
  yield* stream
}
