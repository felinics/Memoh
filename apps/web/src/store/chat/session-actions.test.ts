import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

const { deleteSession, getSession } = vi.hoisted(() => ({ deleteSession: vi.fn(), getSession: vi.fn() }))
vi.mock('@/composables/api/useChat', () => ({ deleteSession }))
vi.mock('@memohai/sdk', () => ({ getBotsByBotIdSessionsBySessionId: getSession }))
vi.mock('@felinic/ui', () => ({ toast: { error: vi.fn() } }))
vi.mock('@/utils/api-error', () => ({ resolveApiErrorMessage: vi.fn() }))
import { createSessionActions } from './session-actions'

function harness() {
  let generation = 0
  const deps: Parameters<typeof createSessionActions>[0] = {
    currentBotId: ref('bot-1'), sessionId: ref('active'), draftIntent: ref(false),
    explicitSessionSelection: ref(true), focusedViewId: ref('view-1'),
    userScopeGeneration: () => generation,
    normalizeTarget: () => ({ botId: 'bot-1', sessionId: 'active', viewId: 'view-1' }),
    isFocusedTarget: () => true,
    chatView: () => { throw new Error('Batch deletion must not create chat views') },
    readOnlyFor: () => false, canForkFor: () => true, isStreaming: () => false,
    abort: vi.fn(), stopSessionRuntime: vi.fn(), clearRuntimeStatus: vi.fn(),
    removeSessionView: vi.fn(), pruneViews: vi.fn(), clearHistoryView: vi.fn(),
    markSessionDeleted: vi.fn(), removeSessionFromList: vi.fn(),
    fallbackSessionAfterDelete: () => null, switchActiveSession: vi.fn(),
    patchSessionInList: vi.fn(), upsertSession: vi.fn(), rememberSession: vi.fn(),
    refreshSessionsList: vi.fn(async () => {}), fetchSessionWindow: async () => [],
    replaceSessionHistory: vi.fn(), rescopeCommandToComposer: () => '', forkFailedMessage: () => '',
  }
  return { deps, actions: createSessionActions(deps), logout: () => { generation++ } }
}

describe('batch session deletion', () => {
  beforeEach(() => { vi.resetAllMocks() })

  it('continues after failures, reconciles tombstones, and deletes the active chat last', async () => {
    const { deps, actions } = harness()
    deleteSession.mockImplementation(async (_bot: string, id: string) => {
      if (id === 'denied' || id === 'tombstoned' || id === 'unknown') throw new Error('request failed')
    })
    getSession.mockImplementation(async ({ path }: { path: { session_id: string } }) => {
      if (path.session_id === 'unknown') throw new Error('offline')
      return path.session_id === 'tombstoned'
        ? { response: { status: 404 } }
        : { response: { status: 200 }, data: { id: 'denied', bot_id: 'bot-1' } }
    })

    expect(await actions.removeSessions(['active', 'denied', 'ok', 'tombstoned', 'unknown', 'ok'])).toEqual({
      total: 5, failed: 3, failedIds: ['denied', 'unknown'],
    })
    expect(deleteSession.mock.calls).toEqual(['denied', 'ok', 'tombstoned', 'unknown', 'active'].map(id => ['bot-1', id]))
    expect(deps.removeSessionView).toHaveBeenCalledWith('bot-1', 'ok')
    expect(deps.removeSessionView).toHaveBeenCalledWith('bot-1', 'active')
    expect(deps.removeSessionView).toHaveBeenCalledWith('bot-1', 'tombstoned')
    expect(deps.refreshSessionsList).toHaveBeenCalledWith('bot-1')
    expect(deps.upsertSession).toHaveBeenCalledWith({ id: 'denied', bot_id: 'bot-1' })
    expect(deps.sessionId.value).toBeNull()
  })

  it('keeps the confirmed bot even if focus switches during a request', async () => {
    const { deps, actions } = harness()
    deleteSession.mockImplementation(async () => { deps.currentBotId.value = 'bot-2' })
    await actions.removeSessions(['a', 'b'])
    expect(deleteSession.mock.calls).toEqual([['bot-1', 'a'], ['bot-1', 'b']])
    expect(deps.removeSessionFromList).not.toHaveBeenCalled()
    expect(deps.sessionId.value).toBe('active')
  })

  it('stops queued requests and cache changes when the account scope changes', async () => {
    const { deps, actions, logout } = harness()
    deleteSession.mockImplementation(async () => { logout() })
    expect(await actions.removeSessions(['a', 'b'])).toBeNull()
    expect(deleteSession).toHaveBeenCalledTimes(1)
    expect(deps.removeSessionView).not.toHaveBeenCalled()
    expect(deps.refreshSessionsList).not.toHaveBeenCalled()
  })
})
