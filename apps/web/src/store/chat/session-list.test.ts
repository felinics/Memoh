import { describe, expect, it } from 'vitest'
import { ref } from 'vue'
import type { SessionSummary } from '@/composables/api/useChat.types'
import { createSessionList } from './session-list'
import type { ChatAssistantTurn, ChatMessage, ChatUserTurn } from './types'

function session(id: string, overrides: Partial<SessionSummary> = {}): SessionSummary {
  return {
    id,
    bot_id: 'bot-1',
    title: `Session ${id}`,
    type: 'chat',
    created_at: '2026-01-02T00:00:00.000Z',
    updated_at: '2026-01-02T00:00:00.000Z',
    ...overrides,
  }
}

function userTurn(id: string, timestamp: string): ChatUserTurn {
  return {
    id,
    serverId: id,
    role: 'user',
    text: id,
    attachments: [],
    timestamp,
    streaming: false,
    isSelf: true,
  }
}

function assistantTurn(id: string, timestamp: string): ChatAssistantTurn {
  return {
    id,
    serverId: id,
    role: 'assistant',
    messages: [],
    timestamp,
    streaming: false,
  }
}

function makeRegistry(initialMessages: ChatMessage[] = []) {
  const currentBotId = ref<string | null>('bot-1')
  const sessionId = ref<string | null>(null)
  const messages = [...initialMessages]
  return {
    currentBotId,
    sessionId,
    messages,
    registry: createSessionList({ currentBotId, sessionId, messages }),
  }
}

function forkAnchor(summary: SessionSummary | null): string {
  const forkedFrom = summary?.metadata?.forked_from as Record<string, unknown> | undefined
  return String(forkedFrom?.fork_message_id ?? '')
}

describe('session list registry', () => {
  it('invalidates active-session lookup when a missing summary is remembered later', () => {
    const { registry, sessionId } = makeRegistry()
    sessionId.value = 'session-1'
    expect(registry.activeSession.value).toBeNull()

    registry.rememberSession(session('session-1'))

    expect(registry.activeSession.value?.id).toBe('session-1')
  })

  it('preserves a provisional title but accepts authoritative fork metadata', () => {
    const { registry } = makeRegistry()
    registry.replaceSessions([session('session-1', {
      title: 'Local provisional title',
      metadata: { forked_from: { session_id: 'parent', fork_message_id: 'assistant-1' } },
    })])

    registry.replaceSessions([session('session-1', {
      title: '',
      metadata: { forked_from: { session_id: 'parent' } },
    })])

    const known = registry.knownSessionSummary('session-1')
    expect(known?.title).toBe('Local provisional title')
    expect(forkAnchor(known)).toBe('')
  })

  it('updates listed and remembered copies through one title operation', () => {
    const { registry } = makeRegistry()
    const original = session('session-1')
    registry.replaceSessions([original])
    registry.rememberSession(original)

    registry.updateKnownSessionTitle('session-1', 'Renamed')
    expect(registry.sessions.value[0]?.title).toBe('Renamed')

    registry.replaceSessions([])
    expect(registry.knownSessionSummary('session-1')?.title).toBe('Renamed')
  })

  it('classifies touches without exposing lookup containers and ignores older timestamps', () => {
    const { registry } = makeRegistry()
    registry.replaceSessions([session('listed')])
    registry.rememberSession(session('remembered', { type: 'acp_agent' }))

    const listed = registry.touchKnownSession('listed', '2026-01-03T00:00:00.000Z')
    expect(listed).toMatchObject({ source: 'listed', visibleInRecents: true })
    expect(listed.session?.updated_at).toBe('2026-01-03T00:00:00.000Z')

    const remembered = registry.touchKnownSession('remembered', '2026-01-01T00:00:00.000Z')
    expect(remembered).toMatchObject({ source: 'remembered', visibleInRecents: true })
    expect(remembered.session?.updated_at).toBe('2026-01-02T00:00:00.000Z')

    expect(registry.touchKnownSession('missing')).toEqual({
      source: 'unknown',
      session: null,
      visibleInRecents: false,
    })
  })

  it('keeps locally deleted sessions out of racing snapshots until deletion state resets', () => {
    const { registry } = makeRegistry()
    registry.markSessionDeleted('bot-1', 'session-1')
    registry.replaceSessions([session('session-1')])
    registry.upsertSession(session('session-1'))
    expect(registry.hasListedSession('session-1')).toBe(false)

    registry.clearDeletedSessionIds()
    registry.upsertSession(session('session-1'))
    expect(registry.hasListedSession('session-1')).toBe(true)
  })

  it('moves a fork anchor before a replaced tail and restores it on rollback', () => {
    const messages: ChatMessage[] = [
      assistantTurn('assistant-inherited', '2026-01-01T00:00:00.000Z'),
      userTurn('user-replaced', '2026-01-03T00:00:00.000Z'),
      assistantTurn('assistant-replaced', '2026-01-03T00:00:01.000Z'),
    ]
    const { registry } = makeRegistry(messages)
    registry.replaceSessions([session('session-1', {
      metadata: { forked_from: { session_id: 'parent', fork_message_id: 'assistant-replaced' } },
    })])

    const restore = registry.updateForkAnchorForReplacedMessage('session-1', messages[1]!)
    expect(forkAnchor(registry.knownSessionSummary('session-1'))).toBe('assistant-inherited')

    restore?.()
    expect(forkAnchor(registry.knownSessionSummary('session-1'))).toBe('assistant-replaced')
  })

})
