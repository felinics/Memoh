import { describe, expect, it, vi } from 'vitest'
import { createChatViewRegistry } from './view-registry'
import type { ChatAssistantTurn, ChatUserTurn } from './types'

vi.mock('@/store/user', () => ({
  useUserStore: () => ({ userInfo: { id: 'user-1' } }),
}))

function userTurn(id: string, text = id): ChatUserTurn {
  return {
    id,
    role: 'user',
    text,
    attachments: [],
    timestamp: '2026-01-01T00:00:00.000Z',
    streaming: false,
    isSelf: true,
  }
}

function pendingApprovalTurn(id: string): ChatAssistantTurn {
  return {
    id,
    role: 'assistant',
    messages: [{
      id: 1,
      type: 'tool',
      name: 'exec',
      input: {},
      tool_call_id: `call-${id}`,
      running: false,
      toolCallId: `call-${id}`,
      toolName: 'exec',
      result: null,
      done: true,
      approval: {
        approval_id: `approval-${id}`,
        status: 'pending',
        can_approve: true,
      },
    }],
    timestamp: '2026-01-01T00:00:01.000Z',
    streaming: false,
  }
}

function pendingUserInputTurn(id: string): ChatAssistantTurn {
  return {
    id,
    role: 'assistant',
    messages: [{
      id: 1,
      type: 'tool',
      name: 'ask_user',
      input: {},
      tool_call_id: `call-${id}`,
      running: false,
      toolCallId: `call-${id}`,
      toolName: 'ask_user',
      result: null,
      done: true,
      userInput: {
        user_input_id: `input-${id}`,
        status: 'pending',
        questions: [],
        can_respond: true,
      },
    }],
    timestamp: '2026-01-01T00:00:01.000Z',
    streaming: false,
  }
}

function makeRegistry(options: { cacheLimit?: number, streaming?: Set<string> } = {}) {
  const onEvict = vi.fn()
  const registry = createChatViewRegistry({
    cacheLimit: options.cacheLimit,
    rememberBackgroundTask: task => task,
    applyPendingBackgroundEventsToTool: () => {},
    bumpFsChangedAtIfFsMutation: () => {},
    fetchMessages: vi.fn().mockResolvedValue([]),
      locateMessage: vi.fn().mockResolvedValue({
        items: [],
        target_id: '',
        target_external_message_id: '',
      }),
    isSessionStreaming: (botId, sessionId) => options.streaming?.has(`${botId}:${sessionId}`) === true,
    onEvict,
  })
  return { registry, onEvict }
}

describe('chat view registry', () => {
  it('shares real sessions while isolating different sessions', () => {
    const { registry } = makeRegistry()
    const first = registry.getOrCreate({ botId: 'bot-1', sessionId: 'session-a', viewId: 'chat:1' })
    const duplicate = registry.getOrCreate({ botId: 'bot-1', sessionId: 'session-a', viewId: 'chat:2' })
    const second = registry.getOrCreate({ botId: 'bot-1', sessionId: 'session-b', viewId: 'chat:3' })

    first.transcript.appendToView(userTurn('a'))
    second.transcript.appendToView(userTurn('b'))

    expect(duplicate).toBe(first)
    expect(first.transcript.messages.map(message => message.id)).toEqual(['a'])
    expect(second.transcript.messages.map(message => message.id)).toEqual(['b'])
  })

  it('keeps loading and pagination state independent between Sessions', () => {
    const { registry } = makeRegistry()
    const first = registry.getOrCreate({ botId: 'bot-1', sessionId: 'session-a', viewId: 'chat:1' })
    const second = registry.getOrCreate({ botId: 'bot-1', sessionId: 'session-b', viewId: 'chat:2' })

    first.transcript.loadingMessages.value = true
    first.transcript.loadingOlder.value = true
    first.transcript.hasMoreOlder.value = false
    first.transcript.hasLoadedOlder.value = true

    expect(second.transcript.loadingMessages.value).toBe(false)
    expect(second.transcript.loadingOlder.value).toBe(false)
    expect(second.transcript.hasMoreOlder.value).toBe(true)
    expect(second.transcript.hasLoadedOlder.value).toBe(false)
  })

  it('keeps drafts isolated and promotes only the matching panel view', () => {
    const { registry } = makeRegistry()
    const first = registry.getOrCreate({ botId: 'bot-1', sessionId: null, viewId: 'chat:1' })
    const second = registry.getOrCreate({ botId: 'bot-1', sessionId: null, viewId: 'chat:2' })
    first.transcript.appendToView(userTurn('draft-a'))
    second.transcript.appendToView(userTurn('draft-b'))

    registry.bindPanel('chat:1', { botId: 'bot-1', sessionId: null, viewId: 'chat:1' }, true)
    registry.bindPanel('chat:2', { botId: 'bot-1', sessionId: null, viewId: 'chat:2' }, true)
    const promoted = registry.promoteDraft('bot-1', 'chat:1', 'session-a')

    expect(promoted.transcript.messages.map(message => message.id)).toEqual(['draft-a'])
    expect(registry.getSession('bot-1', 'session-a')).toBe(promoted)
    expect(registry.getDraft('bot-1', 'chat:1')).toBeUndefined()
    expect(registry.getDraft('bot-1', 'chat:2')?.transcript.messages.map(message => message.id)).toEqual(['draft-b'])
  })

  it('shares the selected Computer between panels for the same Session', () => {
    const { registry } = makeRegistry()
    const first = registry.getOrCreate({ botId: 'bot-1', sessionId: 'session-a', viewId: 'chat:1' })
    const second = registry.getOrCreate({ botId: 'bot-1', sessionId: 'session-a', viewId: 'chat:2' })

    first.workspaceTargetId.value = 'computer-b'
    first.workspaceTargetSnapshot.value = {
      target_id: 'computer-b',
      kind: 'remote',
      name: 'Computer B',
    }
    first.workspaceTargetSelectionSource.value = 'user'

    expect(second.workspaceTargetId.value).toBe('computer-b')
    expect(second.workspaceTargetSnapshot.value?.name).toBe('Computer B')
    expect(second.workspaceTargetSelectionSource.value).toBe('user')
  })

  it('carries the draft Computer selection into the promoted Session', () => {
    const { registry } = makeRegistry()
    const draft = registry.getOrCreate({ botId: 'bot-1', sessionId: null, viewId: 'chat:1' })
    draft.workspaceTargetId.value = 'computer-b'
    draft.workspaceTargetSnapshot.value = {
      target_id: 'computer-b',
      kind: 'remote',
      name: 'Computer B',
    }
    draft.workspaceTargetSelectionSource.value = 'user'

    const promoted = registry.promoteDraft('bot-1', 'chat:1', 'session-a')

    expect(promoted.workspaceTargetId.value).toBe('computer-b')
    expect(promoted.workspaceTargetSnapshot.value?.name).toBe('Computer B')
    expect(promoted.workspaceTargetSelectionSource.value).toBe('user')
  })

  it('reference-counts visible panels for a shared session', () => {
    const { registry } = makeRegistry()
    const target = { botId: 'bot-1', sessionId: 'session-a', viewId: 'chat:1' }

    const first = registry.bindPanel('chat:1', target, true)
    const second = registry.bindPanel('chat:2', { ...target, viewId: 'chat:2' }, true)
    const stillVisible = registry.setPanelVisible('chat:1', false)
    const hidden = registry.setPanelVisible('chat:2', false)

    expect(first.activatedSession?.sessionId).toBe('session-a')
    expect(second.activatedSession).toBeNull()
    expect(stillVisible?.deactivatedSession).toBeNull()
    expect(hidden?.deactivatedSession?.sessionId).toBe('session-a')
  })

  it('keeps a shared Session intact when one of its panels closes', () => {
    const { registry } = makeRegistry()
    const target = { botId: 'bot-1', sessionId: 'session-a', viewId: 'chat:left' }
    const shared = registry.bindPanel('chat:left', target, true).view
    registry.bindPanel('chat:right', { ...target, viewId: 'chat:right' }, true)
    shared.transcript.appendToView(userTurn('shared'))

    registry.unbindPanel('chat:left')

    expect(registry.getPanel('chat:left')).toBeUndefined()
    expect(registry.getPanel('chat:right')).toBe(shared)
    expect(shared.transcript.messages.map(message => message.id)).toEqual(['shared'])
    expect(shared.visiblePanelIds).toEqual(new Set(['chat:right']))
  })

  it('evicts an attached hidden Session and recreates it when the panel returns', () => {
    const { registry, onEvict } = makeRegistry({ cacheLimit: 0 })
    const target = { botId: 'bot-1', sessionId: 'session-a', viewId: 'chat:1' }
    const attached = registry.bindPanel('chat:1', target, true).view
    attached.transcript.appendToView(userTurn('kept'))

    registry.setPanelVisible('chat:1', false)
    registry.prune()

    expect(registry.getPanel('chat:1')).toBeUndefined()
    expect(onEvict).toHaveBeenCalledWith(attached)
    const rebound = registry.bindPanel('chat:1', target, true).view
    expect(rebound).not.toBe(attached)
    expect(rebound.transcript.messages).toEqual([])
  })

  it('counts attached hidden Sessions against the shared cache limit', () => {
    const { registry } = makeRegistry({ cacheLimit: 1 })
    registry.bindPanel('chat:1', {
      botId: 'bot-1',
      sessionId: 'session-attached',
      viewId: 'chat:1',
    }, false)
    const detached = registry.getOrCreate({
      botId: 'bot-1',
      sessionId: 'session-detached',
      viewId: 'chat:2',
    })

    registry.prune()

    expect(registry.getPanel('chat:1')).toBeUndefined()
    expect(registry.getSession('bot-1', 'session-attached')).toBeUndefined()
    expect(registry.getSession('bot-1', 'session-detached')).toBe(detached)
  })

  it('evicts least-recent hidden sessions but protects visible, streaming, and pending views', () => {
    const streaming = new Set(['bot-1:streaming'])
    const { registry, onEvict } = makeRegistry({ cacheLimit: 4, streaming })
    registry.bindPanel('chat:visible', { botId: 'bot-1', sessionId: 'visible', viewId: 'chat:visible' }, true)
    registry.getOrCreate({ botId: 'bot-1', sessionId: 'streaming', viewId: 'chat:streaming' })
    const pending = registry.getOrCreate({ botId: 'bot-1', sessionId: 'pending', viewId: 'chat:pending' })
    pending.transcript.appendToView(pendingApprovalTurn('pending'))
    const asking = registry.getOrCreate({ botId: 'bot-1', sessionId: 'asking', viewId: 'chat:asking' })
    asking.transcript.appendToView(pendingUserInputTurn('asking'))
    registry.getOrCreate({ botId: 'bot-1', sessionId: 'oldest', viewId: 'chat:oldest' })
    registry.getOrCreate({ botId: 'bot-1', sessionId: 'newest', viewId: 'chat:newest' })

    registry.prune()

    expect(registry.getSession('bot-1', 'visible')).toBeDefined()
    expect(registry.getSession('bot-1', 'streaming')).toBeDefined()
    expect(registry.getSession('bot-1', 'pending')).toBeDefined()
    expect(registry.getSession('bot-1', 'asking')).toBeDefined()
    expect(registry.getSession('bot-1', 'oldest')).toBeUndefined()
    expect(onEvict).toHaveBeenCalledWith(expect.objectContaining({ sessionId: 'oldest' }))
  })
})
