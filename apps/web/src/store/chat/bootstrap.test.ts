import { ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import type { SessionSummary } from '@/composables/api/useChat'
import { createChatBootstrap } from './bootstrap'

vi.mock('@/composables/api/useChat', () => ({
  fetchSessions: vi.fn(async () => ({
    items: [
      { id: 'session-focused', title: 'Focused' },
      { id: 'session-split', title: 'Split' },
    ] satisfies Partial<SessionSummary>[],
    next_cursor: null,
    has_more: false,
  })),
}))

describe('createChatBootstrap initialize', () => {
  it('restarts runtime hydration for all visible split sessions of the initialized bot', async () => {
    const currentBotId = ref<string | null>('bot-1')
    const sessionId = ref<string | null>('session-focused')
    const startSessionRuntime = vi.fn()
    const summaries = new Map<string, SessionSummary>([
      ['session-focused', { id: 'session-focused', title: 'Focused' } as SessionSummary],
      ['session-split', { id: 'session-split', title: 'Split' } as SessionSummary],
    ])

    let requestSeq = 0
    const bootstrap = createChatBootstrap({
      currentBotId,
      sessionId,
      draftIntent: ref(false),
      explicitSessionSelection: ref(true),
      userScopeGeneration: () => 1,
      bumpSelectSessionRequest: () => ++requestSeq,
      currentSelectSessionRequest: () => requestSeq,
      prepareForInitialization: vi.fn(),
      resetRefreshCoordinator: vi.fn(),
      resetSessionActivity: vi.fn(),
      stopStreams: vi.fn(),
      stopWebSocket: vi.fn(),
      ensureBot: async () => 'bot-1',
      replaceSessions: items => items,
      sessionsCursor: ref(null),
      hasMoreSessions: ref(false),
      defaultRuntimeIsExternalAgent: async () => false,
      ensureSessionSummary: async (_botId, sid) => summaries.get(sid) ?? null,
      pendingExternalAgentSessionInput: ref(null),
      clearPendingExternalAgentSession: vi.fn(),
      clearHistoryView: vi.fn(),
      markHistoryEmpty: vi.fn(),
      knownSessionSummary: sid => summaries.get(sid),
      startWebSocket: vi.fn(),
      startBotSessionsActivityStream: vi.fn(),
      startSessionRuntime,
      visibleSessionIds: () => ['session-focused', 'session-split'],
      releasePreviousSession: vi.fn(),
      clearFsForBotSwitch: vi.fn(),
      clearRememberedSessions: vi.fn(),
      resetToEmptyComposer: vi.fn(),
      stageDefaultExternalAgentFromSettings: vi.fn(async () => {}),
    })

    await bootstrap.initialize()

    expect(startSessionRuntime.mock.calls).toEqual([
      ['bot-1', 'session-focused'],
      ['bot-1', 'session-split'],
    ])
  })
})
