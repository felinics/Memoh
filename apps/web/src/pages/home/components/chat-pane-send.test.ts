// jsdom's localStorage is unavailable under this runner (node shadows it),
// so the pair-draft tests run against a Map-backed stub, matching the
// markdown test's precedent.
const pairDraftStorage = new Map<string, string>()
Object.defineProperty(globalThis, 'localStorage', {
  value: {
    getItem: (k: string) => pairDraftStorage.get(k) ?? null,
    setItem: (k: string, v: string) => void pairDraftStorage.set(k, String(v)),
    removeItem: (k: string) => void pairDraftStorage.delete(k),
    clear: () => pairDraftStorage.clear(),
  },
})
import { describe, expect, it } from 'vitest'
import {
  captureChatPaneSendContext,
  carriedPairForSource,
  clearComposerPairDraft,
  composerHasNoModel,
  composerPairDraftKey,
  matchesChatPaneSendContext,
  pinnedSubagentModelId,
  readComposerPairDraft,
  shouldRefreshACPComposerConfig,
  welcomeSendConsumedDraft,
  writeComposerPairDraft,
} from './chat-pane-send'

describe('chat pane send context', () => {
  it('keeps the original target after an ephemeral pane is repointed', () => {
    const target = {
      botId: 'bot-1',
      sessionId: 'session-a',
      viewId: 'chat:1',
    }
    const context = captureChatPaneSendContext(target, 'bot-1:chat:1')

    target.sessionId = 'session-b'

    expect(context.target).toEqual({
      botId: 'bot-1',
      sessionId: 'session-a',
      viewId: 'chat:1',
    })
    expect(Object.isFrozen(context.target)).toBe(true)
  })

  it('restores a failed attachment conversion only into the original composer', () => {
    const context = captureChatPaneSendContext({
      botId: 'bot-1',
      sessionId: 'session-a',
      viewId: 'chat:1',
    }, 'bot-1:chat:1')

    expect(matchesChatPaneSendContext(context, {
      botId: 'bot-1',
      sessionId: 'session-a',
      viewId: 'chat:1',
    }, 'bot-1:chat:1')).toBe(true)
    expect(matchesChatPaneSendContext(context, {
      botId: 'bot-1',
      sessionId: 'session-b',
      viewId: 'chat:1',
    }, 'bot-1:chat:1')).toBe(false)
    expect(matchesChatPaneSendContext(context, {
      botId: 'bot-1',
      sessionId: 'session-a',
      viewId: 'chat:2',
    }, 'bot-1:chat:2')).toBe(false)
  })

  it.each([
    'acp.model_unavailable',
    'acp.reasoning_effort_unavailable',
  ])('refreshes ACP config after stale selection error %s', (errorCode) => {
    expect(shouldRefreshACPComposerConfig({
      ok: false,
      stage: 'startup',
      errorCode,
    }, true)).toBe(true)
  })

  it('does not refresh ACP config for unrelated or inactive failures', () => {
    expect(shouldRefreshACPComposerConfig({
      ok: false,
      stage: 'startup',
      errorCode: 'acp.config_update_failed',
    }, true)).toBe(false)
    expect(shouldRefreshACPComposerConfig({
      ok: false,
      stage: 'startup',
      errorCode: 'acp.model_unavailable',
    }, false)).toBe(false)
    expect(shouldRefreshACPComposerConfig({ ok: true }, true)).toBe(false)
  })
})

describe('composer model gate', () => {
  it('blocks a native composer with no chat model', () => {
    expect(composerHasNoModel(false, '')).toBe(true)
    expect(composerHasNoModel(false, '   ')).toBe(true)
  })

  it('allows a native composer once a model is selected', () => {
    expect(composerHasNoModel(false, 'model-1')).toBe(false)
  })

})

describe('pinned subagent model', () => {
  const models = ['model-default', 'model-pinned']

  it('opens a subagent session on the model it was spawned with', () => {
    expect(pinnedSubagentModelId('subagent', { model_uuid: 'model-pinned' }, models))
      .toBe('model-pinned')
  })

  it('leaves non-subagent sessions on the bot default', () => {
    expect(pinnedSubagentModelId('chat', { model_uuid: 'model-pinned' }, models)).toBe('')
    expect(pinnedSubagentModelId(undefined, { model_uuid: 'model-pinned' }, models)).toBe('')
  })

  it('falls back to the bot default when the pinned model is unusable', () => {
    // Sessions spawned before the model was recorded, and models deleted since.
    expect(pinnedSubagentModelId('subagent', {}, models)).toBe('')
    expect(pinnedSubagentModelId('subagent', { model_uuid: '  ' }, models)).toBe('')
    expect(pinnedSubagentModelId('subagent', { model_uuid: 'model-gone' }, models)).toBe('')
  })
})

describe('carried pair gate (issue #879)', () => {
  it.each([
    { source: 'user', want: { modelId: 'm-1', reasoningEffort: 'high' } },
    { source: 'session', want: { modelId: 'm-1', reasoningEffort: 'high' } },
    { source: 'default', want: { modelId: '', reasoningEffort: '' } },
    { source: 'unset', want: { modelId: '', reasoningEffort: '' } },
  ])('source $source carries $want', ({ source, want }) => {
    expect(carriedPairForSource(source, ' m-1 ', ' high ')).toEqual(want)
  })
})

describe('composer pair draft', () => {
  it('round-trips and clears per bot', () => {
    writeComposerPairDraft('bot-1', { model_id: 'm-1', reasoning_effort: 'high' })
    writeComposerPairDraft('bot-2', { model_id: 'm-2', reasoning_effort: 'low' })
    expect(readComposerPairDraft('bot-1')).toEqual({ model_id: 'm-1', reasoning_effort: 'high' })
    clearComposerPairDraft('bot-1')
    expect(readComposerPairDraft('bot-1')).toBeNull()
    expect(readComposerPairDraft('bot-2')).toEqual({ model_id: 'm-2', reasoning_effort: 'low' })
  })

  it('treats a model-less payload as no draft', () => {
    localStorage.setItem(composerPairDraftKey('bot-1'), JSON.stringify({ reasoning_effort: 'high' }))
    expect(readComposerPairDraft('bot-1')).toBeNull()
  })
})

describe('welcome send consumes the draft', () => {
  it('clears only on a successful welcome send', () => {
    expect(welcomeSendConsumedDraft({ sessionId: '' }, { ok: true })).toBe(true)
    expect(welcomeSendConsumedDraft({}, { ok: true })).toBe(true)
    // An existing-session send never touches the draft...
    expect(welcomeSendConsumedDraft({ sessionId: 's-1' }, { ok: true })).toBe(false)
    // ...and neither does a failed welcome send (the pick was never persisted).
    expect(welcomeSendConsumedDraft({ sessionId: '' }, { ok: false })).toBe(false)
  })
})
