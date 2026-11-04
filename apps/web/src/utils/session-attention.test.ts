import { describe, expect, it } from 'vitest'
import { attentionBadgeVariant, canHoldUnread, mostUrgentKind } from './session-attention'

const me = 'u1'

describe('canHoldUnread', () => {
  it('accepts the caller\'s own web chats and external-agent chats', () => {
    expect(canHoldUnread({ type: 'chat', created_by_user_id: me }, me)).toBe(true)
    expect(canHoldUnread({ type: 'acp_agent', channel_type: 'local', created_by_user_id: me }, me)).toBe(true)
  })

  it('rejects what the server would skip', () => {
    expect(canHoldUnread({ type: 'chat', created_by_user_id: 'u2' }, me)).toBe(false)
    expect(canHoldUnread({ type: 'chat' }, me)).toBe(false)
    expect(canHoldUnread({ type: 'schedule', created_by_user_id: me }, me)).toBe(false)
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
