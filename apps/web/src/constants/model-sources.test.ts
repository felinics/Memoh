import { describe, expect, it } from 'vitest'
import {
  findModelSource,
  findModelSourceBySource,
  groupModelSources,
  isMonochromeLogo,
  MODEL_SOURCES,
  modelSourceLogo,
  modelSourceLogoForKey,
  modelSourceNeedsApiKey,
  providerCatalogEntry,
  providerGalleryEntry,
  subscriptionForProvider,
  providerDraftFromSource,
  providerLogo,
  providerSource,
  TEMPLATE_SOURCES,
} from './model-sources'

const CLIENT_TYPES = new Set([
  'openai-completions', 'openai-responses', 'anthropic-messages', 'google-generative-ai',
  'openai-codex', 'github-copilot', 'opencode-go',
])

describe('model sources', () => {
  it('groups the subscriptions first, in their own order, and keeps the rest in order', () => {
    const { subscriptions, others } = groupModelSources(MODEL_SOURCES)
    expect(subscriptions.map(item => item.id)).toEqual(['codex', 'github-copilot', 'opencode-go'])
    expect(others.map(item => item.id)).not.toContain('codex')
    expect(others.some(item => item.subscription)).toBe(false)
    expect(others.length + subscriptions.length).toBe(MODEL_SOURCES.length)
    expect(others.map(item => item.id)).toEqual(MODEL_SOURCES.map(item => item.id).filter(id => !['codex', 'github-copilot', 'opencode-go'].includes(id)))

    const configuredCodex = groupModelSources(MODEL_SOURCES.filter(item => item.id !== 'codex'))
    expect(configuredCodex.subscriptions.map(item => item.id)).toEqual(['github-copilot', 'opencode-go'])
  })

  it('gives each subscription a client type no other subscription uses', () => {
    const types = MODEL_SOURCES.filter(item => item.subscription).map(item => item.client_type)
    expect(new Set(types).size).toBe(types.length)
    expect(subscriptionForProvider('openai-completions', '')).toBeUndefined()
    expect(subscriptionForProvider(undefined, '')).toBeUndefined()
  })

  it('counts a provider as a subscription only on that subscription\'s endpoint', () => {
    expect(subscriptionForProvider('openai-codex', '')?.id).toBe('codex')
    expect(subscriptionForProvider('openai-codex', 'https://chatgpt.com/backend-api/')?.id).toBe('codex')
    expect(subscriptionForProvider('opencode-go', 'https://OpenCode.ai/zen/go/v1')?.id).toBe('opencode-go')
    expect(subscriptionForProvider('opencode-go', 'https://proxy.example.com/v1')).toBeUndefined()
    // Copilot has no fixed base URL, so any provider of its type is it.
    expect(subscriptionForProvider('github-copilot', 'https://anything.example.com')?.id).toBe('github-copilot')
    expect(providerGalleryEntry({ client_type: 'opencode-go', config: { base_url: 'https://proxy.example.com/v1' } })).toBeUndefined()
  })

  it('counts a subscription by its client type only for the gallery', () => {
    const custom = { client_type: 'opencode-go', metadata: {} }
    expect(providerGalleryEntry(custom)?.id).toBe('opencode-go')
    expect(providerGalleryEntry({ client_type: 'openai-codex' })?.id).toBe('codex')
    expect(providerGalleryEntry({ client_type: 'openai-completions' })).toBeUndefined()
    // What a provider imports from stays its own record.
    expect(providerCatalogEntry(custom)).toBeUndefined()
    expect(providerSource(custom)).toBeUndefined()
  })

  it('lists each entry once, sorted by id, and each source once', () => {
    const ids = MODEL_SOURCES.map(item => item.id)
    expect(new Set(ids).size).toBe(ids.length)
    expect(ids).toEqual([...ids].sort((a, b) => a.localeCompare(b)))
    const sources = MODEL_SOURCES.flatMap(item => item.source ? [item.source] : [])
    expect(new Set(sources).size).toBe(sources.length)
  })

  it('gives every entry a name, a supported client type and a usable base URL', () => {
    for (const item of MODEL_SOURCES) {
      expect(item.name.trim(), item.id).not.toBe('')
      expect(CLIENT_TYPES.has(item.client_type), item.id).toBe(true)
      if (item.client_type === 'github-copilot') {
        expect(item.base_url, item.id).toBe('')
        continue
      }
      // Self-hosted entries carry a placeholder the user fills in.
      const url = new URL(item.base_url.replace(/<[^>]+>/g, 'placeholder'))
      expect(['https:', 'http:'], item.id).toContain(url.protocol)
      expect(item.base_url, item.id).not.toMatch(/\$\{|\/$/)
    }
  })

  it('draws every entry with a models.dev logo', () => {
    for (const item of MODEL_SOURCES) {
      expect(modelSourceLogo(item), item.id).toMatch(/^https:\/\/models\.dev\/logos\/[^/]+\.svg$/)
    }
    expect(modelSourceLogo(findModelSource('codex')!)).toBe('https://models.dev/logos/openai.svg')
    expect(modelSourceLogo(findModelSource('github-copilot')!)).toBe('https://models.dev/logos/github-copilot.svg')
    expect(modelSourceLogo(findModelSource('ollama')!)).toBe('https://models.dev/logos/ollama-cloud.svg')
    // models.dev has no New API logo and serves its generic one.
    expect(modelSourceLogo(findModelSource('newapi')!)).toBe('https://models.dev/logos/newapi.svg')
    expect(modelSourceLogoForKey('qwen')).toBe('https://models.dev/logos/alibaba-cn.svg')
    expect(modelSourceLogoForKey('unknown')).toBeUndefined()
  })

  it('maps every former template to an entry', () => {
    for (const [template, id] of Object.entries(TEMPLATE_SOURCES)) {
      expect(findModelSource(id), template).toBeDefined()
    }
  })

  it('finds entries and their logos', () => {
    expect(findModelSource('openai')?.client_type).toBe('openai-responses')
    // Azure is called by deployment name, so it imports from its endpoint.
    expect(findModelSource('azure-openai')?.source).toBeUndefined()
    expect(findModelSourceBySource('azure')).toBeUndefined()
    expect(findModelSource(undefined)).toBeUndefined()
    expect(modelSourceLogo(findModelSource('anthropic')!)).toBe('https://models.dev/logos/anthropic.svg')
    expect(modelSourceLogo(findModelSource('baseten')!)).toBe('https://models.dev/logos/baseten.svg')
  })

  it('needs an API key except for local and OAuth entries', () => {
    expect(modelSourceNeedsApiKey(findModelSource('openai')!)).toBe(true)
    expect(modelSourceNeedsApiKey(findModelSource('ollama')!)).toBe(false)
    expect(modelSourceNeedsApiKey(findModelSource('codex')!)).toBe(false)
    expect(modelSourceNeedsApiKey(findModelSource('github-copilot')!)).toBe(false)
  })

  it('treats only one-color models.dev logos as monochrome', () => {
    expect(isMonochromeLogo('https://models.dev/logos/baseten.svg')).toBe(true)
    expect(isMonochromeLogo('https://models.dev/logos/302ai.svg')).toBe(false)
    expect(isMonochromeLogo('anthropic')).toBe(false)
    expect(isMonochromeLogo('https://example.com/logo.svg')).toBe(false)
    expect(isMonochromeLogo(undefined)).toBe(false)
  })
})

describe('providers and entries', () => {
  it('shows a provider its entry\'s models.dev logo, never a former local icon', () => {
    // Created from the former qwen template, still carrying the old local icon.
    expect(providerLogo({ icon: 'qwen-color', metadata: { registry: { source: 'qwen.yaml' } } }))
      .toBe('https://models.dev/logos/alibaba-cn.svg')
    expect(providerLogo({ icon: 'https://models.dev/logos/deepseek.svg', metadata: { catalog: 'deepseek' } }))
      .toBe('https://models.dev/logos/deepseek.svg')
    // A custom provider keeps a models.dev logo it was given, and drops a local icon.
    expect(providerLogo({ icon: 'https://models.dev/logos/groq.svg', metadata: {} })).toBe('https://models.dev/logos/groq.svg')
    expect(providerLogo({ icon: 'openai', metadata: {} })).toBeUndefined()
    expect(providerLogo({ metadata: { catalog: 'newapi' } })).toBe('https://models.dev/logos/newapi.svg')
  })

  it('drafts a provider that records its entry and source', () => {
    const draft = providerDraftFromSource(findModelSource('azure-openai')!)
    expect(draft).toMatchObject({
      name: 'Azure OpenAI',
      client_type: 'openai-completions',
      enable: false,
      metadata: { catalog: 'azure-openai' },
    })
    expect(providerDraftFromSource(findModelSource('ollama')!).metadata).toEqual({ catalog: 'ollama' })
    expect(providerDraftFromSource(findModelSource('github-copilot')!).config).toEqual({})
  })

  it('finds the entry of providers made by any route', () => {
    expect(providerCatalogEntry({ metadata: { catalog: 'ollama' } })?.id).toBe('ollama')
    expect(providerCatalogEntry({ metadata: { source: 'deepinfra' } })?.id).toBe('deepinfra')
    expect(providerCatalogEntry({ metadata: { registry: { source: 'qwen.yaml' } } })?.id).toBe('alibaba-cn')
    expect(providerCatalogEntry({ metadata: { preset: { source: 'codex.yaml' } } })?.id).toBe('codex')
    expect(providerCatalogEntry({ metadata: {} })).toBeUndefined()
    // An entry id a later release dropped falls through to what else is recorded.
    expect(providerCatalogEntry({ metadata: { catalog: 'gone', source: 'deepinfra' } })?.id).toBe('deepinfra')
    expect(providerCatalogEntry({ metadata: { catalog: 'gone', registry: { source: 'qwen.yaml' } } })?.id).toBe('alibaba-cn')
    expect(providerCatalogEntry({ metadata: { catalog: 'gone' } })).toBeUndefined()
    expect(providerSource({ metadata: { source: 'xai' } })).toBe('xai')
    // A local server lists the models the user loaded, so it imports from its endpoint.
    expect(providerSource({ metadata: { catalog: 'lmstudio' } })).toBeUndefined()
    expect(modelSourceLogo(findModelSource('lmstudio')!)).toBe('https://models.dev/logos/lmstudio.svg')
    expect(providerSource({ metadata: { catalog: 'ollama' } })).toBeUndefined()
    // A provider from a former template imports through its entry's source.
    expect(providerSource({ metadata: { registry: { source: 'qwen.yaml' } } })).toBe('alibaba-cn')
  })
})
