import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  getConnector: vi.fn(),
  reauth: vi.fn(),
}))

vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdConnectorsByConnectionId: mocks.getConnector,
  postBotsByBotIdConnectorsByConnectionIdReauth: mocks.reauth,
}))

import {
  connectorErrorMessage,
  isConnectorOAuthCancelled,
  openConnectorOAuthURL,
  reauthorizeConnector,
  reauthorizeFailureNotice,
  waitForConnectorOAuth,
} from './useConnectorOAuth'

// Failure modes covered in this suite:
// 1. Poll cancellation / error conflation: caller abort treated as hard OAuth failure
//    (or vice versa), or pre-opened popup left dangling on cancel/failure.
// 2. Malicious or malformed `authorization_url` returned by backend/Connect-It
//    (`javascript:`, `file://`, `data:`, `ftp:`, non-URL): navigating the popup or
//    invoking `desktop.openExternalUrl` instead of closing the popup and failing closed.
// 3. Popup blocked/closed mid-flight silently spinning for 120s instead of failing fast.

describe('waitForConnectorOAuth', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.getConnector.mockResolvedValue({ data: { status: 'pending' } })
  })

  it('resolves once the connection turns active', async () => {
    mocks.getConnector.mockResolvedValue({ data: { status: 'active' } })
    await expect(waitForConnectorOAuth('bot', 'conn', null)).resolves.toBeUndefined()
  })

  it('rejects as cancelled when the caller aborts mid-wait', async () => {
    const controller = new AbortController()
    const popup = { closed: false, close: vi.fn() } as unknown as Window

    const wait = waitForConnectorOAuth('bot', 'conn', popup, controller.signal)
    // Let the first poll run and settle on `pending` before cancelling, so the
    // abort has to interrupt a wait that would otherwise keep polling.
    await vi.waitFor(() => expect(mocks.getConnector).toHaveBeenCalled())
    controller.abort()

    await expect(wait).rejects.toSatisfy(isConnectorOAuthCancelled)
    expect(popup.close).toHaveBeenCalled()
  })

  it('rejects immediately when the signal is already aborted', async () => {
    await expect(
      waitForConnectorOAuth('bot', 'conn', null, AbortSignal.abort()),
    ).rejects.toSatisfy(isConnectorOAuthCancelled)
    expect(mocks.getConnector).not.toHaveBeenCalled()
  })

  it('does not treat a real failure as a cancellation', async () => {
    mocks.getConnector.mockResolvedValue({ data: { status: 'authorization_failed' } })
    await expect(waitForConnectorOAuth('bot', 'conn', null)).rejects.toSatisfy(
      error => !isConnectorOAuthCancelled(error),
    )
  })
})

const missingOAuthApp = {
  type: 'urn:memoh:error:connector.oauth_client_not_configured', code: 'connector.oauth_client_not_configured',
  status: 503, fault: 'dependency', detail: 'Connect-It has no OAuth App configured for this connector.', args: {},
}
const t = (key: string, params?: Record<string, unknown>) => params?.connector ? `${key}(${params.connector})` : key

describe('connectorErrorMessage', () => {
  it.each([
    ['admin', 'connectors.oauthAppNotConfigured.admin(GitHub)'],
    ['member', 'connectors.oauthAppNotConfigured.member(GitHub)'],
    ['', 'connectors.oauthAppNotConfigured.member(GitHub)'],
  ])('tells a %s user what to do when Connect-It has no OAuth App', (role, message) => {
    expect(connectorErrorMessage(missingOAuthApp, t, { role, connector: 'GitHub', fallback: 'fallback' })).toBe(message)
  })

  it('keeps the copy of other errors', () => {
    const rejected = { ...missingOAuthApp, code: 'connector.request_rejected', status: 400, fault: 'client' }
    expect(connectorErrorMessage(rejected, t, { role: 'admin', connector: 'GitHub', fallback: 'fallback' }))
      .toBe('Connect-It rejected the connector request.')
    expect(connectorErrorMessage(new Error('oauth_failed'), t, { role: 'admin', connector: 'GitHub', fallback: 'fallback' }))
      .toBe('connectors.oauthFailed')
    expect(connectorErrorMessage(new Error('boom'), t, { role: 'admin', connector: 'GitHub', fallback: 'fallback' }))
      .toBe('fallback')
  })
})

describe('reauthorizeConnector', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('surfaces a missing OAuth App before touching the popup', async () => {
    mocks.reauth.mockRejectedValue(missingOAuthApp)
    const popup = { closed: false, close: vi.fn(), location: { href: 'about:blank' } } as unknown as Window
    const error = await reauthorizeConnector('bot', 'conn', popup).catch((err: unknown) => err)
    expect(mocks.reauth).toHaveBeenCalledWith({ path: { bot_id: 'bot', connection_id: 'conn' }, throwOnError: true })
    expect(connectorErrorMessage(error, t, { role: 'member', connector: 'GitHub', fallback: 'fallback' }))
      .toBe('connectors.oauthAppNotConfigured.member(GitHub)')
    expect(popup.location.href).toBe('about:blank')
    expect(mocks.getConnector).not.toHaveBeenCalled()
  })

  it('opens the authorization URL and waits for the connection to turn active', async () => {
    vi.stubGlobal('window', {})
    mocks.reauth.mockResolvedValue({ data: { connection_id: 'conn', authorization_url: 'https://github.com/login/oauth/authorize' } })
    mocks.getConnector.mockResolvedValue({ data: { status: 'active' } })
    const popup = { closed: false, close: vi.fn(), location: { href: 'about:blank' } } as unknown as Window
    await expect(reauthorizeConnector('bot', 'conn', popup)).resolves.toBeUndefined()
    expect(popup.location.href).toBe('https://github.com/login/oauth/authorize')
  })

  it.each([
    'javascript:alert(document.domain)',
    'JAVASCRIPT:void(0)',
    'file:///etc/passwd',
    'data:text/html,<script>alert(1)</script>',
    'ftp://auth.example.com/oauth',
    'not a valid url',
    '',
  ])('rejects unsafe or malformed authorization_url (%s), closes the popup, and never polls', async (unsafeUrl) => {
    const openExternalUrl = vi.fn()
    vi.stubGlobal('window', { api: { desktop: { openExternalUrl } } })
    mocks.reauth.mockResolvedValue({ data: { connection_id: 'conn', authorization_url: unsafeUrl } })
    const popup = { closed: false, close: vi.fn(), location: { href: 'about:blank' } } as unknown as Window

    const error = await reauthorizeConnector('bot', 'conn', popup).catch((err: unknown) => err)
    expect(error).toBeInstanceOf(Error)
    expect((error as Error).message).toBe('oauth_failed')
    expect(popup.close).toHaveBeenCalledTimes(1)
    expect(popup.location.href).toBe('about:blank')
    expect(openExternalUrl).not.toHaveBeenCalled()
    expect(mocks.getConnector).not.toHaveBeenCalled()
  })
})

describe('openConnectorOAuthURL', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('allows http: and https: URLs on web and delegates to desktop openExternalUrl when present', async () => {
    vi.stubGlobal('window', {})
    const popup = { closed: false, close: vi.fn(), location: { href: 'about:blank' } } as unknown as Window
    await openConnectorOAuthURL('http://localhost:18080/oauth/authorize?client_id=1', popup)
    expect(popup.location.href).toBe('http://localhost:18080/oauth/authorize?client_id=1')
    expect(popup.close).not.toHaveBeenCalled()

    const openExternalUrl = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('window', { api: { desktop: { openExternalUrl } } })
    await openConnectorOAuthURL('https://accounts.example.com/oauth2/v2/auth', null)
    expect(openExternalUrl).toHaveBeenCalledWith('https://accounts.example.com/oauth2/v2/auth')
  })

  it('fails fast with oauth_popup_blocked when the browser blocked or closed the popup', async () => {
    vi.stubGlobal('window', {})
    await expect(openConnectorOAuthURL('https://accounts.example.com/auth', null))
      .rejects.toThrow('oauth_popup_blocked')

    const closedPopup = { closed: true, close: vi.fn(), location: { href: 'about:blank' } } as unknown as Window
    await expect(openConnectorOAuthURL('https://accounts.example.com/auth', closedPopup))
      .rejects.toThrow('oauth_popup_blocked')
  })
})

describe('reauthorizeFailureNotice', () => {
  const catalog = new Map([['github', { name: 'GitHub' }]])

  it.each([
    ['admin', 'connectors.oauthAppNotConfigured.admin(GitHub)'],
    ['member', 'connectors.oauthAppNotConfigured.member(GitHub)'],
  ])('keeps the %s steps for a missing OAuth App until dismissed', (role, message) => {
    expect(reauthorizeFailureNotice(missingOAuthApp, t, role, { type: 'github' }, catalog))
      .toEqual({ message, duration: Number.POSITIVE_INFINITY })
  })

  it('names the connector by its type when the catalog lacks it', () => {
    expect(reauthorizeFailureNotice(missingOAuthApp, t, 'member', { type: 'gitlab' }, catalog).message)
      .toBe('connectors.oauthAppNotConfigured.member(gitlab)')
  })

  it('keeps other failures as a short notice', () => {
    expect(reauthorizeFailureNotice(new Error('boom'), t, 'admin', { type: 'github' }, catalog))
      .toEqual({ message: 'connectors.oauthFailed', duration: undefined })
  })
})
