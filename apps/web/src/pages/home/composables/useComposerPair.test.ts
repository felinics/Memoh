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
      const row = session({
        ...server.get(id),
        ...(runtime.value === 'claude-code' || runtime.value === 'codex'
          ? { preferred_external_model_id: modelId }
          : { preferred_chat_model_id: modelId }),
        preferred_reasoning_effort: effort,
        model_preference_revision: `r-${modelId}`,
      })
      server.set(row)
      return row
    }),
  }
  const usesExternal = computed(() => runtime.value !== 'model')
  const visible = ref(true)
  const onPreferenceConflict = vi.fn()
  const deps: ComposerPairDeps = {
    view,
    target,
    visible,
    botId: ref('bot'),
    activeSession,
    botSettings: ref({ chat_model_id: 'native-default', reasoning_effort: 'medium' }),
    pinnedSubagentModelId: ref(''),
    usesExternalAgentComposer: usesExternal,
    usesDirectRuntime: computed(() => runtime.value === 'claude-code' || runtime.value === 'codex'),
    usesACPRuntime: computed(() => runtime.value === 'acp_agent'),
    runtimeIdentity: computed(() => JSON.stringify([runtime.value, usesExternal.value, botAgentId.value])),
    directCatalog: ref({
      configuredModelId: 'cc-default', defaultModelId: 'cc-default', configuredReasoningEffort: 'medium',
      defaultReasoningEffort: 'medium', reasoningEfforts: [{ id: 'medium' }, { id: 'high' }],
    }),
    draftPromotionPending: () => false,
    onPreferenceConflict,
    api,
  }
  const pair = useComposerPair(deps)
  return { pair, view, target, runtime, botAgentId, activeSession, server, api, visible, onPreferenceConflict, deps }
}

const flush = async () => { for (let n = 0; n < 8; n++) await Promise.resolve(); await nextTick() }

describe('direct composer default selection', () => {
  it.each([
    { configured: 'configured-B', advertised: 'runtime-C', expected: 'configured-B' },
    { configured: '', advertised: 'runtime-B', expected: 'runtime-B' },
    { configured: '', advertised: 'default', expected: 'default' },
  ])('persists, sends and reloads Default as $expected', async ({ configured, advertised, expected }) => {
    const env = setup({ sessionId: 'sess-1', direct: true })
    await flush()
    env.server.set(session({ id: 'sess-1', preferred_external_model_id: 'A', preferred_reasoning_effort: 'high' }))
    env.pair.setPair('A', 'high', 'session')
    // Like useAgentModelCatalog, effort options react synchronously to the
    // chosen model. The click must read B's default after replacing A.
    env.deps.directCatalog = computed(() => ({
      configuredModelId: configured,
      defaultModelId: advertised,
      configuredReasoningEffort: 'high',
      defaultReasoningEffort: env.view.value.pairModelId.value === expected ? 'low' : 'high',
      reasoningEfforts: [{ id: 'low' }, { id: 'high' }],
    }))

    expect(env.pair.selectModel('')).toBe(true)
    expect(env.pair.snapshot()).toEqual({ modelId: expected, effort: 'low', source: 'user' })
    env.pair.persist()
    await flush()
    expect(env.server.get('sess-1').preferred_external_model_id).toBe(expected)
    expect(env.server.get('sess-1').preferred_reasoning_effort).toBe('low')
    const send = env.pair.captureSend()
    expect(send.pair).toEqual({ modelId: expected, reasoningEffort: 'low' })
    send.begin()
    send.finish(true)
    send.releaseReads()

    // A new view has no optimistic state to fall back on; it must recover
    // the default selection from the persisted external preference.
    env.target.value = { ...env.target.value, viewId: 'reopened' }
    await flush()
    expect(env.pair.snapshot()).toEqual({ modelId: expected, effort: 'low', source: 'session' })
  })

  it('carries the selected default on the next send when the picker save fails', async () => {
    const env = setup({ sessionId: 'sess-1', direct: true })
    await flush()
    env.server.set(session({ id: 'sess-1', preferred_external_model_id: 'A', preferred_reasoning_effort: 'high' }))
    env.pair.setPair('A', 'high', 'session')
    env.api.updatePreference.mockRejectedValueOnce(new TypeError('network'))

    expect(env.pair.selectModel('')).toBe(true)
    env.pair.persist()
    await flush()
    env.visible.value = false
    await flush()
    env.visible.value = true
    await flush()
    expect(env.server.get('sess-1').preferred_external_model_id).toBe('A')
    expect(env.pair.snapshot()).toEqual({ modelId: 'cc-default', effort: 'medium', source: 'user' })
    const send = env.pair.captureSend()
    expect(send.pair).toEqual({ modelId: 'cc-default', reasoningEffort: 'medium' })
    send.begin()
    send.finish(false)
    send.releaseReads()
  })

  it('keeps the current pair when the runtime advertises no default model', async () => {
    const env = setup({ sessionId: 'sess-1', direct: true })
    await flush()
    env.pair.setPair('A', 'high', 'session')
    env.deps.directCatalog.value.configuredModelId = ''
    env.deps.directCatalog.value.defaultModelId = ''

    expect(env.pair.selectModel('')).toBe(false)
    expect(env.pair.snapshot()).toEqual({ modelId: 'A', effort: 'high', source: 'session' })
    expect(env.pair.selectModel('B')).toBe(true)
    expect(env.pair.carried.value.modelId).toBe('B')
  })
})

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


describe('composer send preparation', () => {
  it('keeps a welcome send snapshot when its seed arrives during attachment conversion', async () => {
    let resolveSeed!: (seed: { model_id: string, reasoning_effort: string }) => void
    const env = setup({ seed: () => new Promise(r => { resolveSeed = r }) })
    await flush()
    const send = env.pair.captureSend()
    const displayed = env.view.value.pairModelId.value
    resolveSeed({ model_id: 'previous-model', reasoning_effort: 'high' })
    await flush()
    expect(env.view.value.pairModelId.value).toBe(displayed)
    expect(send.pair.modelId).toBe(displayed)
    send.begin()
    send.finish(true)
    send.releaseReads()
  })

  it('saves a choice made during preparation after the captured send finishes', async () => {
    const env = setup({ sessionId: 'sess-1' })
    await flush()
    env.pair.setPair('A', 'low', 'session')
    const send = env.pair.captureSend()
    env.pair.setPair('B', 'high', 'user')
    env.pair.persist()
    await flush()
    send.begin()
    env.server.set(session({ id: 'sess-1', preferred_chat_model_id: send.pair.modelId, preferred_reasoning_effort: send.pair.reasoningEffort }))
    await flush()
    expect(env.api.updatePreference).not.toHaveBeenCalled()
    send.finish(true)
    send.releaseReads()
    await flush()
    expect(send.pair).toEqual({ modelId: 'A', reasoningEffort: 'low' })
    expect(env.server.get('sess-1').preferred_chat_model_id).toBe('B')
    expect(env.view.value.pairModelId.value).toBe('B')
  })

  it('releases a newer choice when preparation fails or resolves to a command', async () => {
    const env = setup({ sessionId: 'sess-1' })
    await flush()
    const send = env.pair.captureSend()
    env.pair.setPair('B', 'high', 'user')
    env.pair.persist()
    await flush()
    send.releaseReads()
    await flush()
    expect(env.server.get('sess-1').preferred_chat_model_id).toBe('B')
  })
})

describe('composer pair persist failures', () => {
  // A failed send must not wedge the view: refresh stays enabled once no
  // write/send is in flight (regression: a plain startup failure used to
  // leave the dirty flag set forever, silently freezing pair revalidation).
  it('a failed send does not block later server revalidation', async () => {
    const env = setup({ sessionId: 'sess-1' })
    await flush()
    env.pair.setPair('A', 'low', 'session')
    const send = env.pair.captureSend()
    send.begin()
    send.finish(false) // startup failure: the pair never reached the server
    send.releaseReads()
    env.server.set(session({ id: 'sess-1', preferred_chat_model_id: 'C', preferred_reasoning_effort: 'max' }))
    env.visible.value = false
    await flush()
    env.visible.value = true
    await flush()
    expect(env.view.value.pairModelId.value).toBe('C')
    expect(env.view.value.pairSource.value).toBe('session')
  })

  // A transiently failed pick is the opposite: the optimistic choice stays
  // and reads hold off until a confirmed send persists it.
  it('keeps an unsaved pick across reloads until a confirmed send persists it', async () => {
    const env = setup({ sessionId: 'sess-1' })
    await flush()
    env.pair.setPair('B', 'high', 'user')
    env.api.updatePreference.mockRejectedValueOnce(new TypeError('network'))
    env.pair.persist()
    await flush()
    expect(env.onPreferenceConflict).not.toHaveBeenCalled()
    // The "other writer" moved the server on; a reload must not clobber B.
    env.server.set(session({ id: 'sess-1', preferred_chat_model_id: 'A', preferred_reasoning_effort: 'low' }))
    env.visible.value = false
    await flush()
    env.visible.value = true
    await flush()
    expect(env.view.value.pairModelId.value).toBe('B')
    // A confirmed send carries and persists the pick; reads resume.
    const send = env.pair.captureSend()
    send.begin()
    send.finish(true)
    send.releaseReads()
    env.visible.value = false
    await flush()
    env.visible.value = true
    await flush()
    expect(env.view.value.pairModelId.value).toBe('A')
  })

  // 409: the pick lost the revision race. Drop the unsaved protection, adopt
  // the server's winning pair, and notify the pane.
  it('a conflicted pick reverts to the server pair and notifies the pane', async () => {
    const env = setup({ sessionId: 'sess-1' })
    await flush()
    env.pair.setPair('B', 'high', 'user')
    env.server.set(session({ id: 'sess-1', preferred_chat_model_id: 'winner', preferred_reasoning_effort: 'low', model_preference_revision: 'r2' }))
    env.api.updatePreference.mockRejectedValueOnce({ code: 'session.model_preference_conflict', status: 409 })
    env.pair.persist()
    await flush()
    expect(env.onPreferenceConflict).toHaveBeenCalledOnce()
    expect(env.view.value.pairModelId.value).toBe('winner')
    expect(env.view.value.pairSource.value).toBe('session')
  })

  // The PATCH response carries both columns on a direct session; only the
  // external one belongs in a direct composer, and vice versa.
  it('applies the PATCH response from the column of the current runtime', async () => {
    const env = setup({ sessionId: 'sess-1', direct: true })
    await flush()
    env.pair.setPair('opus', 'high', 'user')
    env.api.updatePreference.mockImplementationOnce(async (_botId: string, id: string) => {
      const row = session({
        id,
        runtime_type: 'claude-code',
        preferred_external_model_id: 'opus',
        preferred_chat_model_id: 'stale-native-uuid',
        preferred_reasoning_effort: 'high',
        model_preference_revision: 'r2',
      } as Partial<SessionSummary>)
      env.server.set(row)
      return row
    })
    env.pair.persist()
    await flush()
    expect(env.view.value.pairModelId.value).toBe('opus')
    expect(env.view.value.pairSource.value).toBe('session')
  })
})
