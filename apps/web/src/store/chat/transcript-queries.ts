import { messageIdentityId } from '../chat-list.normalize'
import type { ChatAssistantTurn, ChatMessage, ChatUserTurn } from './types'

export function createTranscriptQueries(messages: ChatMessage[]) {
  function latestOptimisticUserText(): string {
    for (let index = messages.length - 1; index >= 0; index -= 1) {
      const message = messages[index]
      if (message?.role === 'user') return message.text.trim()
    }
    return ''
  }

  // A turn id names a round, and a round has two halves on screen: retry
  // addresses its assistant half, edit its user half. The role is part of the
  // lookup rather than an afterthought.
  function findTurnByTurnId(turnId: string, role: 'user'): ChatUserTurn | null
  function findTurnByTurnId(turnId: string, role: 'assistant'): ChatAssistantTurn | null
  function findTurnByTurnId(turnId: string, role: 'user' | 'assistant'): ChatUserTurn | ChatAssistantTurn | null {
    const id = turnId.trim()
    if (!id) return null
    for (const turn of messages) {
      if (turn.role === role && turn.turnId?.trim() === id) return turn
    }
    return null
  }

  function latestVisibleTurn(role: 'user'): ChatUserTurn | null
  function latestVisibleTurn(role: 'assistant'): ChatAssistantTurn | null
  function latestVisibleTurn(role: ChatMessage['role']): ChatUserTurn | ChatAssistantTurn | null {
    for (let index = messages.length - 1; index >= 0; index -= 1) {
      const turn = messages[index]
      if (turn && turn.role !== 'system' && turn.role === role && !turn.__optimistic) return turn
    }
    return null
  }

  function isLatestVisibleUserTurn(turn: ChatMessage): turn is ChatUserTurn {
    if (turn.role !== 'user') return false
    const latest = latestVisibleTurn('user')
    return Boolean(latest && messageIdentityId(latest) === messageIdentityId(turn))
  }

  function isLatestVisibleAssistantTurn(turn: ChatMessage): turn is ChatAssistantTurn {
    if (turn.role !== 'assistant') return false
    const latest = latestVisibleTurn('assistant')
    return Boolean(latest && messageIdentityId(latest) === messageIdentityId(turn))
  }

  return {
    latestOptimisticUserText,
    hasTurn: (turn: ChatMessage) => messages.includes(turn),
    findTurnByTurnId,
    isLatestVisibleUserTurn,
    isLatestVisibleAssistantTurn,
  }
}
