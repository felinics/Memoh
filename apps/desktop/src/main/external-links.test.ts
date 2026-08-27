import { describe, expect, it } from 'vitest'
import { normalizeExternalUrl, resolveNavigationGuardAction } from './external-links'

const trusted = 'file:///Applications/Memoh.app/Contents/renderer/index.html'

describe('normalizeExternalUrl', () => {
  it('accepts web and mail protocols', () => {
    expect(normalizeExternalUrl(' https://memoh.ai/docs ')).toEqual({
      url: 'https://memoh.ai/docs',
      protocol: 'https:',
      supported: true,
    })
    expect(normalizeExternalUrl('mailto:hi@memoh.ai').supported).toBe(true)
  })

  it('rejects anything else', () => {
    expect(normalizeExternalUrl('file:///etc/passwd').supported).toBe(false)
    expect(normalizeExternalUrl('not a url').supported).toBe(false)
    expect(normalizeExternalUrl(null).supported).toBe(false)
  })
})

describe('resolveNavigationGuardAction', () => {
  it('lets the trusted renderer entry navigate', () => {
    expect(resolveNavigationGuardAction({
      url: trusted,
      isMainFrame: true,
      isTrustedRenderer: true,
    })).toEqual({ kind: 'allow' })
  })

  it('sends untrusted main-frame navigation to the OS browser', () => {
    expect(resolveNavigationGuardAction({
      url: 'https://memoh.ai/docs',
      isMainFrame: true,
      isTrustedRenderer: false,
    })).toEqual({ kind: 'external', url: 'https://memoh.ai/docs' })
  })

  it('blocks untrusted main-frame navigation on unsupported protocols', () => {
    expect(resolveNavigationGuardAction({
      url: 'chrome://settings',
      isMainFrame: true,
      isTrustedRenderer: false,
    })).toEqual({ kind: 'block', url: 'chrome://settings' })
  })

  // Regression: the workspace browser panel renders the proxied workspace site in
  // an <iframe>. `will-redirect` fires for EVERY frame (unlike `will-navigate`,
  // which is main-frame only), so a plain 3xx from the proxied site used to be
  // treated as an untrusted top-level navigation: the iframe load was cancelled
  // and the page was handed to the OS browser instead.
  it('leaves subframe navigation alone so the workspace browser stays embedded', () => {
    expect(resolveNavigationGuardAction({
      url: 'http://abc123.browser.memoh.example.com:8080/login',
      isMainFrame: false,
      isTrustedRenderer: false,
    })).toEqual({ kind: 'allow' })
  })

  it('leaves subframe navigation alone even on unsupported protocols', () => {
    expect(resolveNavigationGuardAction({
      url: 'about:blank',
      isMainFrame: false,
      isTrustedRenderer: false,
    })).toEqual({ kind: 'allow' })
  })
})
