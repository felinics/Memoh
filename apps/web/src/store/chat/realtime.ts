import { useRetryingStream } from '@/composables/useRetryingStream'
import {
  connectWebSocket,
  streamBotSessionsActivityEvents,
  streamSessionMessageEvents,
  type BotSessionActivityEvent,
  type ChatWebSocket,
  type SessionMessageStreamEvent,
  type UIStreamEvent,
  type WSClientMessage,
} from '@/composables/api/useChat'

interface RetryingStream {
  start: (runAttempt: (signal: AbortSignal) => Promise<void>) => void
  stop: () => void
}

interface SessionMessagesConnection {
  stream: RetryingStream
  generation: number
}

export interface ChatRealtimeCallbacks {
  onWebSocketEvent: (botId: string, event: UIStreamEvent) => void
  prepareSessionMessages: (botId: string, sessionId: string) => Promise<void>
  onSessionMessageEvent: (botId: string, sessionId: string, event: SessionMessageStreamEvent) => void
  onBotSessionsActivityEvent: (botId: string, event: BotSessionActivityEvent) => void
}

export interface ChatRealtimeTransport {
  connectWebSocket: typeof connectWebSocket
  streamSessionMessageEvents: typeof streamSessionMessageEvents
  streamBotSessionsActivityEvents: typeof streamBotSessionsActivityEvents
  createRetryingStream: () => RetryingStream
}

const defaultTransport: ChatRealtimeTransport = {
  connectWebSocket,
  streamSessionMessageEvents,
  streamBotSessionsActivityEvents,
  createRetryingStream: useRetryingStream,
}

// Owns chat transport lifecycles. Event interpretation stays with the store;
// this controller only guarantees that superseded connections cannot emit.
export function createChatRealtimeController(
  callbacks: ChatRealtimeCallbacks,
  transport: ChatRealtimeTransport = defaultTransport,
) {
  let activeWebSocket: ChatWebSocket | null = null
  let activeWebSocketBotId = ''
  let webSocketGeneration = 0
  let botSessionsActivityGeneration = 0
  const sessionMessagesConnections = new Map<string, SessionMessagesConnection>()
  const botSessionsActivityStream = transport.createRetryingStream()

  function stopWebSocket() {
    webSocketGeneration += 1
    const socket = activeWebSocket
    activeWebSocket = null
    activeWebSocketBotId = ''
    socket?.close()
  }

  function startWebSocket(botId: string) {
    const bid = botId.trim()
    stopWebSocket()
    if (!bid) return

    const generation = webSocketGeneration
    activeWebSocketBotId = bid
    try {
      activeWebSocket = transport.connectWebSocket(bid, (event) => {
        if (generation !== webSocketGeneration || activeWebSocketBotId !== bid) return
        callbacks.onWebSocketEvent(bid, event)
      })
    } catch (error) {
      activeWebSocketBotId = ''
      throw error
    }
  }

  function ensureWebSocketConnected(botId: string): boolean {
    const bid = botId.trim()
    if (!bid) return false
    if (!activeWebSocket || activeWebSocketBotId !== bid) startWebSocket(bid)
    return activeWebSocket?.connected === true
  }

  function sendWebSocketMessage(botId: string, message: WSClientMessage): boolean {
    if (!ensureWebSocketConnected(botId)) return false
    activeWebSocket!.send(message)
    return true
  }

  function abortWebSocketRun(runId: string, botId?: string): boolean {
    const id = runId.trim()
    const bid = botId?.trim()
    if (!id || !activeWebSocket?.connected) return false
    if (bid && bid !== activeWebSocketBotId) return false
    activeWebSocket.abort(id)
    return true
  }

  function sessionMessagesKey(botId: string, sessionId: string) {
    return `${botId}\u0000${sessionId}`
  }

  function stopSessionMessagesConnection(key: string, connection: SessionMessagesConnection) {
    if (sessionMessagesConnections.get(key) !== connection) return
    connection.generation += 1
    sessionMessagesConnections.delete(key)
    connection.stream.stop()
  }

  function stopSessionMessagesStream(botId?: string, sessionId?: string) {
    if (botId === undefined && sessionId === undefined) {
      for (const [key, connection] of sessionMessagesConnections) {
        stopSessionMessagesConnection(key, connection)
      }
      return
    }

    const bid = (botId ?? '').trim()
    const sid = (sessionId ?? '').trim()
    if (!bid || !sid) return
    const key = sessionMessagesKey(bid, sid)
    const connection = sessionMessagesConnections.get(key)
    if (connection) stopSessionMessagesConnection(key, connection)
  }

  function startSessionMessagesStream(botId: string, sessionId: string) {
    const bid = botId.trim()
    const sid = sessionId.trim()
    if (!bid || !sid) return
    const key = sessionMessagesKey(bid, sid)
    if (sessionMessagesConnections.has(key)) return

    const connection: SessionMessagesConnection = {
      stream: transport.createRetryingStream(),
      generation: 0,
    }
    sessionMessagesConnections.set(key, connection)
    connection.stream.start(async (signal) => {
      const generation = ++connection.generation
      const isCurrent = () => sessionMessagesConnections.get(key) === connection
        && connection.generation === generation
      try {
        await callbacks.prepareSessionMessages(bid, sid)
      } catch (error) {
        if (isCurrent() && !signal.aborted) {
          console.error('Failed to load session messages:', error)
        }
      }
      if (!isCurrent() || signal.aborted) return
      await transport.streamSessionMessageEvents(bid, sid, signal, (event) => {
        if (!isCurrent() || signal.aborted) return
        callbacks.onSessionMessageEvent(bid, sid, event)
      })
    })
  }

  function stopBotSessionsActivityStream() {
    botSessionsActivityGeneration += 1
    botSessionsActivityStream.stop()
  }

  function startBotSessionsActivityStream(botId: string) {
    stopBotSessionsActivityStream()
    const bid = botId.trim()
    if (!bid) return

    const generation = botSessionsActivityGeneration
    botSessionsActivityStream.start(async (signal) => {
      if (generation !== botSessionsActivityGeneration || signal.aborted) return
      await transport.streamBotSessionsActivityEvents(bid, signal, (event) => {
        if (generation !== botSessionsActivityGeneration) return
        callbacks.onBotSessionsActivityEvent(bid, event)
      })
    })
  }

  function stopStreams() {
    stopSessionMessagesStream()
    stopBotSessionsActivityStream()
  }

  return {
    startWebSocket,
    stopWebSocket,
    ensureWebSocketConnected,
    sendWebSocketMessage,
    abortWebSocketRun,
    startSessionMessagesStream,
    stopSessionMessagesStream,
    startBotSessionsActivityStream,
    stopStreams,
  }
}
