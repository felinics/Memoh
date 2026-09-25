export interface TerminalLink {
  sessionId: string | null
  offset: number
  mode: 'open' | 'attach'
  attempt: number
  releasing: boolean
}

export type TerminalNotice = 'gone' | 'unsupported' | 'full' | 'taken' | null

export interface TerminalCloseStep {
  link: TerminalLink
  action: 'connect' | 'stop' | 'close-tab'
  delayMs: number
  notice: TerminalNotice
}

export interface TerminalReady {
  sessionId?: string
  offset?: number
  truncated?: boolean
}

const RECONNECT_DELAYS_MS = [500, 1000, 2000, 4000, 10000]

export function createTerminalLink(sessionId: string | null, offset: number): TerminalLink {
  const id = sessionId?.trim() || null
  return {
    sessionId: id,
    offset: id ? Math.max(0, offset) : 0,
    mode: id ? 'attach' : 'open',
    attempt: 0,
    releasing: false,
  }
}

export function reconnectDelay(attempt: number): number {
  const index = Math.min(Math.max(attempt, 1), RECONNECT_DELAYS_MS.length) - 1
  return RECONNECT_DELAYS_MS[index] ?? 10000
}

export function openFrame(link: TerminalLink, cols: number, rows: number) {
  if (link.mode === 'attach' && link.sessionId) {
    return {
      type: 'attach' as const,
      sessionId: link.sessionId,
      since: link.offset,
      cols,
      rows,
    }
  }
  return { type: 'open' as const, cols, rows }
}

export function onReady(link: TerminalLink, ready: TerminalReady): { link: TerminalLink, reset: boolean } {
  const sessionId = ready.sessionId?.trim() || link.sessionId
  const readyOffset = Number.isFinite(ready.offset) ? Math.max(0, Number(ready.offset)) : link.offset
  const reset = ready.truncated === true
  const offset = reset || readyOffset > link.offset ? readyOffset : link.offset
  return {
    reset,
    link: {
      ...link,
      sessionId,
      offset,
      mode: 'attach',
      attempt: 0,
    },
  }
}

export function onBytes(link: TerminalLink, byteLength: number): TerminalLink {
  if (byteLength <= 0) return link
  return { ...link, offset: link.offset + byteLength }
}

export function markRelease(link: TerminalLink): TerminalLink {
  return { ...link, releasing: true }
}

export function manualReconnect(link: TerminalLink): TerminalLink {
  return {
    ...link,
    releasing: false,
    attempt: 0,
    mode: link.sessionId ? 'attach' : 'open',
  }
}

export function onSocketClose(link: TerminalLink, code: number): TerminalCloseStep {
  if (link.releasing) {
    return { link, action: 'stop', delayMs: 0, notice: null }
  }
  if (code === 1000) {
    return {
      link: { ...link, sessionId: null, offset: 0, mode: 'open', attempt: 0 },
      action: 'close-tab',
      delayMs: 0,
      notice: null,
    }
  }
  if (code === 4000) {
    return {
      link: { ...link, sessionId: null, offset: 0, mode: 'open', attempt: 0 },
      action: 'connect',
      delayMs: 0,
      notice: 'gone',
    }
  }
  if (code === 4001) {
    return { link, action: 'stop', delayMs: 0, notice: 'unsupported' }
  }
  if (code === 4002) {
    return { link, action: 'stop', delayMs: 0, notice: 'full' }
  }
  if (code === 4003) {
    return { link, action: 'stop', delayMs: 0, notice: 'taken' }
  }
  const attempt = link.attempt + 1
  return {
    link: {
      ...link,
      attempt,
      mode: link.sessionId ? 'attach' : 'open',
    },
    action: 'connect',
    delayMs: reconnectDelay(attempt),
    notice: null,
  }
}
