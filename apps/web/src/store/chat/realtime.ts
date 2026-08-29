import { useRetryingStream } from '@/composables/useRetryingStream'
import {
  connectWebSocket,
  streamBotSessionsActivityEvents,
  type BotSessionActivityEvent,
  type ChatWebSocket,
  type UIRuntimeEvent,
  type UIStreamEvent,
  type WSClientMessage,
} from '@/composables/api/useChat'
import {
  createRuntimeClient,
  type RuntimeProjectionChange,
} from './runtime-client'

interface RetryingStream {
  start: (runAttempt: (signal: AbortSignal) => Promise<void>) => void
  stop: () => void
}

interface SessionRuntimeConnection {
  prepared: boolean
  pending: RuntimeProjectionChange[]
  initialSnapshotReady: Promise<void>
  resolveInitialSnapshot: (() => void) | null
}

export interface ChatRealtimeCallbacks {
  onWebSocketEvent: (botId: string, event: UIStreamEvent) => void
  prepareSessionRuntime: (
    botId: string,
    sessionId: string,
    commitInitialHistory: (applyHistory: () => void) => Promise<void>,
  ) => Promise<void>
  onRuntimeProjection: (
    botId: string,
    sessionId: string,
    change: RuntimeProjectionChange,
  ) => void
  onBotSessionsActivityEvent: (botId: string, event: BotSessionActivityEvent) => void
}

export interface ChatRealtimeTransport {
  connectWebSocket: typeof connectWebSocket
  streamBotSessionsActivityEvents: typeof streamBotSessionsActivityEvents
  createRetryingStream: () => RetryingStream
}

const defaultTransport: ChatRealtimeTransport = {
  connectWebSocket,
  streamBotSessionsActivityEvents,
  createRetryingStream: useRetryingStream,
}

function isRuntimeEvent(event: UIStreamEvent): event is UIRuntimeEvent {
  return event.type === 'runtime_snapshot'
    || event.type === 'runtime_delta'
    || event.type === 'runtime_dropped'
}

// Owns chat transport lifecycles. The WebSocket carries both turn commands and
// session runtime subscriptions; the bot-wide SSE remains sidebar metadata only.
export function createChatRealtimeController(
  callbacks: ChatRealtimeCallbacks,
  transport: ChatRealtimeTransport = defaultTransport,
) {
  let activeWebSocket: ChatWebSocket | null = null
  let activeWebSocketBotId = ''
  let webSocketGeneration = 0
  let botSessionsActivityGeneration = 0
  const sessionRuntimeConnections = new Map<string, SessionRuntimeConnection>()
  const botSessionsActivityStream = transport.createRetryingStream()
  const runtimeClient = createRuntimeClient({
    send: message => activeWebSocket?.send(message),
    onProjection: (sessionId, change) => {
      const botId = activeWebSocketBotId
      const connection = botId
        ? sessionRuntimeConnections.get(sessionRuntimeKey(botId, sessionId))
        : undefined
      if (!botId || !connection) return
      if (!connection.prepared) {
        connection.pending.push(change)
        if (change.event.type === 'runtime_snapshot') {
          connection.resolveInitialSnapshot?.()
          connection.resolveInitialSnapshot = null
        }
        return
      }
      callbacks.onRuntimeProjection(botId, sessionId, change)
    },
  })

  function stopWebSocket() {
    webSocketGeneration += 1
    const socket = activeWebSocket
    activeWebSocket = null
    activeWebSocketBotId = ''
    runtimeClient.onDisconnected()
    socket?.close()
  }

  function startWebSocket(botId: string) {
    const bid = botId.trim()
    stopWebSocket()
    if (!bid) return

    const generation = webSocketGeneration
    activeWebSocketBotId = bid
    try {
      const socket = transport.connectWebSocket(bid, (event) => {
        if (generation !== webSocketGeneration || activeWebSocketBotId !== bid) return
        if (isRuntimeEvent(event)) {
          runtimeClient.handleEvent(event)
          return
        }
        callbacks.onWebSocketEvent(bid, event)
      })
      socket.onOpen = () => {
        if (generation !== webSocketGeneration || activeWebSocketBotId !== bid) return
        runtimeClient.onConnected()
      }
      socket.onClose = () => {
        if (generation !== webSocketGeneration || activeWebSocketBotId !== bid) return
        runtimeClient.onDisconnected()
      }
      activeWebSocket = socket
      if (socket.connected) runtimeClient.onConnected()
    } catch (error) {
      activeWebSocketBotId = ''
      throw error
    }
  }

  // A live socket handle is enough, even mid-handshake or between reconnect
  // attempts: the ws layer queues messages and flushes them on open, so a send
  // only fails when no socket exists for the bot at all. Failing fast while
  // the first-open handshake was still in flight used to sacrifice the user's
  // first message with "WebSocket is not connected" (#1070).
  function ensureWebSocket(botId: string): boolean {
    const bid = botId.trim()
    if (!bid) return false
    if (!activeWebSocket || activeWebSocketBotId !== bid) startWebSocket(bid)
    return activeWebSocket !== null
  }

  function sendWebSocketMessage(botId: string, message: WSClientMessage): boolean {
    if (!ensureWebSocket(botId)) return false
    activeWebSocket!.send(message)
    return true
  }

  function abortWebSocketRun(
    runId: string,
    botId?: string,
    sessionId?: string,
    controlId?: string,
  ): boolean {
    const id = runId.trim()
    const bid = botId?.trim()
    const sid = sessionId?.trim() ?? ''
    const cid = controlId?.trim() ?? ''
    if (!id || !sid || !cid || !activeWebSocket?.connected) return false
    if (bid && bid !== activeWebSocketBotId) return false
    activeWebSocket.abort(id, sid, cid)
    return true
  }

  function sessionRuntimeKey(botId: string, sessionId: string) {
    return `${botId}\u0000${sessionId}`
  }

  function stopSessionRuntime(botId?: string, sessionId?: string) {
    if (botId === undefined && sessionId === undefined) {
      for (const [key, connection] of sessionRuntimeConnections) {
        const [, sid = ''] = key.split('\u0000')
        connection.resolveInitialSnapshot?.()
        runtimeClient.unsubscribe(sid)
      }
      sessionRuntimeConnections.clear()
      return
    }

    const bid = (botId ?? '').trim()
    const sid = (sessionId ?? '').trim()
    if (!bid || !sid) return
    const key = sessionRuntimeKey(bid, sid)
    const connection = sessionRuntimeConnections.get(key)
    if (!connection) return
    sessionRuntimeConnections.delete(key)
    connection.resolveInitialSnapshot?.()
    runtimeClient.unsubscribe(sid)
  }

  function startSessionRuntime(botId: string, sessionId: string) {
    const bid = botId.trim()
    const sid = sessionId.trim()
    if (!bid || !sid) return
    const key = sessionRuntimeKey(bid, sid)
    if (sessionRuntimeConnections.has(key)) return
    let resolveInitialSnapshot!: () => void
    const initialSnapshotReady = new Promise<void>((resolve) => {
      resolveInitialSnapshot = resolve
    })
    const connection: SessionRuntimeConnection = {
      prepared: false,
      pending: [],
      initialSnapshotReady,
      resolveInitialSnapshot,
    }
    sessionRuntimeConnections.set(key, connection)
    runtimeClient.subscribe(sid)

    const applyBufferedProjections = () => {
      const pending = connection.pending.splice(0)
      for (const change of pending) {
        callbacks.onRuntimeProjection(bid, sid, change)
      }
    }
    const commitInitialHistory = async (applyHistory: () => void) => {
      await connection.initialSnapshotReady
      if (sessionRuntimeConnections.get(key) !== connection) return
      // The database can still expose the pre-edit tail while Runtime already
      // owns its replacement. Commit both projections without yielding so Vue
      // never renders the database-only intermediate state.
      applyHistory()
      applyBufferedProjections()
    }
    void callbacks.prepareSessionRuntime(bid, sid, commitInitialHistory)
      .catch(error => console.error('Failed to load session messages:', error))
      .finally(() => {
        if (sessionRuntimeConnections.get(key) !== connection) return
        connection.prepared = true
        applyBufferedProjections()
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
    stopSessionRuntime()
    runtimeClient.reset()
    stopBotSessionsActivityStream()
  }

  return {
    startWebSocket,
    stopWebSocket,
    ensureWebSocket,
    sendWebSocketMessage,
    abortWebSocketRun,
    startSessionRuntime,
    stopSessionRuntime,
    startBotSessionsActivityStream,
    stopStreams,
    runtimeProjection: runtimeClient.projection,
  }
}
