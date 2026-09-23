import { describe, expect, it } from 'vitest'
import { classifyWorkspaceLink } from './workspace-link'

describe('workspace links', () => {
  it.each(['/data/counter/index.html', '/data/报告.md', '/data/a%20b.txt'])(
    'opens the workspace path %s instead of a site route', (href) => {
      expect(classifyWorkspaceLink(href)).toEqual({ kind: 'file', path: decodeURIComponent(href) })
    },
  )
  it.each(['http://localhost:8080/app?q=1#counter', 'http://127.0.0.1:8080/app?q=1#counter', 'http://[::1]:8080/app?q=1#counter'])(
    'routes %s to the workspace browser', (href) => {
      expect(classifyWorkspaceLink(href)).toEqual({ kind: 'browser', address: 'localhost:8080/app?q=1#counter' })
    },
  )
  it('keeps an explicitly specified default HTTP port', () => {
    expect(classifyWorkspaceLink('http://localhost:80/')).toEqual({ kind: 'browser', address: 'localhost:80/' })
  })
  it.each(['https://example.com', '//example.com/a', 'http://localhost.example.com:8080/'])(
    'keeps public URL %s external', (href) => expect(classifyWorkspaceLink(href)).toEqual({ kind: 'external' }),
  )
  it.each(['#section', 'mailto:user@example.com', 'javascript:alert(1)', 'file:///data/a', '/bad%zz', '/data/%00', '']) (
    'does not classify %s as a workspace link', (href) => expect(classifyWorkspaceLink(href)).toBeNull(),
  )
})
