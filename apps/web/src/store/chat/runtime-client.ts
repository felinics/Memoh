import type {
  RuntimeCursor,
  UIRuntimeEvent,
  UIRuntimeSnapshotEvent,
  WSClientMessage,
} from '@/composables/api/useChat'
import {
  createEmptyRuntimeProjection,
  reduceRuntimeProjection,
  type RuntimeProjectionState,
} from './runtime-projection'

export interface RuntimeProjectionChange {
  previous: RuntimeProjectionState
  current: RuntimeProjectionState
  event: Exclude<UIRuntimeEvent, { type: 'runtime_dropped' }>
}

export interface RuntimeClientDeps {
  send: (message: WSClientMessage) => void
  onProjection: (sessionId: string, change: RuntimeProjectionChange) => void
}

export function createRuntimeClient({ send, onProjection }: RuntimeClientDeps) {
  const subscriptions = new Set<string>()
  const projections = new Map<string, RuntimeProjectionState>()
  const awaitingSnapshots = new Set<string>()
  let connected = false

  function sendSubscribe(sessionId: string) {
    if (!connected) return
    const state = projections.get(sessionId)
    const cursor: RuntimeCursor | undefined = state?.epoch
      ? { epoch: state.epoch, seq: state.seq }
      : undefined
    try {
      send({
        type: 'runtime_subscribe',
        session_id: sessionId,
        ...(cursor ? { cursor } : {}),
      })
    } catch {
      // The socket lifecycle will call onConnected again after reconnect.
    }
  }

  function subscribe(sessionId: string) {
    const sid = sessionId.trim()
    if (!sid) return
    const alreadySubscribed = subscriptions.has(sid)
    subscriptions.add(sid)
    if (!alreadySubscribed) {
      awaitingSnapshots.add(sid)
      sendSubscribe(sid)
    }
  }

  function unsubscribe(sessionId: string) {
    const sid = sessionId.trim()
    if (!sid || !subscriptions.delete(sid)) return
    awaitingSnapshots.delete(sid)
    if (connected) {
      try {
        send({ type: 'runtime_unsubscribe', session_id: sid })
      } catch {
        // Closing a subscription is best-effort once the socket is gone.
      }
    }
  }

  function recoverFromSnapshot(sessionId: string) {
    if (!subscriptions.has(sessionId) || awaitingSnapshots.has(sessionId)) return
    awaitingSnapshots.add(sessionId)
    sendSubscribe(sessionId)
  }

  function handleSnapshot(event: UIRuntimeSnapshotEvent) {
    const sid = event.session_id.trim()
    if (!sid || !subscriptions.has(sid)) return
    const previous = projections.get(sid) ?? createEmptyRuntimeProjection(sid)
    const recovering = awaitingSnapshots.delete(sid)
    if (!recovering && previous.epoch === event.epoch && event.seq <= previous.seq) return
    const current = reduceRuntimeProjection(previous, event)
    projections.set(sid, current)
    onProjection(sid, { previous, current, event })
  }

  function handleEvent(event: UIRuntimeEvent) {
    const sid = event.session_id.trim()
    if (!sid || !subscriptions.has(sid)) return
    if (event.type === 'runtime_dropped') {
      recoverFromSnapshot(sid)
      return
    }
    if (event.type === 'runtime_snapshot') {
      handleSnapshot(event)
      return
    }

    if (awaitingSnapshots.has(sid)) return
    const previous = projections.get(sid)
    if (!previous || !previous.epoch || event.epoch !== previous.epoch) {
      recoverFromSnapshot(sid)
      return
    }
    if (event.seq <= previous.seq) return
    if (event.seq !== previous.seq + 1) {
      recoverFromSnapshot(sid)
      return
    }
    const current = reduceRuntimeProjection(previous, event)
    projections.set(sid, current)
    onProjection(sid, { previous, current, event })
  }

  function onConnected() {
    connected = true
    for (const sessionId of subscriptions) {
      awaitingSnapshots.add(sessionId)
      sendSubscribe(sessionId)
    }
  }

  function onDisconnected() {
    connected = false
  }

  function projection(sessionId: string): RuntimeProjectionState | undefined {
    return projections.get(sessionId.trim())
  }

  function reset() {
    subscriptions.clear()
    projections.clear()
    awaitingSnapshots.clear()
    connected = false
  }

  return {
    subscribe,
    unsubscribe,
    handleEvent,
    onConnected,
    onDisconnected,
    projection,
    reset,
  }
}
