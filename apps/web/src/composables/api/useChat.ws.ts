import { sdkAuthQuery, sdkWebSocketUrl } from '@/lib/api-client'
import type { ChatAttachment, RequestedSkillRequest, UIStreamEvent, UIStreamEventHandler } from './useChat.types'

export interface WSUserInputAnswer {
  question_id: string
  option_ids?: string[]
  custom_text?: string
  text?: string
}

export interface WSClientMessage {
  type: 'message' | 'abort' | 'tool_approval_response' | 'user_input_response' | 'retry_message' | 'edit_message'
  // invocation_id names the submission and is what every turn-starting message
  // carries. run_id names work the server already accepted, so only abort
  // carries it — and only after run_accepted told us the id.
  invocation_id?: string
  run_id?: string
  composer_scope?: string
  text?: string
  session_id?: string
  message_id?: string
  attachments?: ChatAttachment[]
  requested_skills?: RequestedSkillRequest[]
  model_id?: string
  reasoning_effort?: string
  workspace_target_id?: string
  approval_id?: string
  user_input_id?: string
  short_id?: number
  tool_call_id?: string
  decision?: 'approve' | 'reject'
  reason?: string
  answers?: WSUserInputAnswer[]
  canceled?: boolean
}

export interface ChatWebSocket {
  send: (msg: WSClientMessage) => void
  abort: (runId: string) => void
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

  const wsUrl = resolveWebSocketUrl(id)
  const url = wsUrl

  let ws: WebSocket | null = null
  let isConnected = false
  let closed = false
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let reconnectDelay = 1000
  const sendQueue: string[] = []

  const handle: ChatWebSocket = {
    send(msg: WSClientMessage) {
      const payload = JSON.stringify(msg)
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(payload)
        return
      }
      sendQueue.push(payload)
    },
    abort(runId: string) {
      const id = runId.trim()
      if (!id) return
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'abort', run_id: id }))
      }
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
    },
    get connected() {
      return isConnected
    },
    onOpen: null,
    onClose: null,
  }

  function connect() {
    if (closed) return
    ws = new WebSocket(url)

    ws.onopen = () => {
      isConnected = true
      reconnectDelay = 1000
      while (sendQueue.length > 0 && ws?.readyState === WebSocket.OPEN) {
        ws.send(sendQueue.shift()!)
      }
      handle.onOpen?.()
    }

    ws.onclose = () => {
      isConnected = false
      handle.onClose?.()
      scheduleReconnect()
    }

    ws.onerror = () => {
      // onerror is always followed by onclose; reconnect handled there.
    }

    ws.onmessage = (event) => {
      if (typeof event.data !== 'string') return
      try {
        const parsed = JSON.parse(event.data)
        if (!parsed || typeof parsed !== 'object') return
        const eventType = String(parsed.type ?? '').trim()
        if (
          eventType !== 'run_accepted'
          && eventType !== 'run_rejected'
          && eventType !== 'start'
          && eventType !== 'message'
          && eventType !== 'end'
          && eventType !== 'error'
          && eventType !== 'session_created'
          && eventType !== 'user_message'
          && eventType !== 'command_result'
          && eventType !== 'command_error'
        ) {
          return
        }
        onStreamEvent(parsed as UIStreamEvent)
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
