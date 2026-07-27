import type {
  UIToolApproval,
  UIUserInput,
} from '@/composables/api/useChat.types'
import {
  cloneToolApprovalState,
  cloneUserInputState,
} from '../chat-list.normalize'
import type {
  ChatAssistantTurn,
  ChatMessage,
  ToolCallBlock,
} from './types'

interface UserInputStateSnapshot {
  block: ToolCallBlock
  userInput: UIUserInput
}

interface ToolApprovalStateSnapshot {
  block: ToolCallBlock
  approval: UIToolApproval
}

export function createTranscriptDecisions(messages: ChatMessage[]) {
  function forEachToolBlock(visitor: (block: ToolCallBlock) => void) {
    for (const message of messages) {
      if (message.role !== 'assistant') continue
      for (const block of message.messages) {
        if (block.type === 'tool') visitor(block)
      }
    }
  }

  function snapshotToolApprovalStates(approvalId: string) {
    const id = approvalId.trim()
    if (!id) return []
    const snapshots: ToolApprovalStateSnapshot[] = []
    forEachToolBlock((block) => {
      if (block.approval?.approval_id === id) {
        snapshots.push({
          block,
          approval: cloneToolApprovalState(block.approval),
        })
      }
    })
    return snapshots
  }

  function assistantTurnForApproval(approvalId: string) {
    const id = approvalId.trim()
    if (!id) return null
    return messages.find((message): message is ChatAssistantTurn =>
      message.role === 'assistant'
      && message.messages.some(block =>
        block.type === 'tool' && block.approval?.approval_id === id),
    ) ?? null
  }

  function restoreToolApprovalStates(snapshots: ToolApprovalStateSnapshot[]) {
    for (const snapshot of snapshots) {
      if (
        snapshot.block.approval?.approval_id
        !== snapshot.approval.approval_id
      ) continue
      snapshot.block.approval = cloneToolApprovalState(snapshot.approval)
    }
  }

  function snapshotUserInputStates(userInputId: string) {
    const id = userInputId.trim()
    if (!id) return []
    const snapshots: UserInputStateSnapshot[] = []
    forEachToolBlock((block) => {
      if (block.userInput?.user_input_id === id) {
        snapshots.push({
          block,
          userInput: cloneUserInputState(block.userInput),
        })
      }
    })
    return snapshots
  }

  function assistantTurnForUserInput(userInputId: string) {
    const id = userInputId.trim()
    if (!id) return null
    return messages.find((message): message is ChatAssistantTurn =>
      message.role === 'assistant'
      && message.messages.some(block =>
        block.type === 'tool' && block.userInput?.user_input_id === id),
    ) ?? null
  }

  function restoreUserInputStates(snapshots: UserInputStateSnapshot[]) {
    for (const snapshot of snapshots) {
      if (
        snapshot.block.userInput?.user_input_id
        !== snapshot.userInput.user_input_id
      ) continue
      snapshot.block.userInput = cloneUserInputState(snapshot.userInput)
    }
  }

  function markToolApprovalDecision(
    approvalId: string,
    status: 'approved' | 'rejected' | 'pending',
  ) {
    const id = approvalId.trim()
    if (!id) return
    forEachToolBlock((block) => {
      if (block.approval?.approval_id === id) {
        block.approval = {
          ...block.approval,
          status,
          can_approve: status === 'pending',
        }
      }
    })
  }

  function markUserInputDecision(
    userInputId: string,
    status: 'submitted' | 'canceled',
  ) {
    const id = userInputId.trim()
    if (!id) return
    forEachToolBlock((block) => {
      if (block.userInput?.user_input_id === id) {
        block.userInput = { ...block.userInput, status, can_respond: false }
      }
    })
  }

  return {
    snapshotToolApprovalStates,
    assistantTurnForApproval,
    restoreToolApprovalStates,
    snapshotUserInputStates,
    assistantTurnForUserInput,
    restoreUserInputStates,
    markToolApprovalDecision,
    markUserInputDecision,
  }
}
