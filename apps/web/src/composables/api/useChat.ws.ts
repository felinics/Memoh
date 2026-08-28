import { sdkAuthQuery, sdkWebSocketUrl } from '@/lib/api-client'
import type {
  ChatAttachment,
  RequestedSkillRequest,
  RuntimeCursor,
  UIStreamEvent,
  UIStreamEventHandler,
} from './useChat.types'

export interface WSUserInputAnswer {
  question_id: string
  option_ids?: string[]
  custom_text?: string
  text?: string
  skipped?: boolean
}

interface WSTurnOptions {
  model_id?: string
  reasoning_effort?: string
  workspace_target_id?: string
}

export type WSClientMessage =
  | ({
      type: 'message'
      invocation_id: string
      session_id?: string
      composer_scope?: string
      text?: string
      attachments?: ChatAttachment[]
      requested_skills?: RequestedSkillRequest[]
    } & WSTurnOptions)
  | ({
      type: 'retry_message'
      invocation_id: string
      session_id: string
      /** The turn being replaced. A client holds it from admission onward,
       *  unlike a stored message id, which only exists once the round has
       *  been persisted and fetched back. */
      turn_id: string
    } & WSTurnOptions)
  | ({
      type: 'edit_message'
      invocation_id: string
      session_id: string
      /** See retry_message. */
      turn_id: string
      text?: string
      attachments?: ChatAttachment[]
    } & WSTurnOptions)
  | {
      type: 'abort'
      run_id: string
      session_id: string
      control_id: string
    }
  | {
      type: 'tool_approval_response'
      run_id: string
      session_id: string
      decision_id: string
      control_id: string
      decision: 'approve' | 'reject'
      /** Agent-provided permission option id, when the user picked one. */
      option_id?: string
      reason?: string
    }
  | {
      type: 'user_input_response'
      run_id: string
      session_id: string
      decision_id: string
      control_id: string
      answers?: WSUserInputAnswer[]
      canceled?: boolean
      reason?: string
    }
  | {
      type: 'runtime_subscribe'
      session_id: string
      cursor?: RuntimeCursor
    }
  | {
      type: 'runtime_unsubscribe'
      session_id: string
    }

function reliableRequestKey(message: WSClientMessage): string {
  switch (message.type) {
    case 'message':
    case 'retry_message':
    case 'edit_message':
      return `invocation:${message.invocation_id.trim()}`
    case 'abort':
    case 'tool_approval_response':
    case 'user_input_response':
      return `control:${message.control_id.trim()}`
    default:
      return ''
  }
}

function acknowledgedRequestKey(event: UIStreamEvent): string {
  if (
    event.type === 'run_accepted'
    || event.type === 'run_rejected'
    || event.type === 'command_result'
    || event.type === 'command_error'
    || event.type === 'error'
  ) {
    const invocationId = event.invocation_id?.trim()
    return invocationId ? `invocation:${invocationId}` : ''
  }
  if (event.type === 'control_ack') {
    const controlId = event.control_id.trim()
    return controlId ? `control:${controlId}` : ''
  }
  return ''
}

export interface ChatWebSocket {
  send: (msg: WSClientMessage) => void
  abort: (runId: string, sessionId: string, controlId: string) => void
  close: () => void
  readonly connected: boolean
  onOpen: (() => void) | null
  onClose: (() => void) | null
}

function resolveWebSocketUrl(botId: string): string {
  return sdkWebSocketUrl({
    url: '/bots/{bot_id}/web/ws',
    path: { bot_id: botId },
    query: sdkAuthQuery(),
  })
}

export function connectWebSocket(
  botId: string,
  onStreamEvent: UIStreamEventHandler,
): ChatWebSocket {
  const id = botId.trim()
  if (!id) throw new Error('bot id is required')

  const url = resolveWebSocketUrl(id)

  let ws: WebSocket | null = null
  let isConnected = false
  let closed = false
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let reconnectDelay = 1000
  const sendQueue: string[] = []
  const pendingReliableRequests = new Map<string, string>()

  const handle: ChatWebSocket = {
    send(msg: WSClientMessage) {
      const payload = JSON.stringify(msg)
      const reliableKey = reliableRequestKey(msg)
      if (reliableKey) {
        pendingReliableRequests.set(reliableKey, payload)
        if (ws && ws.readyState === WebSocket.OPEN) ws.send(payload)
        return
      }
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(payload)
        return
      }
      sendQueue.push(payload)
    },
    abort(runId: string, sessionId: string, controlId: string) {
      const id = runId.trim()
      const sid = sessionId.trim()
      const cid = controlId.trim()
      if (!id || !sid || !cid) return
      handle.send({
        type: 'abort',
        run_id: id,
        session_id: sid,
        control_id: cid,
      })
    },
    close() {
      closed = true
      if (reconnectTimer) {
        clearTimeout(reconnectTimer)
        reconnectTimer = null
      }
      if (ws) {
        ws.close()
        ws = null
      }
      isConnected = false
      sendQueue.length = 0
      pendingReliableRequests.clear()
    },
    get connected() {
      return isConnected
    },
    onOpen: null,
    onClose: null,
  }

  function connect() {
    if (closed) return
    const socket = new WebSocket(url)
    ws = socket

    socket.onopen = () => {
      isConnected = true
      reconnectDelay = 1000
      for (const payload of pendingReliableRequests.values()) socket.send(payload)
      while (sendQueue.length > 0 && socket.readyState === WebSocket.OPEN) {
        socket.send(sendQueue.shift()!)
      }
      handle.onOpen?.()
    }

    socket.onclose = () => {
      isConnected = false
      handle.onClose?.()
      scheduleReconnect()
    }

    socket.onerror = () => {
      // onerror is always followed by onclose; reconnect handled there.
    }

    socket.onmessage = (event) => {
      if (typeof event.data !== 'string') return
      try {
        const parsed = JSON.parse(event.data)
        if (!parsed || typeof parsed !== 'object') return
        const eventType = String(parsed.type ?? '').trim()
        if (
          eventType !== 'run_accepted'
          && eventType !== 'run_rejected'
          && eventType !== 'error'
          && eventType !== 'session_created'
          && eventType !== 'command_result'
          && eventType !== 'command_error'
          && eventType !== 'runtime_snapshot'
          && eventType !== 'runtime_delta'
          && eventType !== 'runtime_dropped'
          && eventType !== 'control_ack'
        ) {
          return
        }
        const streamEvent = parsed as UIStreamEvent
        const acknowledged = acknowledgedRequestKey(streamEvent)
        if (acknowledged) pendingReliableRequests.delete(acknowledged)
        onStreamEvent(streamEvent)
      } catch {
        // Ignore unparsable messages.
      }
    }
  }

  function scheduleReconnect() {
    if (closed) return
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null
      connect()
    }, reconnectDelay)
    reconnectDelay = Math.min(reconnectDelay * 1.5, 10000)
  }

  connect()
  return handle
}
