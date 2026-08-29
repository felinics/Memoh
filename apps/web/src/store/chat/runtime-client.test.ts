import { describe, expect, it, vi } from 'vitest'
import type {
  UIRuntimeDeltaEvent,
  UIRuntimeDroppedEvent,
  UIRuntimeSnapshotEvent,
  WSClientMessage,
} from '@/composables/api/useChat'
import { createRuntimeClient } from './runtime-client'

function snapshot(seq = 3): UIRuntimeSnapshotEvent {
  return {
    type: 'runtime_snapshot',
    session_id: 'session-1',
    epoch: 'epoch-1',
    seq,
    snapshot: {
      bot_id: 'bot-1',
      session_id: 'session-1',
      epoch: 'epoch-1',
      seq,
      updated_at: '2026-07-27T08:00:00.000Z',
    },
  }
}

function delta(seq: number, epoch = 'epoch-1'): UIRuntimeDeltaEvent {
  return {
    type: 'runtime_delta',
    session_id: 'session-1',
    epoch,
    seq,
    delta: {},
  }
}

function createHarness() {
  const sent: WSClientMessage[] = []
  const onProjection = vi.fn()
  const client = createRuntimeClient({
    send: message => sent.push(message),
    onProjection,
    // Keep delivery synchronous so the existing per-event assertions hold.
    scheduleFrame: callback => callback(),
  })
  return { client, sent, onProjection }
}

describe('runtime client', () => {
  it('subscribes on connect and resumes from its last authoritative cursor', () => {
    const { client, sent } = createHarness()
    client.subscribe('session-1')
    expect(sent).toEqual([])

    client.onConnected()
    expect(sent).toEqual([{ type: 'runtime_subscribe', session_id: 'session-1' }])

    client.handleEvent(snapshot())
    client.onDisconnected()
    client.onConnected()
    expect(sent.at(-1)).toEqual({
      type: 'runtime_subscribe',
      session_id: 'session-1',
      cursor: { epoch: 'epoch-1', seq: 3 },
    })
  })

  it('applies the next sequence and ignores duplicate or stale frames', () => {
    const { client, onProjection } = createHarness()
    client.subscribe('session-1')
    client.onConnected()
    client.handleEvent(snapshot())
    client.handleEvent(delta(4))
    client.handleEvent(delta(4))
    client.handleEvent(delta(2))

    expect(onProjection).toHaveBeenCalledTimes(2)
    expect(client.projection('session-1')?.seq).toBe(4)
  })

  it('ignores stale snapshots in the same epoch but accepts a new epoch', () => {
    const { client, onProjection } = createHarness()
    client.subscribe('session-1')
    client.onConnected()
    client.handleEvent(snapshot(3))
    client.handleEvent(snapshot(2))
    client.handleEvent({
      ...snapshot(1),
      epoch: 'epoch-2',
      snapshot: {
        ...snapshot(1).snapshot,
        epoch: 'epoch-2',
        seq: 1,
      },
    })

    expect(onProjection).toHaveBeenCalledTimes(2)
    expect(client.projection('session-1')).toMatchObject({
      epoch: 'epoch-2',
      seq: 1,
    })
  })

  it('ignores deltas after a gap until an authoritative snapshot replaces the state', () => {
    const { client, sent, onProjection } = createHarness()
    client.subscribe('session-1')
    client.onConnected()
    client.handleEvent(snapshot())
    client.handleEvent(delta(5))
    client.handleEvent(delta(4))

    expect(onProjection).toHaveBeenCalledOnce()
    expect(client.projection('session-1')?.seq).toBe(3)
    expect(sent.at(-1)).toEqual({
      type: 'runtime_subscribe',
      session_id: 'session-1',
      cursor: { epoch: 'epoch-1', seq: 3 },
    })

    client.handleEvent(snapshot(3))
    client.handleEvent(delta(4))
    expect(onProjection).toHaveBeenCalledTimes(3)
    expect(client.projection('session-1')?.seq).toBe(4)
  })

  it('requests only one recovery snapshot while frames keep arriving', () => {
    const { client, sent } = createHarness()
    client.subscribe('session-1')
    client.onConnected()
    client.handleEvent(snapshot())
    client.handleEvent(delta(4, 'epoch-2'))
    client.handleEvent({
      type: 'runtime_dropped',
      session_id: 'session-1',
      epoch: 'epoch-1',
      seq: 3,
    } satisfies UIRuntimeDroppedEvent)

    expect(sent.filter(message => message.type === 'runtime_subscribe')).toHaveLength(2)

    client.handleEvent({
      ...snapshot(1),
      epoch: 'epoch-2',
      snapshot: {
        ...snapshot(1).snapshot,
        epoch: 'epoch-2',
        seq: 1,
      },
    })
    client.handleEvent({
      type: 'runtime_dropped',
      session_id: 'session-1',
      epoch: 'epoch-2',
      seq: 1,
    })
    expect(sent.filter(message => message.type === 'runtime_subscribe')).toHaveLength(3)
  })

  it('unsubscribes explicitly and rejects later frames for that session', () => {
    const { client, sent, onProjection } = createHarness()
    client.subscribe('session-1')
    client.onConnected()
    client.unsubscribe('session-1')
    client.handleEvent(snapshot())

    expect(sent.at(-1)).toEqual({ type: 'runtime_unsubscribe', session_id: 'session-1' })
    expect(onProjection).not.toHaveBeenCalled()
  })

  it('coalesces a burst of deltas into one notification per frame', () => {
    const sent: WSClientMessage[] = []
    const onProjection = vi.fn()
    const frames: Array<() => void> = []
    const client = createRuntimeClient({
      send: message => sent.push(message),
      onProjection,
      scheduleFrame: (callback) => {
        frames.push(callback)
      },
    })
    client.subscribe('session-1')
    client.onConnected()
    client.handleEvent(snapshot())
    expect(onProjection).toHaveBeenCalledTimes(1)

    // snapshot() lands at seq 3; the burst chains off it.
    client.handleEvent(delta(4))
    client.handleEvent(delta(5))
    client.handleEvent(delta(6))
    // Nothing is delivered until the frame runs — the burst accumulates.
    expect(onProjection).toHaveBeenCalledTimes(1)
    expect(frames).toHaveLength(1)

    frames[0]!()
    expect(onProjection).toHaveBeenCalledTimes(2)
    const change = onProjection.mock.calls[1]![1]
    expect(change.event.type).toBe('runtime_delta')
    expect(change.previous.seq).toBe(3)
    expect(change.current.seq).toBe(6)
    expect(client.projection('session-1')?.seq).toBe(6)

    // A later delta starts a fresh batch and schedules exactly one new frame.
    client.handleEvent(delta(7))
    expect(frames).toHaveLength(2)
    frames[1]!()
    expect(onProjection).toHaveBeenCalledTimes(3)
  })
})
