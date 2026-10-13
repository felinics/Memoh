import { describe, expect, it } from 'vitest'
import { findModelSource, isMonochromeLogo, MODEL_SOURCES, modelSourceLogo } from './model-sources'
import { iconMap } from '@/components/provider-icon/icons'

const CLIENT_TYPES = new Set(['openai-completions', 'openai-responses', 'anthropic-messages', 'google-generative-ai'])

describe('model sources', () => {
  it('lists each provider once, sorted by source', () => {
    const ids = MODEL_SOURCES.map(item => item.source)
    expect(new Set(ids).size).toBe(ids.length)
    expect(ids).toEqual([...ids].sort((a, b) => a.localeCompare(b)))
  })

  it('gives every entry a name, a supported client type and a usable base URL', () => {
    for (const item of MODEL_SOURCES) {
      expect(item.name.trim(), item.source).not.toBe('')
      expect(CLIENT_TYPES.has(item.client_type), item.source).toBe(true)
      const url = new URL(item.base_url)
      expect(['https:', 'http:'], item.source).toContain(url.protocol)
      expect(item.base_url, item.source).not.toMatch(/\$\{|\/$/)
    }
  })

  it('only names local icons that exist', () => {
    for (const item of MODEL_SOURCES) {
      if (item.icon) expect(iconMap[item.icon], item.source).toBeDefined()
    }
  })

  it('finds an entry and its logo', () => {
    expect(findModelSource('openai')?.client_type).toBe('openai-responses')
    expect(findModelSource('github-copilot')).toBeUndefined()
    expect(findModelSource(undefined)).toBeUndefined()
    expect(modelSourceLogo(findModelSource('anthropic')!)).toBe('anthropic')
    expect(modelSourceLogo(findModelSource('baseten')!)).toBe('https://models.dev/logos/baseten.svg')
  })

  it('treats only one-color models.dev logos as monochrome', () => {
    expect(isMonochromeLogo('https://models.dev/logos/baseten.svg')).toBe(true)
    expect(isMonochromeLogo('https://models.dev/logos/302ai.svg')).toBe(false)
    expect(isMonochromeLogo('anthropic')).toBe(false)
    expect(isMonochromeLogo('https://example.com/logo.svg')).toBe(false)
    expect(isMonochromeLogo(undefined)).toBe(false)
  })
})
