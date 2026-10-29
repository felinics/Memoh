import { describe, expect, it } from 'vitest'
import { canHoldUnread } from './session-attention'

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
