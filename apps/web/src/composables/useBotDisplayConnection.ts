import { onScopeDispose, ref, shallowRef, toValue, watch, type MaybeRefOrGetter } from 'vue'
import {
  deleteBotsByBotIdContainerDisplaySessionsBySessionId,
  getBotsByBotIdContainerDisplay,
  postBotsByBotIdContainerDisplayWebrtcOffer,
} from '@memohai/sdk'
import i18n from '@/i18n'
import { onAuthSessionCleared } from '@/lib/auth-session'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { decodeClipboardMessage, encodeClipboardPaste } from './display-clipboard'
import {
  postBotsByBotIdContainerDisplayPrepareStream,
  type DisplayPrepareStreamEvent,
} from '@/composables/api/useDisplayPrepareStream'

export type DisplayStatus = 'idle' | 'connecting' | 'connected' | 'disconnected' | 'unavailable'

export interface PrepareProgress {
  percent: number
  message: string
  step?: string
}

interface DisplayInfoPayload {
  enabled?: boolean
  available?: boolean
  running?: boolean
  encoder_available?: boolean
  desktop_available?: boolean
  browser_available?: boolean
  toolkit_available?: boolean
  prepare_supported?: boolean
  prepare_system?: string
  unavailable_reason?: string
}

interface DisplayOfferResponse {
  type: 'answer'
  sdp: string
  session_id?: string
}

const PREPARE_MAX_WAIT_ATTEMPTS = 12
const PREPARE_WAIT_INTERVAL_MS = 500
const HIDDEN_RELEASE_DELAY_MS = 60_000
const connections = new Map<string, {
  connection: BotDisplayConnection
  consumers: number
  retentionTimer: number | null
}>()

function delay(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    signal?.throwIfAborted()
    const abort = () => {
      window.clearTimeout(timer)
      reject(signal?.reason)
    }
    const timer = window.setTimeout(() => {
      signal?.removeEventListener('abort', abort)
      resolve()
    }, ms)
    signal?.addEventListener('abort', abort, { once: true })
  })
}

export class BotDisplayConnection {
  botId: string
  status = ref<DisplayStatus>('idle')
  prepareProgress = ref<PrepareProgress | null>(null)
  unavailableReason = ref('')
  sessionId = ref('')
  peer = shallowRef<RTCPeerConnection | null>(null)
  inputChannel = shallowRef<RTCDataChannel | null>(null)
  stream = shallowRef<MediaStream | null>(null)
  clipboardText = ref('')
  clipboardRevision = ref(0)
  clipboardError = shallowRef<unknown>(null)

  private ensureReadyPromise: Promise<boolean> | null = null
  private acquirePromise: Promise<boolean> | null = null
  private refs = 0
  private hiddenReleaseTimer: number | null = null
  private reconnectTimer: number | null = null
  private recovering = false
  private reconnectAttempts = 0
  private acquireAbort: AbortController | null = null

  constructor(botId: string) {
    this.botId = botId
  }

  addRef() {
    this.cancelHiddenRelease()
    this.refs++
  }

  removeRef() {
    if (this.refs === 0) return
    this.refs--
    if (this.refs !== 0) return

    this.cancelReconnect()
    this.hiddenReleaseTimer = window.setTimeout(() => {
      this.hiddenReleaseTimer = null
      if (this.refs !== 0) return
      this.recovering = false
      this.reconnectAttempts = 0
      this.acquireAbort?.abort()
      this.acquireAbort = null
      this.acquirePromise = null
      this.ensureReadyPromise = null
      this.cleanupPeer()
      this.closeSession()
      this.prepareProgress.value = null
      this.unavailableReason.value = ''
      this.status.value = 'idle'
    }, HIDDEN_RELEASE_DELAY_MS)
  }

  private cancelHiddenRelease() {
    if (this.hiddenReleaseTimer === null) return
    window.clearTimeout(this.hiddenReleaseTimer)
    this.hiddenReleaseTimer = null
  }

  /** Visible viewers share one acquisition and one live peer per bot. */
  acquireConnection(): Promise<boolean> {
    if (this.refs === 0) return Promise.resolve(false)
    if (this.peer.value && this.status.value === 'connected' && this.stream.value && this.inputReady()) {
      return Promise.resolve(true)
    }
    if (this.acquirePromise) return this.acquirePromise

    const abort = new AbortController()
    this.acquireAbort = abort
    const promise = this.doAcquire(abort.signal).finally(() => {
      if (this.acquirePromise === promise) this.acquirePromise = null
      if (this.acquireAbort === abort) this.acquireAbort = null
      if (!abort.signal.aborted && this.recovering && !this.peer.value) this.scheduleReconnect()
    })
    this.acquirePromise = promise
    return promise
  }

  private async doAcquire(signal: AbortSignal): Promise<boolean> {
    this.cancelReconnect()
    if (this.peer.value) this.cleanupPeer()
    this.status.value = 'connecting'
    this.unavailableReason.value = ''
    this.prepareProgress.value = null

    const ready = await this.ensureReady(signal)
    if (signal.aborted) return false
    if (!ready) {
      this.status.value = 'unavailable'
      return false
    }

    try {
      await this.createPeer(signal)
      return !signal.aborted && this.peer.value !== null
    } catch (error) {
      // A viewer may hide and return while an old acquisition settles.
      // Never clear the newer viewer's peer, status or in-flight promise.
      if (signal.aborted) return false
      this.cleanupPeer()
      this.status.value = 'unavailable'
      this.unavailableReason.value = resolveApiErrorMessage(error, this.t('chat.display.status.unavailable'))
      this.prepareProgress.value = null
      return false
    }
  }

  private async createPeer(signal: AbortSignal): Promise<void> {
    const peer = new RTCPeerConnection()
    const inputChannel = peer.createDataChannel('display-input', { ordered: true })
    this.peer.value = peer
    this.inputChannel.value = inputChannel
    const disconnect = () => {
      if (this.peer.value !== peer || this.inputChannel.value !== inputChannel) return
      this.recovering = true
      this.cleanupPeer()
      this.status.value = 'disconnected'
      this.scheduleReconnect()
    }
    inputChannel.addEventListener('close', disconnect)
    inputChannel.addEventListener('error', disconnect)
    inputChannel.addEventListener('message', (event: MessageEvent) => {
      if (this.inputChannel.value !== inputChannel) return
      const message = decodeClipboardMessage(event.data)
      if (message?.type === 'clipboard') {
        this.clipboardText.value = message.text as string
        this.clipboardRevision.value++
      } else if (message?.type === 'clipboard_error') {
        this.clipboardError.value = message
      }
    })
    peer.addTransceiver('video', { direction: 'recvonly' })

    peer.addEventListener('connectionstatechange', () => {
      if (this.peer.value !== peer) return
      switch (peer.connectionState) {
        case 'connected':
          this.recovering = false
          this.reconnectAttempts = 0
          this.status.value = 'connected'
          break
        case 'failed':
        case 'closed':
        case 'disconnected':
          disconnect()
          break
        default:
          this.status.value = 'connecting'
      }
    })
    peer.addEventListener('track', (event) => {
      if (this.peer.value !== peer) return
      this.stream.value = event.streams[0] ?? new MediaStream([event.track])
    })

    const answer = await this.exchangeOffer(peer, this.sessionId.value || undefined, signal)
    if (signal.aborted || this.peer.value !== peer) {
      this.deleteSession(answer.session_id)
      return
    }
    this.sessionId.value = answer.session_id ?? ''

    this.prepareProgress.value = null
  }

  private cleanupPeer() {
    this.cancelReconnect()
    this.stream.value = null
    this.clipboardText.value = ''
    this.clipboardRevision.value = 0
    this.clipboardError.value = null
    const channel = this.inputChannel.value
    const peer = this.peer.value
    this.inputChannel.value = null
    this.peer.value = null
    channel?.close()
    peer?.close()
  }

  private cancelReconnect() {
    if (this.reconnectTimer === null) return
    window.clearTimeout(this.reconnectTimer)
    this.reconnectTimer = null
  }

  private scheduleReconnect() {
    if (!this.recovering || this.refs === 0 || this.acquirePromise || this.reconnectTimer !== null) return
    const wait = Math.min(1000 * 2 ** Math.min(this.reconnectAttempts++, 5), 30_000)
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = null
      if (this.recovering && this.refs > 0) void this.acquireConnection().catch(() => {})
    }, wait)
  }

  /**
   * Ensure the remote display is ready to accept a WebRTC connection.
   * Only one caller per bot will run the actual prepare/check sequence;
   * concurrent callers wait for the same result.
   */
  ensureReady(signal?: AbortSignal): Promise<boolean> {
    if (this.ensureReadyPromise) return this.ensureReadyPromise
    const promise = this.doEnsureReady(signal).finally(() => {
      if (this.ensureReadyPromise === promise) this.ensureReadyPromise = null
    })
    this.ensureReadyPromise = promise
    return promise
  }

  private async doEnsureReady(signal?: AbortSignal): Promise<boolean> {
    this.unavailableReason.value = ''
    this.prepareProgress.value = null

    try {
      let info = await this.loadDisplayInfo(signal)
      signal?.throwIfAborted()
      if (!info.enabled) {
        this.unavailableReason.value = this.t('chat.display.unavailable.disabled')
        return false
      }
      if (this.canPrepareDisplay(info)) {
        const prepared = await this.prepareDisplay(signal)
        signal?.throwIfAborted()
        if (!prepared) return false
        info = await this.waitForDisplayReady(signal)
        signal?.throwIfAborted()
      }
      if (!info.available || !info.running) {
        this.unavailableReason.value = this.formatUnavailableReason(info.unavailable_reason ?? '')
        return false
      }
      if (info.desktop_available === false) {
        this.unavailableReason.value = this.formatUnavailableReason('desktop unavailable')
        return false
      }
      if (info.browser_available === false) {
        this.unavailableReason.value = this.formatUnavailableReason('browser unavailable')
        return false
      }
      return true
    } catch (error) {
      if (signal?.aborted) return false
      this.unavailableReason.value = resolveApiErrorMessage(error, this.t('chat.display.status.unavailable'))
      return false
    } finally {
      if (!signal?.aborted && this.unavailableReason.value) {
        this.prepareProgress.value = null
      }
    }
  }

  /**
   * Exchange offer/answer for the given peer and set the remote description.
   */
  async exchangeOffer(peer: RTCPeerConnection, existingSessionId?: string, signal?: AbortSignal): Promise<DisplayOfferResponse> {
    const offer = await peer.createOffer()
    await peer.setLocalDescription(offer)
    await this.waitForIceGatheringComplete(peer, signal)
    signal?.throwIfAborted()

    const local = peer.localDescription
    if (!local?.sdp) {
      throw new Error('local WebRTC offer is unavailable')
    }
    const { data } = await postBotsByBotIdContainerDisplayWebrtcOffer({
      signal,
      path: { bot_id: this.botId },
      body: {
        type: local.type,
        sdp: local.sdp,
        session_id: existingSessionId || undefined,
        candidate_host: window.location.hostname,
      },
      throwOnError: true,
    })
    if (!data?.sdp) {
      throw new Error('display WebRTC answer is empty')
    }
    const answer: DisplayOfferResponse = { type: 'answer', sdp: data.sdp, session_id: data.session_id }
    if (signal?.aborted) {
      this.deleteSession(answer.session_id)
      signal.throwIfAborted()
    }
    try {
      await peer.setRemoteDescription(new RTCSessionDescription(answer))
    } catch (error) {
      this.deleteSession(answer.session_id)
      throw error
    }
    if (!signal?.aborted) this.prepareProgress.value = null
    return answer
  }

  closeSession() {
    const sessionID = this.sessionId.value
    this.sessionId.value = ''
    this.deleteSession(sessionID)
  }

  dispose() {
    this.cancelHiddenRelease()
    this.cancelReconnect()
    this.recovering = false
    this.reconnectAttempts = 0
    this.refs = 0
    this.acquireAbort?.abort()
    this.acquireAbort = null
    this.acquirePromise = null
    this.ensureReadyPromise = null
    this.cleanupPeer()
    this.sessionId.value = ''
    this.prepareProgress.value = null
    this.unavailableReason.value = ''
    this.status.value = 'idle'
  }

  private deleteSession(sessionID?: string) {
    if (!sessionID) return
    void deleteBotsByBotIdContainerDisplaySessionsBySessionId({
      path: { bot_id: this.botId, session_id: sessionID },
    }).catch(() => {})
  }

  sendInput(payload: Record<string, unknown>) {
    if (this.refs === 0) return
    const channel = this.inputChannel.value
    if (channel?.readyState !== 'open') return
    channel.send(JSON.stringify(payload))
  }

  pasteText(text: string, shift = false): boolean {
    return this.sendClipboard(encodeClipboardPaste(text, shift))
  }

  private sendClipboard(message: string | null): boolean {
    if (this.refs === 0) return false
    const channel = this.inputChannel.value
    if (!message || channel?.readyState !== 'open') return false
    try {
      channel.send(message)
      return true
    } catch {
      return false
    }
  }

  inputReady(): boolean {
    return this.inputChannel.value?.readyState === 'open'
  }

  private async loadDisplayInfo(signal?: AbortSignal): Promise<DisplayInfoPayload> {
    const { data } = await getBotsByBotIdContainerDisplay({
      signal,
      path: { bot_id: this.botId },
      throwOnError: true,
    })
    return data ?? {}
  }

  private isDisplayReady(info: DisplayInfoPayload): boolean {
    return info.enabled === true
      && info.available === true
      && info.running === true
      && info.desktop_available !== false
      && info.browser_available !== false
  }

  private canPrepareDisplay(info: DisplayInfoPayload): boolean {
    const reason = info.unavailable_reason ?? ''
    if (!info.enabled) return false
    // Desktop can connect to older hosted servers that still return the legacy reason.
    if (reason === 'workspace is not reachable'
      || reason === 'container not reachable'
      || reason === 'manager not configured') return false
    if (info.encoder_available === false && reason === 'gstreamer unavailable') return false
    return !info.available
      || !info.running
      || info.desktop_available === false
      || info.browser_available === false
  }

  private async waitForDisplayReady(signal?: AbortSignal): Promise<DisplayInfoPayload> {
    let last = await this.loadDisplayInfo(signal)
    signal?.throwIfAborted()
    for (let attempt = 0; attempt < PREPARE_MAX_WAIT_ATTEMPTS && !this.isDisplayReady(last); attempt += 1) {
      await delay(PREPARE_WAIT_INTERVAL_MS, signal)
      last = await this.loadDisplayInfo(signal)
      signal?.throwIfAborted()
    }
    return last
  }

  private async prepareDisplay(signal?: AbortSignal): Promise<boolean> {
    this.prepareProgress.value = {
      percent: 5,
      message: this.t('chat.display.prepare.checking'),
      step: 'checking',
    }
    try {
      const { stream } = await postBotsByBotIdContainerDisplayPrepareStream({
        signal,
        path: { bot_id: this.botId },
        throwOnError: true,
      })
      for await (const event of stream) {
        signal?.throwIfAborted()
        if (event.type === 'error') {
          this.status.value = 'unavailable'
          this.unavailableReason.value = resolveApiErrorMessage(event, this.t('chat.display.prepare.failed'))
          this.prepareProgress.value = null
          return false
        }
        this.prepareProgress.value = {
          percent: event.percent ?? this.prepareProgress.value?.percent ?? 0,
          message: this.prepareEventMessage(event),
          step: event.step ?? this.prepareProgress.value?.step,
        }
        if (event.type === 'complete') {
          return true
        }
      }
      return true
    } catch (error) {
      if (signal?.aborted) return false
      this.status.value = 'unavailable'
      this.unavailableReason.value = resolveApiErrorMessage(error, this.t('chat.display.prepare.failed'))
      return false
    }
  }

  private prepareEventMessage(event: DisplayPrepareStreamEvent): string {
    switch (event.step) {
      case 'checking': return this.t('chat.display.prepare.checking')
      case 'toolkit': return this.t('chat.display.prepare.toolkit')
      case 'system': return this.t('chat.display.prepare.system')
      case 'installing': return this.t('chat.display.prepare.installing')
      case 'browser': return this.t('chat.display.prepare.browser')
      case 'starting': return this.t('chat.display.prepare.starting')
      case 'desktop': return this.t('chat.display.prepare.desktop')
      case 'styling': return this.t('chat.display.prepare.styling')
      case 'complete': return this.t('chat.display.prepare.complete')
      default: return event.message || this.t('chat.display.prepare.default')
    }
  }

  private async waitForIceGatheringComplete(pc: RTCPeerConnection, signal?: AbortSignal): Promise<void> {
    signal?.throwIfAborted()
    if (pc.iceGatheringState === 'complete') return
    return new Promise((resolve, reject) => {
      const timeout = window.setTimeout(done, 3000)
      function cleanup() {
        window.clearTimeout(timeout)
        pc.removeEventListener('icegatheringstatechange', onChange)
        signal?.removeEventListener('abort', onAbort)
      }
      function done() {
        cleanup()
        resolve()
      }
      function onAbort() {
        cleanup()
        reject(signal?.reason)
      }
      function onChange() {
        if (pc.iceGatheringState === 'complete') done()
      }
      pc.addEventListener('icegatheringstatechange', onChange)
      signal?.addEventListener('abort', onAbort, { once: true })
    })
  }

  private t(key: string) {
    return i18n.global.t(key)
  }

  private formatUnavailableReason(reason: string): string {
    const map: Record<string, string> = {
      'workspace is not reachable': this.t('chat.display.unavailable.container'),
      'container not reachable': this.t('chat.display.unavailable.container'),
      'display bundle unavailable': this.t('chat.display.unavailable.bundle'),
      'display server not reachable': this.t('chat.display.unavailable.server'),
      'gstreamer unavailable': this.t('chat.display.unavailable.encoder'),
      'manager not configured': this.t('chat.display.unavailable.manager'),
      'browser unavailable': this.t('chat.display.unavailable.browser'),
      'desktop unavailable': this.t('chat.display.unavailable.desktop'),
      'toolkit unavailable': this.t('chat.display.unavailable.toolkit'),
    }
    return map[reason] || reason || this.t('chat.display.status.unavailable')
  }
}

onAuthSessionCleared(() => {
  for (const entry of connections.values()) {
    if (entry.retentionTimer !== null) {
      window.clearTimeout(entry.retentionTimer)
      entry.retentionTimer = null
    }
    entry.consumers = 0
    entry.connection.dispose()
  }
  connections.clear()
})

export function useBotDisplayConnection(botId: string, visible: MaybeRefOrGetter<boolean> = true) {
  let entry = connections.get(botId)
  if (!entry) {
    entry = { connection: new BotDisplayConnection(botId), consumers: 0, retentionTimer: null }
    connections.set(botId, entry)
  }
  const current = entry
  if (current.retentionTimer !== null) {
    window.clearTimeout(current.retentionTimer)
    current.retentionTimer = null
  }
  const conn = current.connection
  current.consumers++
  let viewing = false
  const stop = watch(() => toValue(visible), (active) => {
    if (active === viewing) return
    viewing = active
    if (active) {
      conn.addRef()
      void conn.acquireConnection().catch(() => {})
    } else {
      conn.removeRef()
    }
  }, { immediate: true, flush: 'post' })
  onScopeDispose(() => {
    stop()
    if (viewing) conn.removeRef()
    if (current.consumers > 0) current.consumers--
    if (current.consumers === 0 && connections.get(botId) === current) {
      current.retentionTimer = window.setTimeout(() => {
        current.retentionTimer = null
        if (current.consumers === 0 && connections.get(botId) === current) connections.delete(botId)
      }, HIDDEN_RELEASE_DELAY_MS)
    }
  })
  return conn
}
