import { describe, expect, it, vi } from 'vitest'
import type {
  BotSessionActivityEvent,
  ChatWebSocket,
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
  start: (attempt: (signal: AbortSignal) => Promise<void>) => void
  stop: () => void
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

function createSocket(connected = true): ChatWebSocket & {
  send: ReturnType<typeof vi.fn>
  abort: ReturnType<typeof vi.fn>
  close: ReturnType<typeof vi.fn>
} {
  return {
    connected,
    send: vi.fn<(message: WSClientMessage) => void>(),
    abort: vi.fn<(runId: string, sessionId: string, controlId: string) => void>(),
    close: vi.fn<() => void>(),
    onOpen: null,
    onClose: null,
  } as ChatWebSocket & {
    send: ReturnType<typeof vi.fn>
    abort: ReturnType<typeof vi.fn>
    close: ReturnType<typeof vi.fn>
  }
}

function makeController(options: { socketConnected?: boolean } = {}) {
  const sockets: Array<{
    botId: string
    handler: (event: UIStreamEvent) => void
    socket: ReturnType<typeof createSocket>
  }> = []
  const retryingStreams: FakeRetryingStream[] = []
  const activityHandlers: Array<(event: BotSessionActivityEvent) => void> = []
  const callbacks: ChatRealtimeCallbacks = {
    onWebSocketEvent: vi.fn(),
    prepareSessionRuntime: vi.fn(async (_botId, _sessionId, commitInitialHistory) => {
      await commitInitialHistory(() => {})
    }),
    onRuntimeProjection: vi.fn(),
    onBotSessionsActivityEvent: vi.fn(),
  }
  const transport: ChatRealtimeTransport = {
    connectWebSocket: vi.fn((botId, handler) => {
      const socket = createSocket(options.socketConnected ?? true)
      sockets.push({ botId, handler, socket })
      return socket
    }),
    streamBotSessionsActivityEvents: vi.fn(async (_botId, _signal, handler) => {
      activityHandlers.push(handler)
    }),
    createRetryingStream: () => {
      const stream = createFakeRetryingStream()
      retryingStreams.push(stream)
      return stream
    },
  }
  const controller = createChatRealtimeController(callbacks, transport)
  return {
    callbacks,
    sockets,
    activityHandlers,
    retryingStreams,
    controller,
  }
}

async function flushPromises() {
  await Promise.resolve()
  await Promise.resolve()
}

describe('chat realtime controller', () => {
  it('closes the previous websocket and rejects events from its generation', () => {
    const { controller, callbacks, sockets } = makeController()
    controller.startWebSocket('bot-1')
    const first = sockets[0]!
    controller.startWebSocket('bot-2')

    expect(first.socket.close).toHaveBeenCalledOnce()
    first.handler({
      type: 'run_rejected',
      invocation_id: 'stale',
      session_id: 'session-1',
      code: 'session_runtime.session_busy',
      message: 'busy',
    })
    sockets[1]!.handler({
      type: 'run_rejected',
      invocation_id: 'current',
      session_id: 'session-1',
      code: 'session_runtime.session_busy',
      message: 'busy',
    })

    expect(callbacks.onWebSocketEvent).toHaveBeenCalledOnce()
    expect(callbacks.onWebSocketEvent).toHaveBeenCalledWith(
      'bot-2',
      expect.objectContaining({ invocation_id: 'current' }),
    )
  })

  it('sends commands only through the matching connected bot', () => {
    const { controller, sockets } = makeController()
    const message: WSClientMessage = {
      type: 'message',
      invocation_id: 'invocation-1',
      text: 'hello',
    }

    expect(controller.sendWebSocketMessage('bot-1', message)).toBe(true)
    expect(sockets[0]!.socket.send).toHaveBeenCalledWith(message)
    expect(controller.sendWebSocketMessage('bot-2', message)).toBe(true)
    expect(sockets[0]!.socket.close).toHaveBeenCalledOnce()
    expect(sockets[1]!.socket.send).toHaveBeenCalledWith(message)
  })

  it('queues sends through a still-connecting socket instead of refusing them (#1070)', () => {
    const { controller, sockets } = makeController({ socketConnected: false })
    const message: WSClientMessage = {
      type: 'message',
      invocation_id: 'invocation-1',
      text: 'hello',
    }

    // The socket exists but has not finished its first-open handshake; the ws
    // layer queues the payload and flushes it on open, so the send must pass
    // through rather than fail with "WebSocket is not connected".
    expect(controller.sendWebSocketMessage('bot-1', message)).toBe(true)
    expect(sockets[0]!.socket.send).toHaveBeenCalledWith(message)
  })

  it('aborts only a connected websocket for the matching bot', () => {
    const { controller, sockets } = makeController()
    controller.startWebSocket('bot-1')

    expect(controller.abortWebSocketRun(
      'run-1',
      'bot-2',
      'session-1',
      'control-1',
    )).toBe(false)
    expect(controller.abortWebSocketRun(
      'run-1',
      'bot-1',
      'session-1',
      'control-1',
    )).toBe(true)
    expect(sockets[0]!.socket.abort).toHaveBeenCalledWith(
      'run-1',
      'session-1',
      'control-1',
    )
  })

  it('hydrates once, then subscribes to the session runtime on the websocket', async () => {
    const { controller, callbacks, sockets } = makeController()
    controller.startWebSocket('bot-1')
    controller.startSessionRuntime('bot-1', 'session-1')
    controller.startSessionRuntime('bot-1', 'session-1')
    await flushPromises()

    expect(callbacks.prepareSessionRuntime).toHaveBeenCalledOnce()
    expect(sockets[0]!.socket.send).toHaveBeenCalledWith({
      type: 'runtime_subscribe',
      session_id: 'session-1',
    })
  })

  it('routes runtime frames through the ordered projection client', async () => {
    const { controller, callbacks, sockets } = makeController()
    controller.startWebSocket('bot-1')
    controller.startSessionRuntime('bot-1', 'session-1')
    await flushPromises()

    sockets[0]!.handler({
      type: 'runtime_snapshot',
      session_id: 'session-1',
      epoch: 'epoch-1',
      seq: 1,
      snapshot: {
        bot_id: 'bot-1',
        session_id: 'session-1',
        epoch: 'epoch-1',
        seq: 1,
        updated_at: '2026-07-27T08:00:00.000Z',
      },
    })
    await flushPromises()

    expect(callbacks.onRuntimeProjection).toHaveBeenCalledOnce()
    expect(controller.runtimeProjection('session-1')).toMatchObject({
      epoch: 'epoch-1',
      seq: 1,
    })
  })

  it('holds fetched history until the initial runtime snapshot is ready', async () => {
    const { controller, callbacks, sockets } = makeController()
    const phases: string[] = []
    callbacks.onRuntimeProjection = vi.fn(() => phases.push('runtime'))
    callbacks.prepareSessionRuntime = vi.fn(async (_botId, _sessionId, commitInitialHistory) => {
      phases.push('history-fetched')
      await commitInitialHistory(() => phases.push('history'))
      phases.push('prepared')
    })
    controller.startWebSocket('bot-1')
    controller.startSessionRuntime('bot-1', 'session-1')
    await flushPromises()

    expect(phases).toEqual(['history-fetched'])
    expect(callbacks.onRuntimeProjection).not.toHaveBeenCalled()

    sockets[0]!.handler({
      type: 'runtime_snapshot',
      session_id: 'session-1',
      epoch: 'epoch-1',
      seq: 1,
      snapshot: {
        bot_id: 'bot-1',
        session_id: 'session-1',
        epoch: 'epoch-1',
        seq: 1,
        updated_at: '2026-07-27T08:00:00.000Z',
      },
    })
    await flushPromises()

    expect(phases).toEqual(['history-fetched', 'history', 'runtime', 'prepared'])
  })

  it('holds the initial runtime snapshot until fetched history can commit', async () => {
    const { controller, callbacks, sockets } = makeController()
    const phases: string[] = []
    let finishHistoryFetch!: () => void
    const historyFetched = new Promise<void>((resolve) => {
      finishHistoryFetch = resolve
    })
    callbacks.onRuntimeProjection = vi.fn(() => phases.push('runtime'))
    callbacks.prepareSessionRuntime = vi.fn(async (_botId, _sessionId, commitInitialHistory) => {
      await historyFetched
      await commitInitialHistory(() => phases.push('history'))
    })
    controller.startWebSocket('bot-1')
    controller.startSessionRuntime('bot-1', 'session-1')

    sockets[0]!.handler({
      type: 'runtime_snapshot',
      session_id: 'session-1',
      epoch: 'epoch-1',
      seq: 1,
      snapshot: {
        bot_id: 'bot-1',
        session_id: 'session-1',
        epoch: 'epoch-1',
        seq: 1,
        updated_at: '2026-07-27T08:00:00.000Z',
      },
    })
    await flushPromises()

    expect(phases).toEqual([])
    expect(callbacks.onRuntimeProjection).not.toHaveBeenCalled()

    finishHistoryFetch()
    await flushPromises()

    expect(phases).toEqual(['history', 'runtime'])
  })

  it('releases a pending hydration when its runtime subscription stops', async () => {
    const { controller, callbacks } = makeController()
    const prepared = vi.fn()
    callbacks.prepareSessionRuntime = vi.fn(async (_botId, _sessionId, commitInitialHistory) => {
      await commitInitialHistory(() => {})
      prepared()
    })
    controller.startWebSocket('bot-1')
    controller.startSessionRuntime('bot-1', 'session-1')

    controller.stopSessionRuntime('bot-1', 'session-1')
    await flushPromises()

    expect(prepared).toHaveBeenCalledOnce()
    expect(callbacks.onRuntimeProjection).not.toHaveBeenCalled()
  })

  it('unsubscribes a hidden session without stopping another session', async () => {
    const { controller, sockets } = makeController()
    controller.startWebSocket('bot-1')
    controller.startSessionRuntime('bot-1', 'session-1')
    controller.startSessionRuntime('bot-1', 'session-2')
    await flushPromises()

    controller.stopSessionRuntime('bot-1', 'session-1')
    expect(sockets[0]!.socket.send).toHaveBeenCalledWith({
      type: 'runtime_unsubscribe',
      session_id: 'session-1',
    })
    expect(sockets[0]!.socket.send).not.toHaveBeenCalledWith({
      type: 'runtime_unsubscribe',
      session_id: 'session-2',
    })
  })

  it('suppresses bot activity from a stopped generation', async () => {
    const { controller, callbacks, retryingStreams, activityHandlers } = makeController()
    controller.startBotSessionsActivityStream('bot-1')
    await retryingStreams[0]!.attempt!(new AbortController().signal)
    const staleHandler = activityHandlers[0]!

    controller.stopStreams()
    staleHandler({ type: 'ping' })

    expect(callbacks.onBotSessionsActivityEvent).not.toHaveBeenCalled()
  })
})
