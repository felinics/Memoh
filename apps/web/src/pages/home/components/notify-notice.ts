import type { ContentBlock } from '@/store/chat-list'
import { structuredToolResult } from '@/store/chat-list.normalize'

/**
 * A notify_user call that reached the user. It is the part of a scheduled
 * run the user was pointed at, so it reads as conversation content: it breaks
 * out of the tool group as a notice, and opening the notification scrolls to
 * it. A call still running, or one whose session cannot carry unread state,
 * stays in the group as an ordinary tool row.
 */
export function isNotifyNotice(block: ContentBlock): boolean {
  if (block.type !== 'tool' || block.toolName !== 'notify_user') return false
  const status = structuredToolResult(block.result).status
  return status === 'notified' || status === 'already_notified'
}
