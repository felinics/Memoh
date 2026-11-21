// @vitest-environment jsdom
/**
 * Failure modes covered for MCP OAuth popup & callback security:
 * 1. `mcp-callback.vue` posting completion messages with wildcard `'*'` targetOrigin,
 *    exposing OAuth completion/error state to a cross-origin opener, or posting when
 *    `window.location.origin === 'null'` (sandboxed / opaque frame).
 * 2. `mcp-server-detail.vue` passing a non-http(s) `authorization_url` (`javascript:`,
 *    `file://`, `data:`, malformed URL) to `window.open` or `desktop.openExternalUrl`.
 * 3. `mcp-server-detail.vue` accepting forged `message` events from an untrusted
 *    `event.origin`, from a window other than the opened `popup` (`event.source !== popup`),
 *    or when `popup === null` (Desktop external-browser flow), allowing an unrelated frame
 *    to spoof OAuth completion or prematurely abort/close the flow.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import i18n from '@/i18n'

const mocks = vi.hoisted(() => ({
  oauthExchange: vi.fn(),
  oauthStatus: vi.fn(),
  oauthDiscover: vi.fn(),
  oauthAuthorize: vi.fn(),
  oauthRevoke: vi.fn(),
  probeMcp: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
  routeQuery: {} as Record<string, string>,
}))

vi.mock('vue-router', () => ({
  useRoute: () => reactive({ query: mocks.routeQuery }),
}))

vi.mock('@memohai/sdk', () => ({
  postBotsByBotIdMcpByIdOauthExchange: mocks.oauthExchange,
  getBotsByBotIdMcpByIdOauthStatus: mocks.oauthStatus,
  postBotsByBotIdMcpByIdOauthDiscover: mocks.oauthDiscover,
  postBotsByBotIdMcpByIdOauthAuthorize: mocks.oauthAuthorize,
  deleteBotsByBotIdMcpByIdOauthToken: mocks.oauthRevoke,
  postBotsByBotIdMcpByIdProbe: mocks.probeMcp,
  postBotsByBotIdMcp: vi.fn(),
  putBotsByBotIdMcpById: vi.fn(),
  deleteBotsByBotIdMcpById: vi.fn(),
}))

vi.mock('@memohai/sdk/client', () => ({
  client: {
    getConfig: () => ({ baseUrl: 'http://localhost:8082/api' }),
  },
}))

vi.mock('@felinic/ui', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@felinic/ui')>()
  return {
    ...actual,
    toast: Object.assign(vi.fn(), {
      success: mocks.toastSuccess,
      error: mocks.toastError,
      dismiss: vi.fn(),
    }),
  }
})

import McpCallbackPage from './mcp-callback.vue'
import McpServerDetail from '@/pages/bots/components/mcp-server-detail.vue'

if (typeof globalThis.ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

async function flushAsync() {
  await Promise.resolve()
  await nextTick()
  await new Promise(resolve => setTimeout(resolve, 0))
  await nextTick()
}

describe('mcp-callback.vue opener postMessage origin enforcement', () => {
  let app: App | undefined
  let host: HTMLDivElement | undefined
  const originalOpener = window.opener

  beforeEach(() => {
    vi.clearAllMocks()
    mocks.routeQuery = {}
  })

  afterEach(() => {
    app?.unmount()
    host?.remove()
    Object.defineProperty(window, 'opener', { value: originalOpener, configurable: true, writable: true })
  })

  it('posts OAuth success strictly to window.location.origin (never wildcard "*")', async () => {
    const postMessage = vi.fn()
    Object.defineProperty(window, 'opener', { value: { postMessage }, configurable: true, writable: true })
    mocks.routeQuery = { code: 'auth-code-1', state: 'csrf-state-1' }
    mocks.oauthExchange.mockResolvedValue({ data: { ok: true } })

    host = document.createElement('div')
    document.body.appendChild(host)
    app = createApp(McpCallbackPage)
    app.use(i18n)
    app.mount(host)
    await flushAsync()

    expect(mocks.oauthExchange).toHaveBeenCalledWith({
      path: { bot_id: '-', id: '-' },
      body: { code: 'auth-code-1', state: 'csrf-state-1' },
      throwOnError: true,
    })
    expect(postMessage).toHaveBeenCalledTimes(1)
    expect(postMessage).toHaveBeenCalledWith(
      { type: 'mcp-oauth-callback', status: 'success', error: undefined },
      window.location.origin,
    )
    expect(postMessage.mock.calls[0]?.[1]).not.toBe('*')
  })

  it('refuses to call postMessage when window.location.origin is opaque "null"', async () => {
    const postMessage = vi.fn()
    Object.defineProperty(window, 'opener', { value: { postMessage }, configurable: true, writable: true })
    const locationSpy = vi.spyOn(window, 'location', 'get').mockReturnValue({
      ...window.location,
      origin: 'null',
    })
    mocks.routeQuery = { code: 'auth-code-2', state: 'csrf-state-2' }
    mocks.oauthExchange.mockResolvedValue({ data: { ok: true } })

    try {
      host = document.createElement('div')
      document.body.appendChild(host)
      app = createApp(McpCallbackPage)
      app.use(i18n)
      app.mount(host)
      await flushAsync()

      expect(postMessage).not.toHaveBeenCalled()
    } finally {
      locationSpy.mockRestore()
    }
  })
})

describe('mcp-server-detail.vue OAuth URL protocol & callback message validation', () => {
  let app: App | undefined
  let host: HTMLDivElement | undefined
  const originalOpen = window.open
  const originalApi = window.api

  beforeEach(() => {
    vi.clearAllMocks()
    mocks.oauthStatus.mockResolvedValue({ data: { configured: false, has_token: false } })
    mocks.oauthDiscover.mockResolvedValue({ data: { registration_endpoint: 'https://auth.example.com/register' } })
    mocks.probeMcp.mockResolvedValue({ data: { status: 'connected', tools: [] } })
  })

  afterEach(() => {
    app?.unmount()
    host?.remove()
    window.open = originalOpen
    window.api = originalApi
  })

  function mountRemoteDetail() {
    host = document.createElement('div')
    document.body.appendChild(host)
    app = createApp(McpServerDetail, {
      botId: 'bot-1',
      server: {
        id: 'mcp-1',
        name: 'Remote MCP',
        type: 'http',
        is_active: true,
        status: 'error',
        config: { url: 'https://mcp.example.com/mcp' },
      },
    })
    app.use(i18n)
    app.mount(host)
    return host
  }

  function findAuthorizeButton(container: HTMLElement): HTMLButtonElement {
    const buttons = Array.from(container.querySelectorAll('button'))
    const authorizeText = i18n.global.t('mcp.oauth.authorize')
    const match = buttons.find(btn => btn.textContent?.includes(authorizeText))
    if (!match) throw new Error(`Authorize button "${authorizeText}" not found`)
    return match
  }

  it.each([
    'javascript:alert(1)',
    'file:///etc/hosts',
    'data:text/html,<script>evil()</script>',
    'not-a-valid-url',
  ])('blocks non-http(s) authorization_url (%s) before opening a popup or external browser', async (unsafeUrl) => {
    const openSpy = vi.fn()
    const openExternalUrl = vi.fn()
    window.open = openSpy
    window.api = { desktop: { openExternalUrl } } as unknown as typeof window.api
    mocks.oauthAuthorize.mockResolvedValue({ data: { authorization_url: unsafeUrl } })

    const container = mountRemoteDetail()
    await flushAsync()

    findAuthorizeButton(container).click()
    await flushAsync()

    expect(openSpy).not.toHaveBeenCalled()
    expect(openExternalUrl).not.toHaveBeenCalled()
    expect(mocks.toastError).toHaveBeenCalledTimes(1)
  })

  it('ignores cross-origin and wrong-source message events and accepts only same-origin messages from popup', async () => {
    const popup = { closed: false, close: vi.fn() } as unknown as Window
    const otherWindow = { closed: false, close: vi.fn() } as unknown as Window
    window.open = vi.fn(() => popup)
    window.api = undefined
    mocks.oauthAuthorize.mockResolvedValue({
      data: { authorization_url: 'https://auth.example.com/oauth/authorize?state=s1' },
    })

    const container = mountRemoteDetail()
    await flushAsync()

    findAuthorizeButton(container).click()
    await flushAsync()
    expect(window.open).toHaveBeenCalledWith(
      'https://auth.example.com/oauth/authorize?state=s1',
      'mcp-oauth',
      'width=600,height=700',
    )

    // 1. Forged cross-origin message claiming to come from `popup` -> ignored.
    window.dispatchEvent(new MessageEvent('message', {
      data: { type: 'mcp-oauth-callback', status: 'success' },
      origin: 'https://evil.example.com',
      source: popup,
    }))
    await flushAsync()
    expect(popup.close).not.toHaveBeenCalled()
    expect(mocks.toastSuccess).not.toHaveBeenCalled()

    // 2. Same-origin message from a different window handle (`source !== popup`) -> ignored.
    window.dispatchEvent(new MessageEvent('message', {
      data: { type: 'mcp-oauth-callback', status: 'success' },
      origin: window.location.origin,
      source: otherWindow,
    }))
    await flushAsync()
    expect(popup.close).not.toHaveBeenCalled()
    expect(mocks.toastSuccess).not.toHaveBeenCalled()

    // 3. Same-origin message with `source: null` -> ignored.
    window.dispatchEvent(new MessageEvent('message', {
      data: { type: 'mcp-oauth-callback', status: 'success' },
      origin: window.location.origin,
      source: null,
    }))
    await flushAsync()
    expect(popup.close).not.toHaveBeenCalled()
    expect(mocks.toastSuccess).not.toHaveBeenCalled()

    // 4. Legitimate same-origin message from `popup` -> completes OAuth and closes popup.
    mocks.oauthStatus.mockResolvedValue({ data: { configured: true, has_token: true } })
    window.dispatchEvent(new MessageEvent('message', {
      data: { type: 'mcp-oauth-callback', status: 'success' },
      origin: window.location.origin,
      source: popup,
    }))
    await flushAsync()

    expect(popup.close).toHaveBeenCalledTimes(1)
    expect(mocks.toastSuccess).toHaveBeenCalledWith(i18n.global.t('mcp.oauth.authSuccess'))
  })

  it('ignores window message events in desktop mode when popup is null (system browser flow)', async () => {
    const openExternalUrl = vi.fn().mockResolvedValue(undefined)
    window.api = { desktop: { openExternalUrl } } as unknown as typeof window.api
    mocks.oauthAuthorize.mockResolvedValue({
      data: { authorization_url: 'https://auth.example.com/oauth/authorize?state=s2' },
    })

    const container = mountRemoteDetail()
    await flushAsync()

    findAuthorizeButton(container).click()
    await flushAsync()
    expect(openExternalUrl).toHaveBeenCalledWith('https://auth.example.com/oauth/authorize?state=s2')

    window.dispatchEvent(new MessageEvent('message', {
      data: { type: 'mcp-oauth-callback', status: 'success' },
      origin: window.location.origin,
      source: window,
    }))
    await flushAsync()

    expect(mocks.toastSuccess).not.toHaveBeenCalled()
  })
})
