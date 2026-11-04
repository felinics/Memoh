import { describe, expect, it } from 'vitest'
import { attentionBadgeVariant, attentionFocusOf, canHoldUnread, mostUrgentKind, sessionAttentionPath } from './session-attention'

const me = 'u1'

describe('canHoldUnread', () => {
  it('accepts the caller\'s own web chats and external-agent chats', () => {
    expect(canHoldUnread({ type: 'chat', created_by_user_id: me }, me)).toBe(true)
    expect(canHoldUnread({ type: 'acp_agent', channel_type: 'local', created_by_user_id: me }, me)).toBe(true)
  })

  it('accepts the user\'s own schedule runs', () => {
    expect(canHoldUnread({ type: 'schedule', created_by_user_id: me }, me)).toBe(true)
  })

  it('rejects what the server would skip', () => {
    expect(canHoldUnread({ type: 'chat', created_by_user_id: 'u2' }, me)).toBe(false)
    expect(canHoldUnread({ type: 'chat' }, me)).toBe(false)
    expect(canHoldUnread({ type: 'discuss', created_by_user_id: me }, me)).toBe(false)
    expect(canHoldUnread({ type: 'subagent', created_by_user_id: me }, me)).toBe(false)
    expect(canHoldUnread({ type: 'chat', channel_type: 'telegram', created_by_user_id: me }, me)).toBe(false)
    expect(canHoldUnread({ type: 'chat', created_by_user_id: me }, '')).toBe(false)
  })
})

describe('mostUrgentKind', () => {
  it('ranks a failure over waiting on the user over a plain reply', () => {
    expect(mostUrgentKind(['reply', 'failed', 'needs_input'])).toBe('failed')
    expect(mostUrgentKind(['marked', 'needs_input', 'reply'])).toBe('needs_input')
    expect(mostUrgentKind(['marked', 'reply'])).toBe('marked')
  })

  it('has no kind for nothing unread', () => {
    expect(mostUrgentKind([])).toBeNull()
  })
})

describe('attentionBadgeVariant', () => {
  it('colors a count red, yellow or brand by its most urgent kind', () => {
    expect(attentionBadgeVariant(['reply', 'failed'])).toBe('destructive')
    expect(attentionBadgeVariant(['reply', 'needs_input'])).toBe('warning')
    expect(attentionBadgeVariant(['reply', 'marked'])).toBe('brand')
    expect(attentionBadgeVariant([])).toBe('brand')
  })
})

describe('sessionAttentionPath', () => {
  it('opens the session, and asks for the notice and the run row only when told to', () => {
    expect(sessionAttentionPath('b1', 's1')).toBe('/bot/b1?session=s1')
    expect(sessionAttentionPath('b1', 's1', { notice: true, scheduleId: 'sch1' })).toBe('/bot/b1?session=s1&notice=1&schedule=sch1')
    expect(sessionAttentionPath('b 1', 's1', { notice: false })).toBe('/bot/b%201?session=s1')
  })
})

describe('attentionFocusOf', () => {
  it('reveals the notice for an update and the run row for a schedule run', () => {
    expect(attentionFocusOf({ kind: 'update', scheduleId: 'sch1' })).toEqual({ notice: true, scheduleId: 'sch1' })
    expect(attentionFocusOf({ kind: 'failed', scheduleId: 'sch1' })).toEqual({ notice: false, scheduleId: 'sch1' })
    expect(attentionFocusOf({ kind: 'update' })).toEqual({ notice: true, scheduleId: undefined })
  })
})
