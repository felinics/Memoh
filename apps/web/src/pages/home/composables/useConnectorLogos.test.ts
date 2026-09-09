import { describe, expect, it } from 'vitest'
import { connectorAliasOf } from './useConnectorLogos'

describe('connectorAliasOf', () => {
  it('reads the alias Connect-It puts in front of a connector tool', () => {
    expect(connectorAliasOf('notion__notion-search')).toBe('notion')
    expect(connectorAliasOf('github-2__search_repositories')).toBe('github-2')
  })

  it('splits on the first separator so a tool name may keep its own', () => {
    expect(connectorAliasOf('notion__notion__fetch')).toBe('notion')
  })

  it('claims nothing from a native tool name', () => {
    expect(connectorAliasOf('web_search')).toBe('')
    expect(connectorAliasOf('')).toBe('')
  })

  it('rejects a leading separator rather than reading an empty alias', () => {
    expect(connectorAliasOf('__orphan')).toBe('')
  })
})
