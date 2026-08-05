import { serverMessageId } from '../chat-list.normalize'
import type { ChatAssistantTurn, ChatMessage, ChatUserTurn } from './types'

export function createTranscriptQueries(messages: ChatMessage[]) {
  function latestOptimisticUserText(): string {
    for (let index = messages.length - 1; index >= 0; index -= 1) {
      const message = messages[index]
      if (message?.role === 'user') return message.text.trim()
    }
    return ''
  }

  function findTurnByServerId(messageId: string): ChatMessage | null {
    const id = messageId.trim()
    if (!id) return null
    return messages.find(turn => serverMessageId(turn) === id) ?? null
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
    return Boolean(latest && serverMessageId(latest) === serverMessageId(turn))
  }

  function isLatestVisibleAssistantTurn(turn: ChatMessage): turn is ChatAssistantTurn {
    if (turn.role !== 'assistant') return false
    const latest = latestVisibleTurn('assistant')
    return Boolean(latest && serverMessageId(latest) === serverMessageId(turn))
  }

  return {
    latestOptimisticUserText,
    hasTurn: (turn: ChatMessage) => messages.includes(turn),
    findTurnByServerId,
    isLatestVisibleUserTurn,
    isLatestVisibleAssistantTurn,
  }
}
