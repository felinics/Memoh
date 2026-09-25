import { describe, expect, it } from 'vitest'
import {
  createTerminalLink,
  markRelease,
  onBytes,
  onReady,
  onSocketClose,
  openFrame,
  reconnectDelay,
} from './terminal-session'

describe('terminal session link', () => {
  it('opens a new shell when there is no session', () => {
    const link = createTerminalLink(null, 40)
    expect(openFrame(link, 80, 24)).toEqual({ type: 'open', cols: 80, rows: 24 })
    expect(link.offset).toBe(0)
  })

  it('attaches with the stored offset', () => {
    const link = createTerminalLink('sess-1', 12)
    expect(openFrame(link, 80, 24)).toEqual({
      type: 'attach',
      sessionId: 'sess-1',
      since: 12,
      cols: 80,
      rows: 24,
    })
  })

  it('keeps the screen when the replay continues the same offset', () => {
    const link = createTerminalLink('sess-1', 20)
    const applied = onReady(link, { sessionId: 'sess-1', offset: 20, truncated: false })
    expect(applied.reset).toBe(false)
    expect(applied.link.offset).toBe(20)
    expect(onBytes(applied.link, 4).offset).toBe(24)
  })

  it('resets when the server dropped the head of the buffer', () => {
    const applied = onReady(createTerminalLink('sess-1', 4), {
      sessionId: 'sess-1',
      offset: 100,
      truncated: true,
    })
    expect(applied.reset).toBe(true)
    expect(applied.link.offset).toBe(100)
  })

  it('closes the tab when the shell exits', () => {
    const step = onSocketClose(createTerminalLink('sess-1', 8), 1000)
    expect(step.action).toBe('close-tab')
    expect(step.notice).toBeNull()
    expect(step.link.sessionId).toBeNull()
  })

  it('reconnects the same session after an abnormal close', () => {
    const step = onSocketClose(createTerminalLink('sess-1', 8), 1006)
    expect(step.action).toBe('connect')
    expect(step.notice).toBeNull()
    expect(step.delayMs).toBe(500)
    expect(step.link.sessionId).toBe('sess-1')
    expect(step.link.mode).toBe('attach')
    expect(reconnectDelay(5)).toBe(10000)
  })

  it('does not reconnect while the tab is releasing', () => {
    const step = onSocketClose(markRelease(createTerminalLink('sess-1', 1)), 1006)
    expect(step.action).toBe('stop')
    expect(step.notice).toBeNull()
  })

  it('clears a missing session and opens a new shell', () => {
    const step = onSocketClose(createTerminalLink('sess-1', 8), 4000)
    expect(step.notice).toBe('gone')
    expect(step.action).toBe('connect')
    expect(step.link.sessionId).toBeNull()
    expect(openFrame(step.link, 80, 24).type).toBe('open')
  })

  it('stops automatic reconnect when the workspace or session cannot accept it', () => {
    expect(onSocketClose(createTerminalLink(null, 0), 4001).action).toBe('stop')
    expect(onSocketClose(createTerminalLink(null, 0), 4001).notice).toBe('unsupported')
    expect(onSocketClose(createTerminalLink(null, 0), 4002).notice).toBe('full')
    expect(onSocketClose(createTerminalLink('sess-1', 1), 4003).notice).toBe('taken')
  })
})