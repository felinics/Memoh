import type {
  RuntimeCursor,
  RuntimeCurrentRunView,
  UIRuntimeDeltaEvent,
  UIRuntimeEvent,
  UIRuntimeSnapshotEvent,
  WSClientMessage,
} from '@/composables/api/useChat'
import {
  applyRuntimeRunPatch,
  createEmptyRuntimeProjection,
  projectRuntimeTranscript,
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
  // Test hook: production coalesces delta notifications to one per animation
  // frame (see the batch below); tests inject a synchronous scheduler.
  scheduleFrame?: (callback: () => void) => void
}

const defaultScheduleFrame = (callback: () => void) => {
  if (typeof requestAnimationFrame === 'function') {
    requestAnimationFrame(() => callback())
    return
  }
  setTimeout(callback, 16)
}

export function createRuntimeClient({ send, onProjection, scheduleFrame }: RuntimeClientDeps) {
  const schedule = scheduleFrame ?? defaultScheduleFrame
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
    pendingDeltaBatches.delete(sid)
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
    pendingDeltaBatches.delete(sessionId)
    awaitingSnapshots.add(sessionId)
    sendSubscribe(sessionId)
  }

  // Delta batching. Every delta used to trigger a full projection rebuild AND
  // a downstream transcript merge + reactive notification, so a burst of N
  // deltas (normal streaming, or a backgrounded tab replaying its backlog on
  // return) cost N x O(content) and could pin the main thread for tens of
  // seconds. Deltas are now accumulated per session and flushed once per
  // animation frame: seq/epoch integrity is still checked per event, but the
  // transcript build + notification happen once per frame. A hidden tab has no
  // rAF, so background streaming costs ZERO projection work; on return the
  // whole backlog lands in a single frame and the terminal run status flips in
  // that same frame instead of trailing the content queue.
  interface PendingDeltaBatch {
    base: RuntimeProjectionState
    currentRunView: RuntimeCurrentRunView | null
    epoch: string
    seq: number
    lastEvent: UIRuntimeDeltaEvent
  }
  const pendingDeltaBatches = new Map<string, PendingDeltaBatch>()
  let flushScheduled = false

  function flushDeltaBatches() {
    const batches = [...pendingDeltaBatches.entries()]
    pendingDeltaBatches.clear()
    for (const [sid, batch] of batches) {
      if (!subscriptions.has(sid)) continue
      const current: RuntimeProjectionState = {
        ...batch.base,
        sessionId: sid,
        epoch: batch.epoch,
        seq: batch.seq,
        currentRunView: batch.currentRunView,
        transcript: projectRuntimeTranscript(batch.currentRunView),
      }
      projections.set(sid, current)
      onProjection(sid, { previous: batch.base, current, event: batch.lastEvent })
    }
  }

  function scheduleDeltaFlush() {
    if (flushScheduled) return
    flushScheduled = true
    schedule(() => {
      flushScheduled = false
      flushDeltaBatches()
    })
  }

  function handleSnapshot(event: UIRuntimeSnapshotEvent) {
    const sid = event.session_id.trim()
    if (!sid || !subscriptions.has(sid)) return
    const previous = projections.get(sid) ?? createEmptyRuntimeProjection(sid)
    const recovering = awaitingSnapshots.delete(sid)
    if (!recovering && previous.epoch === event.epoch && event.seq <= previous.seq) return
    pendingDeltaBatches.delete(sid)
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
    const delivered = projections.get(sid)
    if (!delivered || !delivered.epoch || event.epoch !== delivered.epoch) {
      recoverFromSnapshot(sid)
      return
    }
    const pending = pendingDeltaBatches.get(sid)
    const latestSeq = pending?.seq ?? delivered.seq
    if (event.seq <= latestSeq) return
    if (event.seq !== latestSeq + 1) {
      recoverFromSnapshot(sid)
      return
    }
    const currentRunView = applyRuntimeRunPatch(
      pending?.currentRunView ?? delivered.currentRunView,
      event.delta,
    )
    pendingDeltaBatches.set(sid, {
      base: pending?.base ?? delivered,
      currentRunView,
      epoch: event.epoch,
      seq: event.seq,
      lastEvent: event,
    })
    scheduleDeltaFlush()
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
    pendingDeltaBatches.clear()
    flushScheduled = false
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
