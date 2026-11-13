import { describe, expect, it, vi } from 'vitest'
import { attachGuestWindowOpenGuard, normalizeExternalUrl, resolveNavigationGuardAction } from './external-links'

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

  it('routes http(s)/mailto links from preview subframes to OS browser while blocking unsafe schemes and preserving workspace browser frames', () => {
    expect(resolveNavigationGuardAction({
      url: 'https://memoh.ai/pricing',
      isMainFrame: false,
      isTrustedRenderer: false,
      frameUrl: 'blob:http://localhost:18082/1234-5678',
    })).toEqual({ kind: 'external', url: 'https://memoh.ai/pricing' })

    expect(resolveNavigationGuardAction({
      url: 'mailto:support@memoh.ai',
      isMainFrame: false,
      isTrustedRenderer: false,
      frameUrl: 'chrome-extension://mhjfbmdgcfjbbpaeojofohoefgiehjai/index.html',
      initiatorUrl: 'blob:file:///1234-5678',
      ancestorFrameUrls: ['blob:file:///1234-5678'],
    })).toEqual({ kind: 'external', url: 'mailto:support@memoh.ai' })

    expect(resolveNavigationGuardAction({
      url: 'https://memoh.ai/docs',
      isMainFrame: false,
      isTrustedRenderer: false,
      frameUrl: 'about:srcdoc',
    })).toEqual({ kind: 'external', url: 'https://memoh.ai/docs' })

    expect(resolveNavigationGuardAction({
      url: 'file:///etc/passwd',
      isMainFrame: false,
      isTrustedRenderer: false,
      frameUrl: 'blob:http://localhost:18082/1234-5678',
    })).toEqual({ kind: 'block', url: 'file:///etc/passwd' })

    expect(resolveNavigationGuardAction({
      url: 'blob:http://localhost:18082/next-doc',
      isMainFrame: false,
      isTrustedRenderer: false,
      frameUrl: 'about:blank',
    })).toEqual({ kind: 'allow' })

    expect(resolveNavigationGuardAction({
      url: 'https://example.com/oauth/callback',
      isMainFrame: false,
      isTrustedRenderer: false,
      frameUrl: 'http://abc123.browser.memoh.example.com:8080/start',
    })).toEqual({ kind: 'allow' })

    expect(resolveNavigationGuardAction({
      url: 'https://example.com/nested-widget-link',
      isMainFrame: false,
      isTrustedRenderer: false,
      frameUrl: 'about:srcdoc',
      ancestorFrameUrls: ['http://abc123.browser.memoh.example.com:8080/app'],
    })).toEqual({ kind: 'allow' })
  })
})

describe('attachGuestWindowOpenGuard', () => {
  it('skips top-level window WebContents so BrowserWindow guards keep ownership', () => {
    const setWindowOpenHandler = vi.fn()
    const attached = attachGuestWindowOpenGuard(
      { getType: () => 'window', setWindowOpenHandler },
      vi.fn(),
    )
    expect(attached).toBe(false)
    expect(setWindowOpenHandler).not.toHaveBeenCalled()
  })

  it('denies in-app popup windows from PDF/guest WebContents and routes supported URLs to OS browser', () => {
    let handler: ((details: { url: string }) => { action: 'deny' }) | null = null
    const openExternal = vi.fn(() => Promise.resolve())
    const onBlocked = vi.fn()

    const attached = attachGuestWindowOpenGuard(
      {
        getType: () => 'webview',
        setWindowOpenHandler: (fn) => { handler = fn },
      },
      openExternal,
      onBlocked,
    )

    expect(attached).toBe(true)
    expect(handler).not.toBeNull()

    expect(handler!({ url: ' https://memoh.ai/docs ' })).toEqual({ action: 'deny' })
    expect(openExternal).toHaveBeenCalledWith('https://memoh.ai/docs')
    expect(onBlocked).not.toHaveBeenCalled()

    expect(handler!({ url: 'mailto:support@memoh.ai' })).toEqual({ action: 'deny' })
    expect(openExternal).toHaveBeenCalledWith('mailto:support@memoh.ai')

    expect(handler!({ url: 'file:///etc/hosts' })).toEqual({ action: 'deny' })
    expect(openExternal).toHaveBeenCalledTimes(2)
    expect(onBlocked).toHaveBeenCalledWith('file:///etc/hosts')
  })
})
