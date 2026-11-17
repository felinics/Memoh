import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { apiErrorStatus, parseMemohError } from '@/utils/api-error'

vi.mock('./auth-session', () => ({ notifyAuthSessionCleared: vi.fn() }))

let client: typeof import('@memohai/sdk/client')['client']
let setupApiClient: typeof import('./api-client')['setupApiClient']
let fetchBoundedBlob: typeof import('./bounded-fetch')['fetchBoundedBlob']
let fetchBoundedText: typeof import('./bounded-fetch')['fetchBoundedText']

function chunkStream(chunks: Uint8Array[], hooks: { onPull?: () => void, onCancel?: () => void, keepOpen?: boolean } = {}) {
  let i = 0
  return new ReadableStream<Uint8Array>({
    pull(c) {
      if (i >= chunks.length) { if (!hooks.keepOpen) c.close(); return }
      hooks.onPull?.(); c.enqueue(chunks[i++]!)
    },
    cancel: () => hooks.onCancel?.(),
  }, new CountQueuingStrategy({ highWaterMark: 0 }))
}

describe('bounded-fetch', () => {
  beforeEach(async () => {
    vi.resetModules()
    const store = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: vi.fn((k: string) => store.get(k) ?? null),
      setItem: vi.fn((k: string, v: string) => { store.set(k, v) }),
      removeItem: vi.fn((k: string) => { store.delete(k) }),
      clear: vi.fn(() => { store.clear() }),
    })
    client = (await import('@memohai/sdk/client')).client
    setupApiClient = (await import('./api-client')).setupApiClient
    const bounded = await import('./bounded-fetch')
    fetchBoundedBlob = bounded.fetchBoundedBlob
    fetchBoundedText = bounded.fetchBoundedText
    client.interceptors.request.clear(); client.interceptors.response.clear(); client.interceptors.error.clear()
  })

  afterEach(() => {
    client.interceptors.request.clear(); client.interceptors.response.clear(); client.interceptors.error.clear()
    client.setConfig({ baseUrl: '/api', fetch: undefined })
    vi.unstubAllGlobals()
  })

  it('enforces Content-Length fast-reject and streaming chunk byte limits', async () => {
    let pulled = 0; let cancelled = false; let aborted = false; let pulledDeclared = false
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      const url = input instanceof Request ? input.url : String(input)
      const signal = input instanceof Request ? input.signal : init?.signal
      if (url.endsWith('/declared.pdf')) {
        return new Response(chunkStream([new Uint8Array(4)], { onPull: () => { pulledDeclared = true } }), { status: 200, headers: { 'Content-Length': '500' } })
      }
      if (url.includes('/under')) {
        return new Response(chunkStream([new Uint8Array(8), new Uint8Array(8)], { keepOpen: true }), { status: 200, headers: { 'Content-Length': '4' } })
      }
      signal?.addEventListener('abort', () => { aborted = true })
      return new Response(chunkStream([new Uint8Array(6), new Uint8Array(6), new Uint8Array(1024)], { onPull: () => { pulled++ }, onCancel: () => { cancelled = true } }), { status: 200 })
    })
    setupApiClient({ baseUrl: 'http://example.test/api', fetch: fetchMock })

    expect(await fetchBoundedBlob({ target: { url: '/bots/{bot_id}/media/{content_hash}', path: { bot_id: 'b1', content_hash: 'h1' } }, maxBytes: 10 }))
      .toEqual({ ok: false, reason: 'too-large', maxBytes: 10 })
    expect([pulled, cancelled, aborted]).toEqual([2, true, true])
    expect(await fetchBoundedBlob({ target: { url: '/under' }, maxBytes: 12 })).toEqual({ ok: false, reason: 'too-large', maxBytes: 12 })
    expect(await fetchBoundedBlob({ target: { src: 'https://cdn.other.test/declared.pdf' }, maxBytes: 100 })).toEqual({ ok: false, reason: 'too-large', maxBytes: 100 })
    expect(pulledDeclared).toBe(false)
    expect(fetchMock.mock.calls[2]?.[1]?.credentials).toBe('omit')
  })

  it('attaches SDK auth on managed API routes, handles 401 session cleanup, and preserves Problem Details', async () => {
    localStorage.setItem('token', 'session-token')
    const onUnauthorized = vi.fn()
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(JSON.stringify({ code: 'auth.unauthorized', status: 401 }), { status: 401, headers: { 'Content-Type': 'application/problem+json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ code: 'media.asset_not_found', status: 404, request_id: 'req-404' }), { status: 404, headers: { 'Content-Type': 'application/problem+json' } }))
    setupApiClient({ baseUrl: 'http://example.test/api', fetch: fetchMock, onUnauthorized })

    let err401: unknown
    try { await fetchBoundedBlob({ target: { src: 'http://example.test/api/bots/b1/media/h1' }, maxBytes: 1024 }) } catch (e) { err401 = e }
    expect((fetchMock.mock.calls[0]?.[0] as Request).headers.get('Authorization')).toBe('Bearer session-token')
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
    expect(parseMemohError(err401)?.code).toBe('auth.unauthorized')

    let err404: unknown
    try { await fetchBoundedBlob({ target: { url: '/bots/{bot_id}/media/{content_hash}', path: { bot_id: 'b1', content_hash: 'x' } }, maxBytes: 1024 }) } catch (e) { err404 = e }
    expect(parseMemohError(err404)).toMatchObject({ code: 'media.asset_not_found', status: 404, requestId: 'req-404' })
    expect(apiErrorStatus(err404)).toBe(404)
  })

  it('propagates caller AbortSignal cancellation and decodes bounded UTF-8 text', async () => {
    const enc = new TextEncoder()
    const caller = new AbortController()
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(new Response(new ReadableStream<Uint8Array>({ pull(c) { c.enqueue(new Uint8Array([1])); caller.abort() } }), { status: 200 }))
      .mockResolvedValueOnce(new Response(chunkStream([enc.encode('hello '), enc.encode('世界')]), { status: 200 }))
      .mockResolvedValueOnce(new Response(chunkStream([enc.encode('0123456789abcdef')]), { status: 200 }))
    setupApiClient({ baseUrl: 'http://example.test/api', fetch: fetchMock })

    await expect(fetchBoundedBlob({ target: { url: '/m' }, maxBytes: 1024, signal: caller.signal })).rejects.toThrow()
    expect(await fetchBoundedText({ target: { url: '/t1' }, maxBytes: 64 })).toEqual({ ok: true, text: 'hello 世界' })
    expect(await fetchBoundedText({ target: { url: '/t2' }, maxBytes: 8 })).toEqual({ ok: false, reason: 'too-large', maxBytes: 8 })
  })
})
