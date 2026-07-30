import { describe, expect, it } from 'vitest'
import { filterCompactionModels } from './compaction-models'

describe('filterCompactionModels', () => {
  it('offers only models the resolver can accept', () => {
    const models = [
      { id: 'codex-model', model_id: 'gpt-5-codex', provider_id: 'codex-provider', enable: true, config: { context_window: 200000 } },
      { id: 'chat-model', model_id: 'claude-sonnet-4-5', provider_id: 'chat-provider', enable: true, config: { context_window: 200000 } },
      { id: 'disabled-model', model_id: 'claude-off', provider_id: 'chat-provider', enable: false, config: { context_window: 200000 } },
      { id: 'windowless-model', model_id: 'claude-nowin', provider_id: 'chat-provider', enable: true, config: {} },
      { id: 'speech-model', model_id: 'tts-1', provider_id: 'speech-provider', enable: true, config: { context_window: 16000 } },
      { id: 'off-provider-model', model_id: 'gpt-4o', provider_id: 'off-provider', enable: true, config: { context_window: 32000 } },
      { id: 'orphan-model', model_id: 'gpt-4o-mini', provider_id: 'missing-provider', enable: true, config: { context_window: 8192 } },
      { id: 'unknown-client-model', model_id: 'mystery-chat', provider_id: 'unknown-provider', enable: true, config: { context_window: 64000 } },
      { id: 'image-model', model_id: 'flux-schnell', provider_id: 'chat-provider', enable: true, config: { context_window: 32000 } },
      { id: 'namespaced-image-model', model_id: 'accounts/fireworks/models/flux-1-dev', provider_id: 'chat-provider', enable: true, config: { context_window: 32000 } },
      {
        id: 'image-name-tool-caller',
        model_id: 'wan2.7-omni',
        provider_id: 'chat-provider',
        enable: true,
        config: { context_window: 128000, compatibilities: ['tool-call'] },
      },
    ]
    const providers = [
      { id: 'codex-provider', client_type: 'openai-codex', enable: true },
      { id: 'chat-provider', client_type: 'openai-responses', enable: true },
      { id: 'speech-provider', client_type: 'edge-speech', enable: true },
      { id: 'off-provider', client_type: 'openai-completions', enable: false },
      { id: 'unknown-provider', client_type: 'mystery-client', enable: true },
    ]

    expect(filterCompactionModels(models, providers).map(model => model.id)).toEqual([
      'chat-model',
      'image-name-tool-caller',
    ])
  })
})
