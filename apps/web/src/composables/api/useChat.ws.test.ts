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

    ws.abort('run-1')

    expect(JSON.parse(socket.sent[0]!)).toEqual({
      type: 'abort',
      run_id: 'run-1',
    })
  })

  it('lets scripted server events drive each websocket client independently', () => {
    const firstHandler = vi.fn()
    const secondHandler = vi.fn()
    connectWebSocket('bot-1', firstHandler)
    connectWebSocket('bot-1', secondHandler)
    const first = MockWebSocket.instances[0]!
    const second = MockWebSocket.instances[1]!
    first.open()
    second.open()

    first.emit({ type: 'start', run_id: 'run-a', session_id: 'session-1' })
    second.emit({ type: 'message', run_id: 'run-b', session_id: 'session-2', data: { id: 0, type: 'text', content: 'hello' } })

    expect(firstHandler).toHaveBeenCalledTimes(1)
    expect(firstHandler).toHaveBeenCalledWith({ type: 'start', run_id: 'run-a', session_id: 'session-1' })
    expect(secondHandler).toHaveBeenCalledTimes(1)
    expect(secondHandler).toHaveBeenCalledWith({ type: 'message', run_id: 'run-b', session_id: 'session-2', data: { id: 0, type: 'text', content: 'hello' } })
  })

  it('reconnects after disconnect and flushes queued messages on the new socket', () => {
    vi.useFakeTimers()
    const ws = connectWebSocket('bot-1', vi.fn())
    const first = MockWebSocket.instances[0]!
    first.open()

    first.close()
    expect(ws.connected).toBe(false)

    ws.send({
      type: 'message',
      invocation_id: 'invocation-after-reconnect',
      session_id: 'session-1',
      text: 'resume',
    })

    vi.advanceTimersByTime(1000)
    const second = MockWebSocket.instances[1]!
    expect(second).toBeDefined()
    expect(second.sent).toEqual([])

    second.open()

    expect(JSON.parse(second.sent[0]!)).toEqual({
      type: 'message',
      invocation_id: 'invocation-after-reconnect',
      session_id: 'session-1',
      text: 'resume',
    })
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
