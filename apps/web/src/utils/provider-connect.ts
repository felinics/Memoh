// Generic provider deep-link contract (`/providers/connect#payload=...`).
// Any aggregator or gateway (New API, one-api forks, …) can hand Memoh a
// prefilled provider by encoding this payload as base64url in the URL
// fragment. The fragment never reaches the server, so the key stays out of
// access logs; the page always asks for user confirmation before saving.

export const PROVIDER_CONNECT_KIND = 'memoh-provider-import'

export interface ProviderConnectPayload {
  name: string
  baseUrl: string
  apiKey: string
  clientType: string
  template?: string
}

export type ProviderConnectError =
  | 'missing'
  | 'decode'
  | 'kind'
  | 'version'
  | 'baseUrl'
  | 'apiKey'
  | 'clientType'

// Mirrors models.IsLLMClientType on the backend; connect links only make
// sense for key-based chat providers (OAuth client types can't be prefilled).
const ALLOWED_CLIENT_TYPES = new Set([
  'openai-completions',
  'openai-responses',
  'anthropic-messages',
  'google-generative-ai',
])

export class PayloadError extends Error {
  readonly reason: ProviderConnectError

  constructor(reason: ProviderConnectError) {
    super(reason)
    this.reason = reason
  }
}

function base64UrlDecode(input: string): string {
  const b64 = input.replace(/-/g, '+').replace(/_/g, '/')
  const bytes = Uint8Array.from(atob(b64), c => c.charCodeAt(0))
  return new TextDecoder().decode(bytes)
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

export function parseProviderConnectPayload(encoded: string): ProviderConnectPayload {
  const trimmed = encoded.trim()
  if (!trimmed) throw new PayloadError('missing')

  let raw: unknown
  try {
    raw = JSON.parse(base64UrlDecode(trimmed))
  } catch {
    throw new PayloadError('decode')
  }
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) throw new PayloadError('decode')
  const obj = raw as Record<string, unknown>

  if (obj.kind !== PROVIDER_CONNECT_KIND) throw new PayloadError('kind')
  if (obj.v !== 1) throw new PayloadError('version')

  const baseUrl = optionalString(obj.base_url)
  if (!baseUrl) throw new PayloadError('baseUrl')

  let host = ''
  try {
    const url = new URL(baseUrl)
    if (url.protocol !== 'http:' && url.protocol !== 'https:') throw new PayloadError('baseUrl')
    host = url.host
  } catch (err) {
    if (err instanceof PayloadError) throw err
    throw new PayloadError('baseUrl')
  }

  const apiKey = optionalString(obj.api_key)
  if (!apiKey) throw new PayloadError('apiKey')

  const clientType = optionalString(obj.client_type) ?? 'openai-completions'
  if (!ALLOWED_CLIENT_TYPES.has(clientType)) throw new PayloadError('clientType')

  return {
    name: optionalString(obj.name) ?? host,
    baseUrl: baseUrl.replace(/\/+$/, ''),
    apiKey,
    clientType,
    template: optionalString(obj.template),
  }
}

/** Mask all but the last 4 characters for display on the confirm screen. */
export function maskApiKey(key: string): string {
  return key.length <= 4 ? '••••' : `••••${key.slice(-4)}`
}
