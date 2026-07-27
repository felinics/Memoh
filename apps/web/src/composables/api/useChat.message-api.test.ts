import { beforeEach, describe, expect, it, vi } from 'vitest'

// The SDK functions are mocked at the module level so we can hand them an
// AsyncGenerator and pin the contract that the wrappers `await` the SDK call
// before destructuring `stream`. Forgetting the `await` once shipped the bug
// where `stream` was a Promise and `for await` on it threw synchronously
// inside `consumeSSE`; the store-level test suite missed that regression
// because it mocks the wrapper, not the SDK underneath it.
vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdSessionsEvents: vi.fn(),
  getBotsByBotIdMessages: vi.fn(),
  getBotsByBotIdMessagesLocate: vi.fn(),
}))

vi.mock('@memohai/sdk/client', () => ({
  client: { get: vi.fn(), post: vi.fn(), setConfig: vi.fn() },
}))

import { getBotsByBotIdSessionsEvents } from '@memohai/sdk'

import { streamBotSessionsActivityEvents } from './useChat.message-api'

async function* singleEventStream(event: unknown) {
  yield event
}

describe('streamBotSessionsActivityEvents', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('awaits the SDK call before iterating its stream and forwards each typed event', async () => {
    const event = { type: 'session_touched', session_id: 's-1', last_activity_at: '2026-06-19T00:00:00Z' }
    vi.mocked(getBotsByBotIdSessionsEvents).mockResolvedValue({
      stream: singleEventStream(event),
    } as never)

    const onEvent = vi.fn()
    const controller = new AbortController()
    await streamBotSessionsActivityEvents('bot-1', controller.signal, onEvent)

    expect(onEvent).toHaveBeenCalledTimes(1)
    expect(onEvent).toHaveBeenCalledWith(event)
  })
})
