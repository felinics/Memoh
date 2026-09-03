import { watch, type Ref } from 'vue'
import type { ChatMessage } from '@/store/chat-list'
import { isRuntimeContinuationUserTurn, isRuntimeSteerTurnId } from '@/store/chat/types'

function runtimeSignature(message: ChatMessage): string {
  if (message.role !== 'user' && message.role !== 'assistant') {
    return `${message.id}\u0000${message.role}\u0000${message.turnId ?? ''}`
  }
  return `${message.id}\u0000${message.role}\u0000${message.turnId ?? ''}\u0000${message.runtimeRunId ?? ''}\u0000${message.runtimeContinuation ? 'continuation' : ''}`
}

function watchQueueTurn(
  messages: Ref<ChatMessage[]>,
  matches: (message: ChatMessage) => boolean,
  signature: (message: ChatMessage) => string,
  pin: (anchorId: string) => void,
) {
  const seen = new Set(messages.value.filter(matches).map(message => message.id))

  watch(
    () => messages.value.map(signature),
    () => {
      const queueTurns = messages.value.filter(matches)
      const fresh = queueTurns.filter(message => !seen.has(message.id))
      for (const message of queueTurns) seen.add(message.id)
      const turn = fresh[fresh.length - 1]
      if (!turn) return
      for (let cursor = messages.value.indexOf(turn) - 1; cursor >= 0; cursor -= 1) {
        const previous = messages.value[cursor]
        if (previous?.role !== 'user') continue
        const anchorId = previous.id.trim()
        if (anchorId) pin(anchorId)
        break
      }
    },
    { flush: 'sync' },
  )
}

export function useQueueTurnAnchors(
  messages: Ref<ChatMessage[]>,
  pinAfterSteer: (anchorId: string) => void,
  pinAfterFollowUp: (anchorId: string) => void,
) {
  watchQueueTurn(
    messages,
    message => message.role === 'user' && isRuntimeSteerTurnId(message.turnId),
    message => `${message.id}\u0000${message.role}\u0000${message.turnId ?? ''}`,
    pinAfterSteer,
  )
  watchQueueTurn(
    messages,
    isRuntimeContinuationUserTurn,
    runtimeSignature,
    pinAfterFollowUp,
  )
}
