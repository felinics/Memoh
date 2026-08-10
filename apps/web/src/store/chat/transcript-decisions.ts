import type {
  UIToolApproval,
  UIUserInput,
  UIUserInputAnswer,
} from '@/composables/api/useChat.types'
import type { WSUserInputAnswer } from '@/composables/api/useChat.ws'
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
  toolCallId: string
  userInput: UIUserInput
}

interface ToolApprovalStateSnapshot {
  toolCallId: string
  approval: UIToolApproval
}

function projectSubmittedAnswers(
  userInput: UIUserInput,
  submitted: WSUserInputAnswer[] | undefined,
): UIUserInputAnswer[] {
  const questions = new Map(userInput.questions?.map(question => [question.id, question]) ?? [])
  return (submitted ?? []).flatMap((answer) => {
    const questionId = answer.question_id.trim()
    if (!questionId) return []
    const question = questions.get(questionId)
    const selected = (answer.option_ids ?? []).flatMap((optionId) => {
      const id = optionId.trim()
      if (!id) return []
      const option = question?.options?.find(candidate => candidate.id === id)
      return [{ id, label: option?.label ?? id }]
    })
    const projected: UIUserInputAnswer = {
      question_id: questionId,
      question: question?.text ?? questionId,
    }
    if (selected.length > 0) projected.selected = selected
    if (answer.custom_text?.trim()) projected.custom_text = answer.custom_text.trim()
    if (answer.text?.trim()) projected.text = answer.text.trim()
    return [projected]
  })
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
          toolCallId: block.toolCallId,
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
    forEachToolBlock((block) => {
      const current = block.approval
      if (!current) return
      const snapshot = snapshots.find(candidate =>
        candidate.toolCallId === block.toolCallId
        && candidate.approval.approval_id === current.approval_id,
      )
      if (!snapshot) return
      block.approval = cloneToolApprovalState(snapshot.approval)
    })
  }

  function snapshotUserInputStates(userInputId: string) {
    const id = userInputId.trim()
    if (!id) return []
    const snapshots: UserInputStateSnapshot[] = []
    forEachToolBlock((block) => {
      if (block.userInput?.user_input_id === id) {
        snapshots.push({
          toolCallId: block.toolCallId,
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
    forEachToolBlock((block) => {
      const current = block.userInput
      if (!current) return
      const snapshot = snapshots.find(candidate =>
        candidate.toolCallId === block.toolCallId
        && candidate.userInput.user_input_id === current.user_input_id,
      )
      if (!snapshot) return
      block.userInput = cloneUserInputState(snapshot.userInput)
    })
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
    answers?: WSUserInputAnswer[],
  ) {
    const id = userInputId.trim()
    if (!id) return
    forEachToolBlock((block) => {
      if (block.userInput?.user_input_id === id) {
        block.userInput = {
          ...block.userInput,
          status,
          answers: status === 'submitted'
            ? projectSubmittedAnswers(block.userInput, answers)
            : undefined,
          can_respond: false,
        }
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
