import { afterEach, describe, expect, it, vi } from 'vitest'
import { connectChatGPT, normalizeChatGPTRequest, secureServer } from './chatgpt-auth'

const sdk = vi.hoisted(() => ({ begin: vi.fn(), complete: vi.fn(), retain: vi.fn() }))
vi.mock('@memohai/sdk', () => ({ beginChatGptAuthorization: sdk.begin, completeChatGptAuthorization: sdk.complete, retainChatGptRegistration: sdk.retain }))
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.clearAllMocks() })
const request = { providerId: '7c468882-dc81-4f37-a9eb-fd9009352d04', token: 'memoh-session' }
function prepare(clientId = 'dynamic_agent_client') {
  sdk.begin.mockImplementation(({ body }: { body: { redirect_uri: string } }) => {
    const query = new URLSearchParams({ state: 'expected-state', redirect_uri: body.redirect_uri })
    return { data: { state: 'expected-state', code_verifier: 'private-verifier', client_id: clientId,
      authorization_url: `https://auth.openai.com/api/accounts/authorize?${query}` } }
  })
  sdk.complete.mockResolvedValue({ data: { configured: true } })
  sdk.retain.mockResolvedValue({ response: new Response(null, { status: 204 }) })
}
describe('local ChatGPT authorization boundary', () => {
  it('binds a real loopback callback, rejects wrong state and exchanges locally with the issued client', async () => {
    prepare()
    const actualFetch = globalThis.fetch
    const exchange = vi.fn().mockResolvedValue(new Response(JSON.stringify({ access_token: 'private-access', refresh_token: 'private-refresh', id_token: 'private-id' })))
    vi.stubGlobal('fetch', (url: string, init?: RequestInit) => url.startsWith('https://auth.openai.com/') ? exchange(url, init) : actualFetch(url, init))
    const result = await connectChatGPT({ baseUrl: 'https://memoh.example', request, signal: new AbortController().signal,
      currentBaseUrl: () => 'https://memoh.example', openExternal: async (raw) => {
        const authorization = new URL(raw)
        const callback = new URL(authorization.searchParams.get('redirect_uri')!)
        expect(callback.hostname).toBe('127.0.0.1')
        callback.search = new URLSearchParams({ state: 'wrong', code: 'intercepted', client_id: 'oaiapp_test' }).toString()
        expect((await actualFetch(callback)).status).toBe(400)
        callback.search = new URLSearchParams({ state: 'expected-state', code: 'auth-code', client_id: 'oaiapp_test' }).toString()
        expect((await actualFetch(callback)).status).toBe(200)
      } })
    expect(result).toEqual({ ok: true })
    const form = exchange.mock.calls[0]![1].body as URLSearchParams
    expect(form.get('client_id')).toBe('oaiapp_test')
    expect(form.get('code_verifier')).toBe('private-verifier')
    expect(form.get('resource')).toBe('https://api.openai.com/v1')
    expect(sdk.complete.mock.calls[0]![0].body.tokens.refresh_token).toBe('private-refresh')
    expect(sdk.retain.mock.calls[0]![0].body).toEqual({ state: 'expected-state', client_id: 'oaiapp_test' })
    expect(sdk.retain.mock.invocationCallOrder[0]).toBeLessThan(exchange.mock.invocationCallOrder[0]!)
    expect(JSON.stringify(result)).not.toContain('private')
  })
  it.each([
    { name: 'returning sign-in without a callback client ID', pending: 'oaiapp_test', returned: '', ok: true },
    { name: 'returning sign-in with its registered client ID', pending: 'oaiapp_test', returned: 'oaiapp_test', ok: true },
    { name: 'returning sign-in with a different client ID', pending: 'oaiapp_test', returned: 'oaiapp_other', ok: false },
    { name: 'new registration without an issued client ID', pending: 'dynamic_agent_client', returned: '', ok: false },
  ])('$name', async ({ pending, returned, ok }) => {
    prepare(pending)
    const actualFetch = globalThis.fetch
    const exchange = vi.fn().mockResolvedValue(new Response(JSON.stringify({ access_token: 'private-access' })))
    vi.stubGlobal('fetch', (url: string, init?: RequestInit) => url.startsWith('https://auth.openai.com/') ? exchange(url, init) : actualFetch(url, init))
    const result = await connectChatGPT({ baseUrl: 'https://memoh.example', request, signal: new AbortController().signal,
      currentBaseUrl: () => 'https://memoh.example', openExternal: async (raw) => {
        const callback = new URL(new URL(raw).searchParams.get('redirect_uri')!)
        callback.search = new URLSearchParams({ state: 'expected-state', code: 'auth-code', ...(returned ? { client_id: returned } : {}) }).toString()
        await actualFetch(callback)
      } })
    expect(result).toEqual(ok ? { ok: true } : { ok: false, code: 'chatgpt.authorization_invalid' })
    expect(exchange).toHaveBeenCalledTimes(ok ? 1 : 0)
    expect(sdk.complete).toHaveBeenCalledTimes(ok ? 1 : 0)
    if (ok) expect((exchange.mock.calls[0]![1].body as URLSearchParams).get('client_id')).toBe(pending)
  })
  it('cancels without importing credentials or leaving an active listener', async () => {
    prepare()
    const controller = new AbortController()
    let callbackUrl = ''
    const result = await connectChatGPT({ baseUrl: 'http://127.0.0.1:19080', request, signal: controller.signal,
      currentBaseUrl: () => 'http://127.0.0.1:19080', openExternal: async (raw) => {
        callbackUrl = new URL(raw).searchParams.get('redirect_uri')!
        controller.abort()
      } })
    expect(result).toEqual({ ok: false, code: 'chatgpt.authorization_cancelled' })
    expect(sdk.complete).not.toHaveBeenCalled()
    await expect(fetch(callbackUrl)).rejects.toThrow()
  })
  it('retains registration on invalid_grant and reuses it on a fresh sign-in', async () => {
    prepare()
    const actualFetch = globalThis.fetch
    const exchange = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ error: 'invalid_grant' }), { status: 400 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ access_token: 'private-access' })))
    vi.stubGlobal('fetch', (url: string, init?: RequestInit) => url.startsWith('https://auth.openai.com/') ? exchange(url, init) : actualFetch(url, init))
    const options = { baseUrl: 'https://memoh.example', request, signal: new AbortController().signal,
      currentBaseUrl: () => 'https://memoh.example', openExternal: async (raw: string) => {
        const callback = new URL(new URL(raw).searchParams.get('redirect_uri')!)
        callback.search = new URLSearchParams({ state: 'expected-state', code: 'auth-code', client_id: 'oaiapp_test' }).toString()
        await actualFetch(callback)
      } }
    expect(await connectChatGPT(options)).toEqual({ ok: false, code: 'chatgpt.authorization_invalid' })
    expect(sdk.retain).toHaveBeenCalledOnce()
    expect(sdk.complete).not.toHaveBeenCalled()
    prepare('oaiapp_test')
    expect(await connectChatGPT(options)).toEqual({ ok: true })
    expect(sdk.retain).toHaveBeenCalledOnce()
    expect((exchange.mock.calls[1]![1].body as URLSearchParams).get('client_id')).toBe('oaiapp_test')
  })
  it('does not exchange a code when its registration could not be retained', async () => {
    prepare()
    sdk.retain.mockResolvedValue({ response: new Response(null, { status: 503 }), error: { code: 'chatgpt.unavailable' } })
    const actualFetch = globalThis.fetch
    const exchange = vi.fn()
    vi.stubGlobal('fetch', (url: string, init?: RequestInit) => url.startsWith('https://auth.openai.com/') ? exchange(url, init) : actualFetch(url, init))
    const result = await connectChatGPT({ baseUrl: 'https://memoh.example', request, signal: new AbortController().signal,
      currentBaseUrl: () => 'https://memoh.example', openExternal: async (raw) => {
        const callback = new URL(new URL(raw).searchParams.get('redirect_uri')!)
        callback.search = new URLSearchParams({ state: 'expected-state', code: 'auth-code', client_id: 'oaiapp_test' }).toString()
        await actualFetch(callback)
      } })
    expect(result).toEqual({ ok: false, code: 'chatgpt.unavailable' })
    expect(exchange).not.toHaveBeenCalled()
    expect(sdk.complete).not.toHaveBeenCalled()
  })
  it('requires HTTPS for remote transfer and validates narrow renderer input', () => {
    expect(secureServer('http://remote.example')).toBe(false)
    expect(secureServer('https://remote.example')).toBe(true)
    expect(secureServer('http://localhost:19080')).toBe(true)
    expect(secureServer('https://user:password@remote.example')).toBe(false)
    expect(normalizeChatGPTRequest({ ...request, providerId: '../other' })).toBeNull()
    expect(normalizeChatGPTRequest(request)).toEqual(request)
  })
})
