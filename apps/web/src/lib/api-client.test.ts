import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('./auth-session', () => ({
  notifyAuthSessionCleared: vi.fn(),
}))

let client: typeof import('@memohai/sdk/client')['client']
let setupApiClient: typeof import('./api-client')['setupApiClient']
let sdkApiBaseUrl: typeof import('./api-client')['sdkApiBaseUrl']
let mockedNotifyAuthSessionCleared: ReturnType<typeof vi.fn>

function stubLocalStorage() {
  const store = new Map<string, string>()
  vi.stubGlobal('localStorage', {
    getItem: vi.fn((key: string) => store.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => {
      store.set(key, value)
    }),
    removeItem: vi.fn((key: string) => {
      store.delete(key)
    }),
    clear: vi.fn(() => {
      store.clear()
    }),
  })
}

async function drain<T>(stream: AsyncGenerator<T, unknown, unknown>) {
  for await (const _event of stream) {
  }
}

describe('setupApiClient auth handling', () => {
  beforeEach(async () => {
    vi.resetModules()
    stubLocalStorage()
    localStorage.clear()
    const sdkClientModule = await import('@memohai/sdk/client')
    const apiClientModule = await import('./api-client')
    const authSession = await import('./auth-session')
    client = sdkClientModule.client
    setupApiClient = apiClientModule.setupApiClient
    sdkApiBaseUrl = apiClientModule.sdkApiBaseUrl
    mockedNotifyAuthSessionCleared = vi.mocked(authSession.notifyAuthSessionCleared) as unknown as ReturnType<typeof vi.fn>
    mockedNotifyAuthSessionCleared.mockClear()
    client.interceptors.request.clear()
    client.interceptors.response.clear()
    client.interceptors.error.clear()
  })

  afterEach(() => {
    client.interceptors.request.clear()
    client.interceptors.response.clear()
    client.interceptors.error.clear()
    client.setConfig({ baseUrl: '/api', fetch: undefined })
    vi.unstubAllGlobals()
  })

  it('runs the same unauthorized flow for SSE 401 responses', async () => {
    localStorage.setItem('token', 'stale-token')
    const onUnauthorized = vi.fn()
    const fetchMock = vi.fn<typeof fetch>(async () => new Response('', {
      status: 401,
      statusText: 'Unauthorized',
    }))
    const onSseError = vi.fn()

    setupApiClient({
      baseUrl: 'http://example.test',
      fetch: fetchMock as unknown as typeof fetch,
      onUnauthorized,
    })

    const result = await client.sse.get({
      url: '/events',
      onSseError,
      sseMaxRetryAttempts: 1,
    })

    await drain(result.stream)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const request = fetchMock.mock.calls[0]?.[0] as Request
    expect(request.headers.get('Authorization')).toBe('Bearer stale-token')
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem('token')).toBeNull()
    expect(onSseError).toHaveBeenCalledTimes(1)
  })

  it('runs the same unauthorized flow for REST 401 responses', async () => {
    localStorage.setItem('token', 'stale-token')
    const onUnauthorized = vi.fn()
    const fetchMock = vi.fn(async () => new Response('', {
      status: 401,
      statusText: 'Unauthorized',
    }))

    setupApiClient({
      baseUrl: 'http://example.test',
      fetch: fetchMock as unknown as typeof fetch,
      onUnauthorized,
    })

    await client.get({ url: '/users/me' })

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem('token')).toBeNull()
  })

  it('runs the unauthorized flow before a REST 401 is thrown', async () => {
    localStorage.setItem('token', 'stale-token')
    const onUnauthorized = vi.fn()
    const fetchMock = vi.fn(async () => new Response(
      JSON.stringify({ message: 'expired' }),
      {
        status: 401,
        statusText: 'Unauthorized',
        headers: { 'Content-Type': 'application/json' },
      },
    ))

    setupApiClient({
      baseUrl: 'http://example.test',
      fetch: fetchMock as unknown as typeof fetch,
      onUnauthorized,
    })

    await expect(client.get({
      url: '/users/me',
      throwOnError: true,
    })).rejects.toEqual({ message: 'expired' })

    expect(onUnauthorized).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem('token')).toBeNull()
  })

  it('returns network errors without inventing a response', async () => {
    const networkError = new TypeError('network unavailable')
    const fetchMock = vi.fn(async () => {
      throw networkError
    })

    setupApiClient({
      baseUrl: 'http://example.test',
      fetch: fetchMock as unknown as typeof fetch,
    })

    const result = await client.get({ url: '/users/me' })

    expect(result.error).toBe(networkError)
    expect(result.request).toBeInstanceOf(Request)
    expect(result.response).toBeUndefined()
  })

  it('normalizes stored token whitespace before comparing 401 request tokens', async () => {
    localStorage.setItem('token', '  stale-token  ')
    const onUnauthorized = vi.fn()
    const fetchMock = vi.fn<typeof fetch>(async () => new Response('', {
      status: 401,
      statusText: 'Unauthorized',
    }))

    setupApiClient({
      baseUrl: 'http://example.test',
      fetch: fetchMock as unknown as typeof fetch,
      onUnauthorized,
    })

    await client.get({ url: '/users/me' })

    const request = fetchMock.mock.calls[0]?.[0] as Request
    expect(request.headers.get('Authorization')).toBe('Bearer stale-token')
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem('token')).toBeNull()
  })

  it('does not let a stale request 401 clear a newer login token', async () => {
    localStorage.setItem('token', 'old-token')
    const onUnauthorized = vi.fn()
    const fetchMock = vi.fn<typeof fetch>(async () => {
      localStorage.setItem('token', 'new-token')
      return new Response('', {
        status: 401,
        statusText: 'Unauthorized',
      })
    })

    setupApiClient({
      baseUrl: 'http://example.test',
      fetch: fetchMock as unknown as typeof fetch,
      onUnauthorized,
    })

    await client.get({ url: '/users/me' })

    const request = fetchMock.mock.calls[0]?.[0] as Request
    expect(request.headers.get('Authorization')).toBe('Bearer old-token')
    expect(onUnauthorized).not.toHaveBeenCalled()
    expect(mockedNotifyAuthSessionCleared).not.toHaveBeenCalled()
    expect(localStorage.getItem('token')).toBe('new-token')
  })

  it('handles a 401 when the current session has a token but the request has no bearer token', async () => {
    localStorage.setItem('token', 'current-token')
    const onUnauthorized = vi.fn()
    const fetchMock = vi.fn<typeof fetch>(async () => new Response('', {
      status: 401,
      statusText: 'Unauthorized',
    }))

    setupApiClient({
      baseUrl: 'http://example.test',
      fetch: fetchMock as unknown as typeof fetch,
      onUnauthorized,
    })
    client.interceptors.request.use((request) => {
      request.headers.delete('Authorization')
      return request
    })

    await client.get({ url: '/users/me' })

    const request = fetchMock.mock.calls[0]?.[0] as Request
    expect(request.headers.get('Authorization')).toBeNull()
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem('token')).toBeNull()
  })

  it('leaves the token alone on non-401 responses', async () => {
    localStorage.setItem('token', 'valid-token')
    const fetchMock = vi.fn(async () => new Response('{}', { status: 200 }))

    setupApiClient({
      baseUrl: 'http://example.test',
      fetch: fetchMock as unknown as typeof fetch,
    })

    await client.get({ url: '/users/me' })

    expect(localStorage.getItem('token')).toBe('valid-token')
    expect(mockedNotifyAuthSessionCleared).not.toHaveBeenCalled()
  })

  it('notifies only once for repeated 401 responses after the token is cleared', async () => {
    localStorage.setItem('token', 'stale-token')
    const onUnauthorized = vi.fn()
    const fetchMock = vi.fn(async () => new Response('', {
      status: 401,
      statusText: 'Unauthorized',
    }))

    setupApiClient({
      baseUrl: 'http://example.test',
      fetch: fetchMock as unknown as typeof fetch,
      onUnauthorized,
    })

    await client.get({ url: '/first' })
    await client.get({ url: '/second' })

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
    expect(mockedNotifyAuthSessionCleared).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem('token')).toBeNull()
  })

  it('does not add duplicate request interceptors across repeated setup calls', async () => {
    localStorage.setItem('token', 'valid-token')
    const fetchMock = vi.fn<typeof fetch>(async () => new Response('', { status: 204 }))

    setupApiClient({
      baseUrl: 'http://example.test',
      fetch: fetchMock as unknown as typeof fetch,
    })
    setupApiClient({
      baseUrl: 'http://example.test',
      fetch: fetchMock as unknown as typeof fetch,
    })

    await client.get({ url: '/users/me' })

    expect(client.interceptors.request.fns.filter(Boolean)).toHaveLength(1)
    const request = fetchMock.mock.calls[0]?.[0] as Request
    expect(request.headers.get('Authorization')).toBe('Bearer valid-token')
  })

  it('still injects the bearer token through request interceptors', async () => {
    localStorage.setItem('token', 'valid-token')
    const fetchMock = vi.fn<typeof fetch>(async () => new Response('', { status: 204 }))

    setupApiClient({
      baseUrl: 'http://example.test',
      fetch: fetchMock as unknown as typeof fetch,
    })

    await client.get({ url: '/users/me' })

    const request = fetchMock.mock.calls[0]?.[0] as Request
    expect(request.headers.get('Authorization')).toBe('Bearer valid-token')
  })

  it('resolves the browser API prefix for Remote Runtime commands', () => {
    vi.stubGlobal('window', {
      location: {
        protocol: 'https:',
        host: 'memoh.example.test',
        origin: 'https://memoh.example.test',
      },
    })
    setupApiClient({ baseUrl: '/api' })

    expect(sdkApiBaseUrl()).toBe('https://memoh.example.test/api')
  })

  it('keeps the desktop direct-server API base for Remote Runtime commands', () => {
    vi.stubGlobal('window', {
      location: {
        protocol: 'app:',
        host: 'memoh.local',
        origin: 'null',
      },
    })
    setupApiClient({ baseUrl: 'http://127.0.0.1:18731/' })

    expect(sdkApiBaseUrl()).toBe('http://127.0.0.1:18731')
  })
})
