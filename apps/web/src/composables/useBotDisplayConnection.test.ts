// @vitest-environment jsdom
import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, ref } from 'vue'
import { notifyAuthSessionCleared } from '@/lib/auth-session'

/**
 * Failure Modes covered by this behavioral suite:
 * - FM-DC0: Structured preparation SSE error fails to localize or surface `unavailable` status.
 * - FM-DC1: During the 60s hidden/retention grace window, `notifyAuthSessionCleared('logout')` leaves
 *           `RTCPeerConnection` / `RTCDataChannel` open, retains remote `clipboardText`, leaks timers,
 *           or hands the stale connection entry to the next logged-in user.
 * - FM-DC2: An in-flight WebRTC acquisition started before logout continues after `notifyAuthSessionCleared`
 *           and populates `peer` / `sessionId` for the logged-out session.
 */

const postDisplayPrepareStream = vi.fn()
const getDisplay = vi.fn()
const postWebrtcOffer = vi.fn()
const deleteDisplaySession = vi.fn<(...args: unknown[]) => Promise<Record<string, never>>>(async () => ({}))

const storageMap = new Map<string, string>([['language', 'zh']])
const localStorageStub = {
  getItem: (key: string) => storageMap.get(key) ?? null,
  setItem: vi.fn((key: string, value: string) => {
    storageMap.set(key, value)
  }),
  removeItem: vi.fn((key: string) => {
    storageMap.delete(key)
  }),
  clear: vi.fn(() => {
    storageMap.clear()
  }),
}

vi.stubGlobal('localStorage', localStorageStub)

vi.mock('@memohai/sdk', () => ({
  deleteBotsByBotIdContainerDisplaySessionsBySessionId: (...args: unknown[]) => deleteDisplaySession(...args),
  getBotsByBotIdContainerDisplay: (...args: unknown[]) => getDisplay(...args),
  postBotsByBotIdContainerDisplayWebrtcOffer: (...args: unknown[]) => postWebrtcOffer(...args),
}))

vi.mock('@/composables/api/useDisplayPrepareStream', () => ({
  postBotsByBotIdContainerDisplayPrepareStream: (...args: unknown[]) => postDisplayPrepareStream(...args),
}))

class FakeDataChannel extends EventTarget {
  readyState: RTCDataChannelState = 'open'
  close = vi.fn(() => {
    this.readyState = 'closed'
  })

  send = vi.fn()
}

class FakePeerConnection extends EventTarget {
  iceGatheringState: RTCIceGatheringState = 'complete'
  connectionState: RTCPeerConnectionState = 'new'
  localDescription: RTCSessionDescriptionInit | null = null
  remoteDescription: RTCSessionDescriptionInit | null = null
  lastChannel: FakeDataChannel | null = null
  close = vi.fn(() => {
    this.connectionState = 'closed'
  })

  createDataChannel = vi.fn(() => {
    const ch = new FakeDataChannel()
    this.lastChannel = ch
    return ch as unknown as RTCDataChannel
  })

  addTransceiver = vi.fn()
  createOffer = vi.fn(async () => ({ type: 'offer' as const, sdp: 'v=0-offer' }))
  setLocalDescription = vi.fn(async (desc: RTCSessionDescriptionInit) => {
    this.localDescription = desc
  })

  setRemoteDescription = vi.fn(async (desc: RTCSessionDescriptionInit) => {
    this.remoteDescription = desc
    this.connectionState = 'connected'
    this.dispatchEvent(new Event('connectionstatechange'))
  })
}

const createdPeers: FakePeerConnection[] = []

const { BotDisplayConnection, useBotDisplayConnection } = await import('./useBotDisplayConnection')

describe('BotDisplayConnection structured preparation errors & auth cleanup', () => {
  afterAll(() => {
    vi.unstubAllGlobals()
  })

  beforeEach(() => {
    createdPeers.length = 0
    storageMap.clear()
    storageMap.set('language', 'zh')
    postDisplayPrepareStream.mockReset()
    getDisplay.mockReset()
    postWebrtcOffer.mockReset()
    deleteDisplaySession.mockClear()
    vi.stubGlobal('localStorage', localStorageStub)
    vi.stubGlobal('MediaStream', class FakeMediaStream {})
    vi.stubGlobal('RTCSessionDescription', class FakeRTCSessionDescription {
      type: RTCSdpType
      sdp: string
      constructor(init: RTCSessionDescriptionInit) {
        this.type = init.type
        this.sdp = init.sdp ?? ''
      }
    })
    vi.stubGlobal('RTCPeerConnection', class extends FakePeerConnection {
      constructor() {
        super()
        createdPeers.push(this)
      }
    })
  })

  afterEach(() => {
    notifyAuthSessionCleared('logout')
    vi.useRealTimers()
  })

  it('renders a structured preparation error through the public readiness flow (FM-DC0)', async () => {
    getDisplay.mockResolvedValue({
      data: {
        enabled: true,
        available: false,
        running: false,
        prepare_supported: true,
      },
    })
    postDisplayPrepareStream.mockResolvedValue({
      stream: (async function* () {
        yield {
          type: 'error',
          code: 'workspace.unreachable',
          args: {},
          message: 'The workspace could not be reached.',
          request_id: 'req-display-1',
        }
      })(),
    })

    const connection = new BotDisplayConnection('bot-1')
    const ready = await connection.ensureReady()

    expect(ready).toBe(false)
    expect(connection.status.value).toBe('unavailable')
    expect(connection.unavailableReason.value).toBe('暂时无法连接工作区，请稍后重试')
  })

  it('immediately closes retained RTCPeerConnection & DataChannel, clears timers, and zeroes clipboardText on auth session clear (FM-DC1)', async () => {
    vi.useFakeTimers()
    getDisplay.mockResolvedValue({
      data: {
        enabled: true,
        available: true,
        running: true,
        desktop_available: true,
        browser_available: true,
      },
    })
    postWebrtcOffer.mockResolvedValue({
      data: {
        type: 'answer',
        sdp: 'v=0-answer',
        session_id: 'rtc-session-1',
      },
    })

    const visible = ref(true)
    const scope = effectScope()
    const conn = scope.run(() => useBotDisplayConnection('bot-1', visible))!
    for (let i = 0; i < 12; i++) {
      await Promise.resolve()
    }

    expect(createdPeers).toHaveLength(1)
    const peer = createdPeers[0]!
    const channel = peer.lastChannel!
    expect(conn.status.value).toBe('connected')
    expect(conn.sessionId.value).toBe('rtc-session-1')

    // Simulate remote clipboard arrival over the DataChannel.
    channel.dispatchEvent(new MessageEvent('message', {
      data: JSON.stringify({ type: 'clipboard', text: 'top-secret-user-a-clipboard' }),
    }))
    expect(conn.clipboardText.value).toBe('top-secret-user-a-clipboard')

    // Dispose viewer scope so both the 60s hidden-release timer and 60s map retention timer are armed,
    // while peer/channel/clipboard are still held warm in memory.
    scope.stop()
    expect(peer.close).not.toHaveBeenCalled()
    expect(channel.close).not.toHaveBeenCalled()
    expect(vi.getTimerCount()).toBeGreaterThanOrEqual(2)

    // Auth session clears during the 60s retention window: everything must tear down IMMEDIATELY.
    notifyAuthSessionCleared('logout')

    expect(peer.close).toHaveBeenCalledTimes(1)
    expect(channel.close).toHaveBeenCalledTimes(1)
    expect(conn.peer.value).toBeNull()
    expect(conn.inputChannel.value).toBeNull()
    expect(conn.stream.value).toBeNull()
    expect(conn.sessionId.value).toBe('')
    expect(conn.clipboardText.value).toBe('')
    expect(conn.status.value).toBe('idle')
    expect(vi.getTimerCount()).toBe(0)

    // Next user mounting the same botId receives a fresh connection instance rather than User A's disposed one.
    const nextScope = effectScope()
    const nextConn = nextScope.run(() => useBotDisplayConnection('bot-1', ref(false)))!
    expect(nextConn).not.toBe(conn)
    nextScope.stop()
    // Clean up nextScope's retention timer before ending test.
    notifyAuthSessionCleared('logout')
  })

  it('aborts an in-flight WebRTC acquisition on auth session clear so late responses cannot attach a peer (FM-DC2)', async () => {
    let capturedSignal: AbortSignal | undefined
    let resolveInfo!: (value: unknown) => void
    getDisplay.mockImplementation(({ signal }: { signal?: AbortSignal }) => {
      capturedSignal = signal
      return new Promise((resolve) => {
        resolveInfo = resolve
      })
    })

    const scope = effectScope()
    const conn = scope.run(() => useBotDisplayConnection('bot-2', ref(true)))!
    await Promise.resolve()
    await Promise.resolve()

    expect(conn.status.value).toBe('connecting')
    expect(capturedSignal?.aborted).toBe(false)

    notifyAuthSessionCleared('unauthorized')

    expect(capturedSignal?.aborted).toBe(true)
    expect(conn.status.value).toBe('idle')
    expect(conn.peer.value).toBeNull()

    // Even if the underlying request resolves afterwards, no WebRTC offer or peer is created.
    resolveInfo({
      data: {
        enabled: true,
        available: true,
        running: true,
        desktop_available: true,
        browser_available: true,
      },
    })
    await Promise.resolve()
    await Promise.resolve()

    expect(createdPeers).toHaveLength(0)
    expect(postWebrtcOffer).not.toHaveBeenCalled()
    expect(conn.peer.value).toBeNull()
    scope.stop()
  })
})
