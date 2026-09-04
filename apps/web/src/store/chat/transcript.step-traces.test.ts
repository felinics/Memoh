import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import type { UITurn } from '@/composables/api/useChat.types'
import { createBackgroundTaskTracker } from './background-tasks'
import { createTranscriptController } from './transcript'
import type { ChatAssistantTurn, ChatUserTurn, ToolCallBlock } from './types'

vi.mock('@/store/user', () => ({
  useUserStore: () => ({ userInfo: { id: 'user-1' } }),
}))

function makeTranscript() {
  const backgroundTasks = createBackgroundTaskTracker()
  return createTranscriptController({
    currentBotId: ref<string | null>('bot-1'),
    sessionId: ref<string | null>('session-1'),
    rememberBackgroundTask: backgroundTasks.rememberBackgroundTask,
    applyPendingBackgroundEventsToTool: backgroundTasks.applyPendingBackgroundEventsToTool,
    bumpFsChangedAtIfFsMutation: vi.fn(),
    fetchMessages: vi.fn().mockResolvedValue([]),
    locateMessage: vi.fn().mockResolvedValue({ items: [], target_id: '', target_external_message_id: '' }),
  })
}

describe('transcript trace facts', () => {
  it('keeps step traces, execution timing and context injection on normalized turns', () => {
    const transcript = makeTranscript()
    const turns: UITurn[] = [
      { id: 'user-1', turn_id: 'turn-1', role: 'user', text: 'hello', timestamp: '2026-09-03T00:00:00.000Z' },
      {
        id: 'assistant-1',
        turn_id: 'turn-1',
        role: 'assistant',
        timestamp: '2026-09-03T00:00:01.000Z',
        messages: [
          { id: 0, type: 'tool', name: 'exec', input: {}, tool_call_id: 'call-1', running: false, output: 'ok', execution_timing: { started_at_ms: 1_500, ended_at_ms: 1_900 } },
          { id: 1, type: 'text', content: 'done' },
        ],
        step_traces: [
          { first_message_id: 0, step_index: 0, started_at_ms: 1_000, ended_at_ms: 1_400, finish_reason: 'tool-calls' },
          { first_message_id: 1, step_index: 1, started_at_ms: 2_000, first_token_at_ms: 2_100, ended_at_ms: 2_500, finish_reason: 'stop', usage: { output_tokens: 4 } },
        ],
      },
      { id: 'user-2', turn_id: 'turn-1', role: 'user', text: '<message>stop</message>', timestamp: '2026-09-03T00:00:02.000Z', context_injection: { kind: 'steering' } },
    ]
    transcript.replaceMessages(turns, 'session-1')

    const [user, assistant, injected] = transcript.messages as [ChatUserTurn, ChatAssistantTurn, ChatUserTurn]
    expect(user.contextInjection).toBeUndefined()
    expect(injected.contextInjection).toEqual({ kind: 'steering' })
    const assistantTurn = turns[1]!
    expect(assistant.stepTraces).toEqual(assistantTurn.role === 'assistant' ? assistantTurn.step_traces : [])
    const tool = assistant.messages[0] as ToolCallBlock
    expect(tool.execution_timing).toEqual({ started_at_ms: 1_500, ended_at_ms: 1_900 })
  })
})
