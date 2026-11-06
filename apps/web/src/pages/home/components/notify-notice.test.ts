import { describe, expect, it } from 'vitest'
import type { ContentBlock } from '@/store/chat-list'
import { isNotifyNotice } from './notify-notice'

function tool(toolName: string, result: unknown): ContentBlock {
  return { type: 'tool', id: 't1', toolCallId: 'c1', toolName, input: {}, result, done: result !== null } as unknown as ContentBlock
}

describe('isNotifyNotice', () => {
  it('is a notify_user call that reached the user, first time or not', () => {
    expect(isNotifyNotice(tool('notify_user', { structuredContent: { status: 'notified' } }))).toBe(true)
    expect(isNotifyNotice(tool('notify_user', { status: 'already_notified' }))).toBe(true)
  })

  it('leaves a running, refused or unrelated call in the tool group', () => {
    expect(isNotifyNotice(tool('notify_user', null))).toBe(false)
    expect(isNotifyNotice(tool('notify_user', { status: 'not_eligible' }))).toBe(false)
    expect(isNotifyNotice(tool('send', { status: 'notified' }))).toBe(false)
    expect(isNotifyNotice({ type: 'text', id: 'x1', content: 'notified' } as unknown as ContentBlock)).toBe(false)
  })
})
