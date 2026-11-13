import { client } from '@memohai/sdk/client'
import { notifyAuthSessionCleared } from './auth-session'

export interface SetupApiClientOptions {
  baseUrl?: string
  fetch?: typeof fetch
  // Called after the access token is cleared on a 401. Hosts decide what to
  // do — usually a router redirect to the login screen.
  onUnauthorized?: () => void
}

interface SdkUrlOptions {
  url: string
  path?: Record<string, unknown>
  query?: Record<string, unknown>
}

export function sdkAuthQuery(): { token?: string } {
  try {
    if (typeof localStorage === 'undefined') return {}
    const token = localStorage.getItem('token')?.trim()
    return token ? { token } : {}
  } catch {
    return {}
  }
}

export function sdkApiUrl(options: SdkUrlOptions): string {
  return client.buildUrl({
    ...client.getConfig(),
    ...options,
  })
}

function browserOrigin(): string {
  const { protocol, host, origin } = window.location
  return origin || `${protocol}//${host}`
}

/**
 * Return the configured HTTP API base as an absolute URL.
 *
 * The Remote Runtime CLI needs the same public base URL as the generated SDK:
 * hosted Web normally uses `/api`, while the desktop shell configures a direct
 * local-server URL. Keeping this derivation next to SDK setup prevents pages
 * from guessing which prefix the current host uses.
 */
export function sdkApiBaseUrl(): string {
  const configured = client.getConfig().baseUrl?.trim() || '/api'
  const normalized = configured.endsWith('/') ? configured : `${configured}/`
  let url: URL
  try {
    url = new URL(normalized)
  } catch {
    url = new URL(normalized, `${browserOrigin()}/`)
  }
  return url.toString().replace(/\/$/, '')
}

export function sdkWebSocketUrl(options: SdkUrlOptions): string {
  const url = new URL(sdkApiUrl(options), browserOrigin())
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  return url.toString()
}

let onUnauthorizedHook: (() => void) | undefined
let configuredBaseFetch: typeof fetch | undefined
let unauthorizedNotified = false

export function sdkRawFetch(): typeof fetch {
  return configuredBaseFetch ?? globalThis.fetch
}

interface ApiFetchContext {
  input: Parameters<typeof fetch>[0]
  init?: Parameters<typeof fetch>[1]
  requestToken: string
}

type ApiFetchResponseHandler = (
  response: Response,
  context: ApiFetchContext,
) => void | Promise<void>

function handleUnauthorized(requestToken?: string) {
  const currentToken = authToken()
  if (currentToken && requestToken && requestToken !== currentToken) return

  try {
    if (currentToken) {
      localStorage.removeItem('token')
    }
  } catch {
  }
  if (!currentToken && unauthorizedNotified) return
  unauthorizedNotified = true
  notifyAuthSessionCleared('unauthorized')
  onUnauthorizedHook?.()
}

function createApiFetch(
  baseFetch: typeof fetch = globalThis.fetch,
  responseHandlers: ApiFetchResponseHandler[] = [handleAuthResponse],
): typeof fetch {
  return async (input, init) => {
    const context: ApiFetchContext = {
      input,
      init,
      requestToken: requestBearerToken(input, init),
    }
    const response = await baseFetch(input, init)
    for (const handler of responseHandlers) {
      await handler(response, context)
    }
    return response
  }
}

function handleAuthResponse(response: Response, context: ApiFetchContext) {
  if (response.status === 401) {
    handleUnauthorized(context.requestToken)
  }
}

function authToken(): string {
  try {
    return localStorage.getItem('token')?.trim() ?? ''
  } catch {
    return ''
  }
}

function requestBearerToken(input: Parameters<typeof fetch>[0], init?: Parameters<typeof fetch>[1]): string {
  const headers = input instanceof Request ? new Headers(input.headers) : new Headers()
  if (init?.headers) {
    new Headers(init.headers).forEach((value, key) => {
      headers.set(key, value)
    })
  }
  const authorization = headers.get('Authorization')?.trim() ?? ''
  const match = /^Bearer\s+(.+)$/i.exec(authorization)
  return match?.[1]?.trim() ?? ''
}

function addAuthorizationHeader(request: Request): Request {
  const token = authToken()
  if (token) {
    unauthorizedNotified = false
    request.headers.set('Authorization', `Bearer ${token}`)
  }
  return request
}

function installAuthRequestInterceptor() {
  if (client.interceptors.request.exists(addAuthorizationHeader)) return
  client.interceptors.request.use(addAuthorizationHeader)
}

/**
 * Configure the SDK client with base URL, auth interceptor, and 401 handling.
 * Call this once at app startup (main.ts).
 */
export function setupApiClient(options: SetupApiClientOptions = {}) {
  const apiBaseUrl = options.baseUrl?.trim() || import.meta.env.VITE_API_URL?.trim() || '/api'
  const agentBaseUrl = import.meta.env.VITE_AGENT_URL?.trim() || '/agent'
  void agentBaseUrl

  onUnauthorizedHook = options.onUnauthorized
  configuredBaseFetch = options.fetch

  client.setConfig({
    baseUrl: apiBaseUrl,
    fetch: createApiFetch(options.fetch),
  })

  installAuthRequestInterceptor()
}
