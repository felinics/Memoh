import { describe, expect, it } from 'vitest'
import {
  findModelSource,
  findModelSourceBySource,
  isMonochromeLogo,
  MODEL_SOURCES,
  modelSourceLogo,
  modelSourceNeedsApiKey,
  providerCatalogEntry,
  providerDraftFromSource,
  providerSource,
  TEMPLATE_SOURCES,
} from './model-sources'
import { iconMap } from '@/components/provider-icon/icons'

const CLIENT_TYPES = new Set([
  'openai-completions', 'openai-responses', 'anthropic-messages', 'google-generative-ai',
  'openai-codex', 'github-copilot', 'opencode-go',
])

describe('model sources', () => {
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

  it('only names local icons that exist', () => {
    for (const item of MODEL_SOURCES) {
      if (item.icon) expect(iconMap[item.icon], item.id).toBeDefined()
    }
  })

  it('maps every former template to an entry', () => {
    for (const [template, id] of Object.entries(TEMPLATE_SOURCES)) {
      expect(findModelSource(id), template).toBeDefined()
    }
  })

  it('finds entries and their logos', () => {
    expect(findModelSource('openai')?.client_type).toBe('openai-responses')
    expect(findModelSource('azure-openai')?.source).toBe('azure')
    expect(findModelSourceBySource('azure')?.id).toBe('azure-openai')
    expect(findModelSource(undefined)).toBeUndefined()
    expect(modelSourceLogo(findModelSource('anthropic')!)).toBe('anthropic')
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
  it('drafts a provider that records its entry and source', () => {
    const draft = providerDraftFromSource(findModelSource('azure-openai')!)
    expect(draft).toMatchObject({
      name: 'Azure OpenAI',
      client_type: 'openai-completions',
      enable: false,
      metadata: { catalog: 'azure-openai', source: 'azure' },
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
    expect(providerSource({ metadata: { source: 'xai' } })).toBe('xai')
    expect(providerSource({ metadata: { catalog: 'ollama' } })).toBeUndefined()
  })
})
