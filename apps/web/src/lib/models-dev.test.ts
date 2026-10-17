import { describe, expect, it } from 'vitest'
import { catalogModelsFor, modelConfigFromCatalog, reasoningFromCatalog, type ModelsDevCatalog } from './models-dev'
import fixture from './models-dev.fixture.json'

// Rows copied from models.dev; the expectations are the declarations the former
// provider templates made by hand for the same models.
const catalog = fixture as unknown as ModelsDevCatalog

function reasoningOf(source: string, modelID: string, protocol: string) {
  return reasoningFromCatalog(catalog[source]!.models![modelID]!, protocol)
}

describe('reasoningFromCatalog', () => {
  it('reads Anthropic generations onto the budget and adaptive shapes', () => {
    expect(reasoningOf('anthropic', 'claude-haiku-4-5', 'anthropic-messages'))
      .toEqual({ budget: { min: 1024 }, dialect: 'budget', off: 'accepted' })
    expect(reasoningOf('anthropic', 'claude-opus-4-5-20251101', 'anthropic-messages'))
      .toEqual({ efforts: ['low', 'medium', 'high'], budget: { min: 1024 }, dialect: 'budget', off: 'accepted' })
    expect(reasoningOf('anthropic', 'claude-opus-4-6', 'anthropic-messages'))
      .toEqual({ efforts: ['low', 'medium', 'high', 'max'], budget: { min: 1024 }, dialect: 'adaptive', off: 'accepted' })
    expect(reasoningOf('anthropic', 'claude-opus-4-7', 'anthropic-messages'))
      .toEqual({ efforts: ['low', 'medium', 'high', 'xhigh', 'max'], dialect: 'adaptive', off: 'accepted' })
    expect(reasoningOf('anthropic', 'claude-sonnet-5', 'anthropic-messages'))
      .toEqual({ efforts: ['low', 'medium', 'high', 'xhigh', 'max'], dialect: 'adaptive', toggle: true, off: 'accepted' })
    // Claude 5 without a switch may reject an explicit off; nothing says, so it
    // is left undeclared and no off control is offered.
    expect(reasoningOf('anthropic', 'claude-fable-5', 'anthropic-messages'))
      .toEqual({ efforts: ['low', 'medium', 'high', 'xhigh', 'max'], dialect: 'adaptive' })
  })

  it('reads Gemini generations onto the budget and tier shapes', () => {
    expect(reasoningOf('google', 'gemini-2.5-pro', 'google-generative-ai'))
      .toEqual({ budget: { min: 128, max: 32768 }, dialect: 'budget', off: 'rejected' })
    expect(reasoningOf('google', 'gemini-2.5-flash', 'google-generative-ai'))
      .toEqual({ budget: { min: 0, max: 24576 }, dialect: 'budget', toggle: true, off: 'accepted' })
    expect(reasoningOf('google', 'gemini-3-flash-preview', 'google-generative-ai'))
      .toEqual({ efforts: ['minimal', 'low', 'medium', 'high'], dialect: 'tier', off: 'rejected' })
  })

  it('reads OpenAI none as a switch, not an effort', () => {
    expect(reasoningOf('openai', 'gpt-5', 'openai-responses'))
      .toEqual({ efforts: ['minimal', 'low', 'medium', 'high'], dialect: 'tier' })
    expect(reasoningOf('openai', 'gpt-5.1', 'openai-responses'))
      .toEqual({ efforts: ['low', 'medium', 'high'], dialect: 'tier', toggle: true })
  })

  it('requires reasoning_content on DeepSeek and Kimi replays', () => {
    expect(reasoningOf('deepseek', 'deepseek-v4-pro', 'openai-completions')).toEqual({
      efforts: ['low', 'high', 'max'],
      dialect: 'tier',
      toggle: true,
      off: 'accepted',
      replay: { field: 'reasoning_content', required: true },
    })
    expect(reasoningOf('moonshotai-cn', 'kimi-k2.6', 'openai-completions')?.replay)
      .toEqual({ field: 'reasoning_content', required: true })
  })

  it('reads an empty option list as always reasoning', () => {
    expect(reasoningOf('minimax', 'MiniMax-M2.7', 'openai-completions')).toEqual({})
  })

  // The server sends Gemini thinking as a budget or a level and has no wire
  // for a bare switch, so declaring one would offer a control that never
  // reaches the model.
  it('leaves a Google model that only offers a switch without a control', () => {
    expect(reasoningFromCatalog({ id: 'gemma-4-31b-it', reasoning: true, reasoning_options: [{ type: 'toggle' }] }, 'google-generative-ai'))
      .toEqual({ off: 'rejected' })
  })

  it('drops null and empty effort values the catalog lists', () => {
    expect(reasoningFromCatalog({ id: 'sarvam-m', reasoning: true, reasoning_options: [{ type: 'effort', values: [null, 'low', '', 'high'] }] }, 'openai-completions'))
      .toEqual({ efforts: ['low', 'high'], dialect: 'tier' })
  })
})

describe('catalogModelsFor', () => {
  it('builds stored configs from a provider\'s chat models', () => {
    const models = catalogModelsFor(catalog, 'anthropic', 'anthropic-messages')
    const opus = models.find(model => model.model_id === 'claude-opus-4-7')!
    expect(opus.type).toBe('chat')
    expect(opus.config.schema_version).toBe(2)
    expect(opus.config.tool_call).toBe(true)
    expect(opus.config.modalities?.input).toContain('image')
    expect(opus.config.context_window).toBeGreaterThan(0)
    expect(models.map(model => model.name)).toEqual([...models.map(model => model.name)].sort((a, b) => a.localeCompare(b)))
  })

  it('reads each OpenCode Go model on the protocol it routes to', () => {
    const models = catalogModelsFor(catalog, 'opencode-go', 'opencode-go')
    const grok = models.find(model => model.model_id === 'grok-4.7')!
    expect(grok.config.reasoning?.dialect).toBe('tier')
    const mimo = models.find(model => model.model_id === 'mimo-v2.5')!
    expect(mimo.config.reasoning).toEqual({ replay: { field: 'reasoning_content' } })
  })

  // models.dev marks almost no embedding model with a type; the id says what
  // it is. The catalog has no vector size, so such a model is left to the
  // endpoint import, which probes it when the model is created.
  it('leaves embedding models out by id as well as by type', () => {
    const models = catalogModelsFor({
      openai: {
        id: 'openai',
        models: {
          'gpt-5': { id: 'gpt-5' },
          'text-embedding-3-small': { id: 'text-embedding-3-small' },
          'embeddinggemma-300m': { id: 'embeddinggemma-300m' },
          'typed': { id: 'typed', type: 'embedding' },
        },
      },
    }, 'openai', 'openai-responses')
    expect(models.map(model => model.model_id)).toEqual(['gpt-5'])
  })

  it('returns nothing for an unknown or missing source', () => {
    expect(catalogModelsFor(catalog, 'nobody', 'openai-completions')).toEqual([])
    expect(catalogModelsFor(catalog, undefined, 'openai-completions')).toEqual([])
    expect(catalogModelsFor(undefined, 'anthropic', 'anthropic-messages')).toEqual([])
  })

  it('keeps text as the floor of a model\'s modalities', () => {
    expect(modelConfigFromCatalog({ id: 'm' }, 'openai-completions').modalities).toEqual({ input: ['text'], output: ['text'] })
  })
})
