import { describe, expect, it, vi } from 'vitest'
import type {
  BotSessionActivityEvent,
  ChatWebSocket,
  SessionMessageStreamEvent,
  UIStreamEvent,
  WSClientMessage,
} from '@/composables/api/useChat'
import {
  createChatRealtimeController,
  type ChatRealtimeCallbacks,
  type ChatRealtimeTransport,
} from './realtime'

interface FakeRetryingStream {
  attempt: ((signal: AbortSignal) => Promise<void>) | null
  start: ReturnType<typeof vi.fn>
  stop: ReturnType<typeof vi.fn>
}

function createFakeRetryingStream(): FakeRetryingStream {
  const stream: FakeRetryingStream = {
    attempt: null,
    start: vi.fn((attempt: (signal: AbortSignal) => Promise<void>) => {
      stream.attempt = attempt
    }),
    stop: vi.fn(),
  }
  return stream
}

function createSocket(connected = true): ChatWebSocket & { send: ReturnType<typeof vi.fn>, abort: ReturnType<typeof vi.fn>, close: ReturnType<typeof vi.fn> } {
  return {
    connected,
    send: vi.fn(),
    abort: vi.fn(),
    close: vi.fn(),
    onOpen: null,
    onClose: null,
  }
}

function makeController() {
  const sockets: Array<{ botId: string, handler: (event: UIStreamEvent) => void, socket: ReturnType<typeof createSocket> }> = []
  const retryingStreams: FakeRetryingStream[] = []
  const sessionHandlers: Array<{
    botId: string
    sessionId: string
    handler: (event: SessionMessageStreamEvent) => void
  }> = []
  const activityHandlers: Array<(event: BotSessionActivityEvent) => void> = []
  const callbacks: ChatRealtimeCallbacks = {
    onWebSocketEvent: vi.fn(),
    prepareSessionMessages: vi.fn().mockResolvedValue(undefined),
    onSessionMessageEvent: vi.fn(),
    onBotSessionsActivityEvent: vi.fn(),
  }
  const transport: ChatRealtimeTransport = {
    connectWebSocket: vi.fn((botId, handler) => {
      const socket = createSocket()
      sockets.push({ botId, handler, socket })
      return socket
    }),
    streamSessionMessageEvents: vi.fn(async (botId, sessionId, _signal, handler) => {
      sessionHandlers.push({ botId, sessionId, handler })
    }),
    streamBotSessionsActivityEvents: vi.fn(async (_botId, _signal, handler) => {
      activityHandlers.push(handler)
    }),
    createRetryingStream: vi.fn(() => {
      const stream = createFakeRetryingStream()
      retryingStreams.push(stream)
      return stream
    }),
  }
  const controller = createChatRealtimeController(callbacks, transport)
  return {
    callbacks,
    transport,
    sockets,
    sessionHandlers,
    activityHandlers,
    retryingStreams,
    controller,
  }
}

describe('chat realtime controller', () => {
  it('closes the previous websocket and rejects events from its generation', () => {
    const { controller, callbacks, sockets } = makeController()
    controller.startWebSocket('bot-1')
    const first = sockets[0]!
    controller.startWebSocket('bot-2')

    expect(first.socket.close).toHaveBeenCalledOnce()
    first.handler({ type: 'start', run_id: 'stale' } as UIStreamEvent)
    sockets[1]!.handler({ type: 'start', run_id: 'current' } as UIStreamEvent)

    expect(callbacks.onWebSocketEvent).toHaveBeenCalledOnce()
    expect(callbacks.onWebSocketEvent).toHaveBeenCalledWith('bot-2', expect.objectContaining({ run_id: 'current' }))
  })

  it('does not expose a socket and sends only through the matching connected bot', () => {
    const { controller, sockets } = makeController()
    const message: WSClientMessage = { type: 'message', text: 'hello' }

    expect(controller.sendWebSocketMessage('bot-1', message)).toBe(true)
    expect(sockets[0]!.socket.send).toHaveBeenCalledWith(message)
    expect(controller.ensureWebSocketConnected('bot-1')).toBe(true)
    expect(sockets).toHaveLength(1)

    expect(controller.sendWebSocketMessage('bot-2', message)).toBe(true)
    expect(sockets[0]!.socket.close).toHaveBeenCalledOnce()
    expect(sockets[1]!.socket.send).toHaveBeenCalledWith(message)
  })

  it('aborts only a connected websocket for the matching bot', () => {
    const { controller, sockets } = makeController()
    controller.startWebSocket('bot-1')

    expect(controller.abortWebSocketRun('run-1', 'bot-2')).toBe(false)
    expect(controller.abortWebSocketRun('run-1', 'bot-1')).toBe(true)
    expect(sockets[0]!.socket.abort).toHaveBeenCalledOnce()
    expect(sockets[0]!.socket.abort).toHaveBeenCalledWith('run-1')
  })

  it('starts the same session once and prepares every retry attempt', async () => {
    const { controller, callbacks, transport, retryingStreams, sessionHandlers } = makeController()
    controller.startSessionMessagesStream('bot-1', 'session-1')
    controller.startSessionMessagesStream(' bot-1 ', ' session-1 ')

    const sessionRetry = retryingStreams[1]!
    expect(transport.createRetryingStream).toHaveBeenCalledTimes(2)
    expect(sessionRetry.start).toHaveBeenCalledOnce()

    await sessionRetry.attempt!(new AbortController().signal)
    const staleHandler = sessionHandlers[0]!.handler
    await sessionRetry.attempt!(new AbortController().signal)
    expect(callbacks.prepareSessionMessages).toHaveBeenCalledTimes(2)
    expect(callbacks.prepareSessionMessages).toHaveBeenNthCalledWith(1, 'bot-1', 'session-1')
    expect(callbacks.prepareSessionMessages).toHaveBeenNthCalledWith(2, 'bot-1', 'session-1')

    staleHandler({ type: 'ping' } as SessionMessageStreamEvent)
    sessionHandlers[1]!.handler({ type: 'ping' } as SessionMessageStreamEvent)

    expect(callbacks.onSessionMessageEvent).toHaveBeenCalledOnce()
    expect(callbacks.onSessionMessageEvent).toHaveBeenCalledWith('bot-1', 'session-1', { type: 'ping' })
  })

  it('keeps different session streams alive concurrently', async () => {
    const { controller, callbacks, retryingStreams, sessionHandlers } = makeController()
    controller.startSessionMessagesStream('bot-1', 'session-1')
    controller.startSessionMessagesStream('bot-1', 'session-2')

    const firstRetry = retryingStreams[1]!
    const secondRetry = retryingStreams[2]!
    await firstRetry.attempt!(new AbortController().signal)
    await secondRetry.attempt!(new AbortController().signal)

    sessionHandlers.find(item => item.sessionId === 'session-1')!.handler({ type: 'ping' })
    sessionHandlers.find(item => item.sessionId === 'session-2')!.handler({ type: 'ping' })

    expect(firstRetry.stop).not.toHaveBeenCalled()
    expect(secondRetry.stop).not.toHaveBeenCalled()
    expect(callbacks.onSessionMessageEvent).toHaveBeenCalledTimes(2)
    expect(callbacks.onSessionMessageEvent).toHaveBeenCalledWith('bot-1', 'session-1', { type: 'ping' })
    expect(callbacks.onSessionMessageEvent).toHaveBeenCalledWith('bot-1', 'session-2', { type: 'ping' })
  })

  it('stops only the requested session and suppresses its late events', async () => {
    const { controller, callbacks, retryingStreams, sessionHandlers } = makeController()
    controller.startSessionMessagesStream('bot-1', 'session-1')
    controller.startSessionMessagesStream('bot-1', 'session-2')

    const firstRetry = retryingStreams[1]!
    const secondRetry = retryingStreams[2]!
    await firstRetry.attempt!(new AbortController().signal)
    await secondRetry.attempt!(new AbortController().signal)
    const firstHandler = sessionHandlers.find(item => item.sessionId === 'session-1')!.handler
    const secondHandler = sessionHandlers.find(item => item.sessionId === 'session-2')!.handler

    controller.stopSessionMessagesStream('bot-1', 'session-1')
    firstHandler({ type: 'ping' })
    secondHandler({ type: 'ping' })

    expect(firstRetry.stop).toHaveBeenCalledOnce()
    expect(secondRetry.stop).not.toHaveBeenCalled()
    expect(callbacks.onSessionMessageEvent).toHaveBeenCalledOnce()
    expect(callbacks.onSessionMessageEvent).toHaveBeenCalledWith('bot-1', 'session-2', { type: 'ping' })
  })

  it('restarts a stopped session without accepting events from the old connection', async () => {
    const { controller, callbacks, retryingStreams, sessionHandlers } = makeController()
    controller.startSessionMessagesStream('bot-1', 'session-1')
    await retryingStreams[1]!.attempt!(new AbortController().signal)
    const staleHandler = sessionHandlers[0]!.handler

    controller.stopSessionMessagesStream('bot-1', 'session-1')
    controller.startSessionMessagesStream('bot-1', 'session-1')
    await retryingStreams[2]!.attempt!(new AbortController().signal)

    staleHandler({ type: 'ping' })
    sessionHandlers[1]!.handler({ type: 'ping' })

    expect(callbacks.onSessionMessageEvent).toHaveBeenCalledOnce()
    expect(callbacks.onSessionMessageEvent).toHaveBeenCalledWith('bot-1', 'session-1', { type: 'ping' })
  })

  it('stops every session stream when no target is provided', () => {
    const { controller, retryingStreams } = makeController()
    controller.startSessionMessagesStream('bot-1', 'session-1')
    controller.startSessionMessagesStream('bot-1', 'session-2')

    controller.stopSessionMessagesStream()

    expect(retryingStreams[1]!.stop).toHaveBeenCalledOnce()
    expect(retryingStreams[2]!.stop).toHaveBeenCalledOnce()
  })

  it('suppresses bot activity from a stopped generation', async () => {
    const { controller, callbacks, retryingStreams, activityHandlers } = makeController()
    controller.startBotSessionsActivityStream('bot-1')
    const activityRetry = retryingStreams[0]!
    await activityRetry.attempt!(new AbortController().signal)
    const staleHandler = activityHandlers[0]!

    controller.stopStreams()
    staleHandler({ type: 'ping' } as BotSessionActivityEvent)

    expect(callbacks.onBotSessionsActivityEvent).not.toHaveBeenCalled()
  })
})
