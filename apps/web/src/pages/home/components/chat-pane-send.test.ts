import { describe, expect, it } from 'vitest'
import {
  captureChatPaneSendContext,
  composerHasNoModel,
  matchesChatPaneSendContext,
  pinnedSubagentModelId,
  shouldRefreshACPComposerConfig,
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
