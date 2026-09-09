import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { client } from '@memohai/sdk/client'
import { connectWebSocket } from './useChat.ws'

class MockWebSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3

  static instances: MockWebSocket[] = []

  readyState = MockWebSocket.CONNECTING
  sent: string[] = []
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  readonly url: string

  constructor(url: string) {
    this.url = url
    MockWebSocket.instances.push(this)
  }

  send(payload: string) {
    this.sent.push(payload)
  }

  close() {
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.()
  }

  open() {
    this.readyState = MockWebSocket.OPEN
    this.onopen?.()
  }

  emit(payload: unknown) {
    this.onmessage?.({ data: JSON.stringify(payload) })
  }
}

describe('useChat.ws', () => {
  beforeEach(() => {
    MockWebSocket.instances = []
    vi.unstubAllGlobals()
    client.setConfig({ baseUrl: '/api' })
    vi.stubGlobal('window', {
      location: {
        protocol: 'http:',
        host: 'localhost:8082',
      },
    })
    vi.stubGlobal('localStorage', {
      getItem: vi.fn(() => ''),
    })
    vi.stubGlobal('WebSocket', MockWebSocket)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('queues outbound messages until socket opens', () => {
    const onStreamEvent = vi.fn()
    const ws = connectWebSocket('bot-1', onStreamEvent)
    const socket = MockWebSocket.instances[0]!

    expect(socket).toBeDefined()
    ws.send({ type: 'message', invocation_id: 'invocation-1', text: 'hello', session_id: 'session-1' })
    expect(socket.sent).toEqual([])

    socket.open()

    expect(socket.sent).toHaveLength(1)
    expect(JSON.parse(socket.sent[0]!)).toEqual({
      type: 'message',
      invocation_id: 'invocation-1',
      text: 'hello',
      session_id: 'session-1',
    })
  })

  it('sends targeted abort messages', () => {
    const ws = connectWebSocket('bot-1', vi.fn())
    const socket = MockWebSocket.instances[0]!
    socket.open()

    ws.abort('run-1', 'session-1', 'control-1')

    expect(JSON.parse(socket.sent[0]!)).toEqual({
      type: 'abort',
      run_id: 'run-1',
      session_id: 'session-1',
      control_id: 'control-1',
    })
  })

  it('replays an unacknowledged control with the same id', () => {
    vi.useFakeTimers()
    const ws = connectWebSocket('bot-1', vi.fn())
    const first = MockWebSocket.instances[0]!
    first.open()
    ws.abort('run-1', 'session-1', 'control-1')

    first.close()
    vi.advanceTimersByTime(1000)
    const second = MockWebSocket.instances[1]!
    second.open()
    expect(JSON.parse(second.sent[0]!)).toEqual({
      type: 'abort',
      run_id: 'run-1',
      session_id: 'session-1',
      control_id: 'control-1',
    })

    second.emit({
      type: 'control_ack',
      session_id: 'session-1',
      run_id: 'run-1',
      control: 'abort',
      control_id: 'control-1',
      applied: true,
    })
    second.close()
    vi.advanceTimersByTime(1000)
    const third = MockWebSocket.instances[2]!
    third.open()
    expect(third.sent).toEqual([])
  })

  it('routes runtime events independently per connection', () => {
    const firstHandler = vi.fn()
    const secondHandler = vi.fn()
    connectWebSocket('bot-1', firstHandler)
    connectWebSocket('bot-1', secondHandler)
    const first = MockWebSocket.instances[0]!
    const second = MockWebSocket.instances[1]!
    first.open()
    second.open()

    first.emit({
      type: 'runtime_snapshot',
      session_id: 'session-1',
      epoch: 'epoch-1',
      seq: 1,
      snapshot: {
        bot_id: 'bot-1',
        session_id: 'session-1',
        epoch: 'epoch-1',
        seq: 1,
        updated_at: '2026-07-27T08:00:00Z',
      },
    })
    second.emit({
      type: 'runtime_delta',
      session_id: 'session-2',
      epoch: 'epoch-2',
      seq: 2,
      delta: {},
    })

    expect(firstHandler).toHaveBeenCalledTimes(1)
    expect(firstHandler).toHaveBeenCalledWith(expect.objectContaining({
      type: 'runtime_snapshot',
      session_id: 'session-1',
    }))
    expect(secondHandler).toHaveBeenCalledTimes(1)
    expect(secondHandler).toHaveBeenCalledWith({
      type: 'runtime_delta',
      session_id: 'session-2',
      epoch: 'epoch-2',
      seq: 2,
      delta: {},
    })
  })

  it('replays an unacknowledged invocation after reconnect and stops after acceptance', () => {
    vi.useFakeTimers()
    const onStreamEvent = vi.fn()
    const ws = connectWebSocket('bot-1', onStreamEvent)
    const first = MockWebSocket.instances[0]!
    first.open()

    ws.send({
      type: 'message',
      invocation_id: 'invocation-1',
      session_id: 'session-1',
      text: 'resume',
    })
    expect(first.sent).toHaveLength(1)

    first.close()
    vi.advanceTimersByTime(1000)
    const second = MockWebSocket.instances[1]!
    second.open()

    expect(JSON.parse(second.sent[0]!)).toEqual({
      type: 'message',
      invocation_id: 'invocation-1',
      session_id: 'session-1',
      text: 'resume',
    })

    second.emit({
      type: 'run_accepted',
      invocation_id: 'invocation-1',
      session_id: 'session-1',
      run_id: 'run-1',
      turn_id: 'turn-1',
      epoch: 'epoch-1',
      seq: 1,
    })
    expect(onStreamEvent).toHaveBeenCalledOnce()

    second.close()
    vi.advanceTimersByTime(1000)
    const third = MockWebSocket.instances[2]!
    third.open()
    expect(third.sent).toEqual([])
  })

  it('forwards preference settlement before the response completes', () => {
    const onStreamEvent = vi.fn()
    connectWebSocket('bot-1', onStreamEvent)
    const socket = MockWebSocket.instances[0]!
    socket.open()
    const event = { type: 'model_preference_settled', invocation_id: 'inv', run_id: 'run', session_id: 'session' }
    socket.emit(event)
    expect(onStreamEvent).toHaveBeenCalledWith(event)
  })

  it('uses the configured absolute API base URL', () => {
    client.setConfig({ baseUrl: 'http://127.0.0.1:18080' })
    vi.stubGlobal('localStorage', {
      getItem: vi.fn(() => 'token with spaces'),
    })

    connectWebSocket('bot 1', vi.fn())

    expect(MockWebSocket.instances[0]?.url).toBe('ws://127.0.0.1:18080/bots/bot%201/web/ws?token=token%20with%20spaces')
  })
})
