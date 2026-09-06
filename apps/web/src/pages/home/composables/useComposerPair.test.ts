import { describe, expect, it, vi } from 'vitest'
import { computed, nextTick, ref } from 'vue'
import type { ChatViewEntry, ChatViewTarget } from '@/store/chat-list'
import type { SessionSummary } from '@/composables/api/useChat'
import { createComposerPairSync } from '@/store/chat/composer-pair-sync'
import { useComposerPair, type ComposerPairDeps } from './useComposerPair'

function fakeView(): ChatViewEntry {
  return {
    pairSync: createComposerPairSync(),
    pairModelId: ref(''),
    pairEffort: ref(''),
    pairSource: ref('unset'),
  } as unknown as ChatViewEntry
}

function session(overrides: Partial<SessionSummary>): SessionSummary {
  return { id: 'sess-1', ...overrides } as SessionSummary
}

// Builds a pane-like environment: the view is keyed by the target, the
// runtime identity is what the pane derives from the active session.
function setup(options: { sessionId?: string, direct?: boolean, seed?: () => Promise<{ model_id: string, reasoning_effort: string }> } = {}) {
  const views = new Map<string, ChatViewEntry>()
  const target = ref<ChatViewTarget>({ botId: 'bot', sessionId: options.sessionId ?? '', viewId: 'v1' } as ChatViewTarget)
  const view = computed(() => {
    const key = `${target.value.botId}:${target.value.sessionId}:${target.value.viewId}`
    let entry = views.get(key)
    if (!entry) { entry = fakeView(); views.set(key, entry) }
    return entry
  })
  const runtime = ref(options.direct ? 'claude-code' : 'model')
  const botAgentId = ref(options.direct ? 'agent-cc' : '')
  const activeSession = ref<SessionSummary | null>(options.sessionId
    ? session({ id: options.sessionId, runtime_type: runtime.value, bot_agent_id: botAgentId.value } as Partial<SessionSummary>)
    : null)
  // A tiny server: one row per session, PATCH stores the pair like the real one.
  const rows = new Map<string, SessionSummary>()
  if (options.sessionId) rows.set(options.sessionId, session({ id: options.sessionId }))
  const server = {
    set(row: SessionSummary) { rows.set(row.id, row) },
    get(id: string) { return rows.get(id) ?? session({ id }) },
  }
  const api = {
    fetchSession: vi.fn(async (_botId: string, id: string) => server.get(id)),
    fetchSeed: vi.fn(options.seed ?? (async () => ({ model_id: '', reasoning_effort: '' }))),
    updatePreference: vi.fn(async (_botId: string, id: string, modelId: string, effort: string) => {
      const row = session({ ...server.get(id), preferred_chat_model_id: modelId, preferred_reasoning_effort: effort, model_preference_revision: `r-${modelId}` })
      server.set(row)
      return row
    }),
  }
  const usesExternal = computed(() => runtime.value !== 'model')
  const deps: ComposerPairDeps = {
    view,
    target,
    visible: ref(true),
    botId: ref('bot'),
    activeSession,
    botSettings: ref({ chat_model_id: 'native-default', reasoning_effort: 'medium' }),
    pinnedSubagentModelId: ref(''),
    usesExternalAgentComposer: usesExternal,
    usesDirectRuntime: computed(() => runtime.value === 'claude-code' || runtime.value === 'codex'),
    usesACPRuntime: computed(() => runtime.value === 'acp_agent'),
    runtimeIdentity: computed(() => JSON.stringify([runtime.value, usesExternal.value, botAgentId.value])),
    directCatalog: ref({ configuredModelId: 'cc-default', defaultModelId: 'cc-default', configuredReasoningEffort: 'medium' }),
    draftPromotionPending: () => false,
    api,
  }
  const pair = useComposerPair(deps)
  return { pair, view, target, runtime, botAgentId, activeSession, server, api }
}

const flush = async () => { for (let n = 0; n < 8; n++) await Promise.resolve(); await nextTick() }

describe('useComposerPair runtime namespace', () => {
  it('drops an external pick when an empty session switches back to the native runtime', async () => {
    const env = setup({ sessionId: 'sess-1', direct: true })
    await flush()
    // The user picks opus/high on the Claude Code session.
    env.view.value.pairModelId.value = 'opus'
    env.view.value.pairEffort.value = 'high'
    env.pair.setSource('user')
    expect(env.pair.carried.value).toEqual({ modelId: 'opus', reasoningEffort: 'high' })

    // Switching to Memoh: the server clears the preference, the row refreshes.
    env.server.set(session({ id: 'sess-1', runtime_type: 'model' } as Partial<SessionSummary>))
    env.activeSession.value = env.server.get('sess-1')
    env.runtime.value = 'model'
    env.botAgentId.value = ''
    await flush()

    expect(env.view.value.pairModelId.value).toBe('native-default')
    expect(env.view.value.pairEffort.value).toBe('medium')
    expect(env.view.value.pairSource.value).toBe('default')
    // A default-sourced pair is omitted from the wire: the native resolver
    // never sees the external model ID.
    expect(env.pair.carried.value).toEqual({ modelId: '', reasoningEffort: '' })
  })

  it('invalidates a picker write that was in flight when the runtime changed', async () => {
    const env = setup({ sessionId: 'sess-1', direct: true })
    await flush()
    let resolveRead!: (row: SessionSummary) => void
    env.api.fetchSession.mockImplementationOnce(() => new Promise<SessionSummary>((r) => { resolveRead = r }))
    env.view.value.pairModelId.value = 'opus'
    env.pair.setSource('user')
    env.pair.persist()
    await flush()

    env.runtime.value = 'model'
    env.botAgentId.value = ''
    env.activeSession.value = env.server.get('sess-1')
    await flush()
    resolveRead(session({ id: 'sess-1', model_preference_revision: 'r1' }))
    await flush()

    expect(env.api.updatePreference).not.toHaveBeenCalled()
    expect(env.view.value.pairSource.value).toBe('default')
  })

  it('does not reset when the pane is repointed to another session', async () => {
    const env = setup({ sessionId: 'sess-1' })
    await flush()
    env.view.value.pairModelId.value = 'picked'
    env.view.value.pairEffort.value = 'high'
    env.pair.setSource('user')
    env.pair.persist()
    await flush()
    expect(env.api.updatePreference).toHaveBeenCalledTimes(1)

    env.server.set(session({ id: 'sess-2', preferred_chat_model_id: 'remembered', preferred_reasoning_effort: 'low' }))
    env.activeSession.value = env.server.get('sess-2')
    env.target.value = { ...env.target.value, sessionId: 'sess-2' }
    await flush()
    expect(env.view.value.pairModelId.value).toBe('remembered')
    expect(env.view.value.pairSource.value).toBe('session')

    // Coming back finds the first session's own pick intact.
    env.activeSession.value = env.server.get('sess-1')
    env.target.value = { ...env.target.value, sessionId: 'sess-1' }
    await flush()
    expect(env.view.value.pairModelId.value).toBe('picked')
    expect(env.view.value.pairEffort.value).toBe('high')
    expect(env.view.value.pairSource.value).toBe('session')
  })

  it('drops a welcome seed that resolves after the default external Agent was staged', async () => {
    let resolveSeed!: (seed: { model_id: string, reasoning_effort: string }) => void
    const env = setup({ seed: () => new Promise((r) => { resolveSeed = r }) })
    await flush()
    expect(env.api.fetchSeed).toHaveBeenCalledTimes(1)

    // The bot's default Agent lands on the still-empty draft.
    env.runtime.value = 'claude-code'
    env.botAgentId.value = 'agent-cc'
    await flush()
    expect(env.view.value.pairSource.value).toBe('unset')

    resolveSeed({ model_id: '03982dd9-native-uuid', reasoning_effort: 'medium' })
    await flush()
    expect(env.view.value.pairModelId.value).toBe('')
    expect(env.view.value.pairSource.value).toBe('unset')
  })

  it('reseeds a native draft from bot settings after an external Agent is unstaged', async () => {
    const env = setup()
    await flush()
    expect(env.view.value.pairSource.value).toBe('default')
    env.runtime.value = 'claude-code'
    env.botAgentId.value = 'agent-cc'
    await flush()
    expect(env.view.value.pairSource.value).toBe('unset')
    env.view.value.pairModelId.value = 'opus'
    env.pair.setSource('user')

    env.runtime.value = 'model'
    env.botAgentId.value = ''
    await flush()
    expect(env.view.value.pairModelId.value).toBe('native-default')
    expect(env.view.value.pairSource.value).toBe('default')
  })
})
