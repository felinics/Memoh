import { createServer } from 'node:http'
import { timingSafeEqual } from 'node:crypto'
import { beginChatGptAuthorization, completeChatGptAuthorization, retainChatGptRegistration } from '@memohai/sdk'
import type { ChatGPTConnectRequest, ChatGPTConnectResult } from '../shared/chatgpt-auth'

const invalid = 'chatgpt.authorization_invalid'
const unavailable = 'chatgpt.unavailable'
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
export function normalizeChatGPTRequest(raw: unknown): ChatGPTConnectRequest | null {
  if (!raw || typeof raw !== 'object') return null
  const request = raw as Partial<ChatGPTConnectRequest>
  if (typeof request.providerId !== 'string' || !uuidPattern.test(request.providerId)
    || typeof request.token !== 'string' || !request.token || request.token.length > 16384) return null
  return { providerId: request.providerId, token: request.token }
}
function publicCode(error: unknown): string {
  if (error && typeof error === 'object' && 'code' in error && typeof error.code === 'string'
    && /^chatgpt\.[a-z_]+$/.test(error.code)) return error.code
  return unavailable
}
export function secureServer(baseUrl: string): boolean {
  try {
    const url = new URL(baseUrl)
    return !url.username && !url.password && (url.protocol === 'https:'
      || (url.protocol === 'http:' && ['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname)))
  } catch { return false }
}
function equalState(a: string, b: string): boolean {
  const aa = Buffer.from(a)
  const bb = Buffer.from(b)
  return aa.length === bb.length && timingSafeEqual(aa, bb)
}

// A local listener is confined to the authorization attempt. There is no local
// Memoh server and no persistent OpenAI credential file on the desktop.
export async function connectChatGPT(options: {
  baseUrl: string
  request: ChatGPTConnectRequest
  signal: AbortSignal
  openExternal: (url: string) => Promise<void>
  currentBaseUrl: () => string
}): Promise<ChatGPTConnectResult> {
  if (!secureServer(options.baseUrl)) return { ok: false, code: 'chatgpt.secure_transport_required' }
  const controller = new AbortController()
  const abort = () => controller.abort()
  options.signal.addEventListener('abort', abort, { once: true })
  if (options.signal.aborted) abort()
  const timeout = setTimeout(abort, 10 * 60 * 1000)
  let expectedState = ''
  type Callback = { code: string, clientId: string } | null
  let resolveCallback!: (value: Callback) => void
  const callback = new Promise<Callback>((resolve) => { resolveCallback = resolve })
  const server = createServer((req, res) => {
    res.setHeader('Cache-Control', 'no-store')
    res.setHeader('Content-Type', 'text/plain; charset=utf-8')
    const url = new URL(req.url || '/', 'http://127.0.0.1')
    if (req.method !== 'GET' || url.pathname !== '/auth/callback') { res.writeHead(404); res.end(); return }
    if (!expectedState || !equalState(url.searchParams.get('state') || '', expectedState)) {
      res.writeHead(400); res.end('Invalid authorization state.'); return
    }
    const code = url.searchParams.get('code') || ''
    if (!code || url.searchParams.has('error')) {
      res.writeHead(400); res.end('Authorization was not completed. Return to Memoh.'); resolveCallback(null); return
    }
    res.end('Authorization received. Return to Memoh to finish connecting.')
    resolveCallback({ code, clientId: url.searchParams.get('client_id') || '' })
  })
  const cancel = () => { resolveCallback(null); server.closeAllConnections(); server.close() }
  controller.signal.addEventListener('abort', cancel, { once: true })
  try {
    if (controller.signal.aborted) return { ok: false, code: 'chatgpt.authorization_cancelled' }
    await new Promise<void>((resolve, reject) => {
      server.once('error', reject)
      server.listen(0, '127.0.0.1', resolve)
    })
    const address = server.address()
    if (!address || typeof address === 'string') return { ok: false, code: unavailable }
    const redirectUri = `http://127.0.0.1:${address.port}/auth/callback`
    const shared = {
      baseUrl: options.baseUrl,
      headers: { Authorization: `Bearer ${options.request.token}` },
      path: { id: options.request.providerId },
      signal: controller.signal,
      cache: 'no-store' as const,
      redirect: 'error' as const,
    }
    const begin = await beginChatGptAuthorization({ ...shared, body: { redirect_uri: redirectUri } })
    const grant = begin.data
    if (!grant?.authorization_url || !grant.state || !grant.code_verifier || !grant.client_id) {
      return { ok: false, code: publicCode(begin.error) }
    }
    const authorizeUrl = new URL(grant.authorization_url)
    if (authorizeUrl.origin !== 'https://auth.openai.com' || authorizeUrl.pathname !== '/api/accounts/authorize'
      || authorizeUrl.searchParams.get('state') !== grant.state
      || authorizeUrl.searchParams.get('redirect_uri') !== redirectUri) return { ok: false, code: invalid }
    expectedState = grant.state
    await options.openExternal(authorizeUrl.toString())
    const returned = await callback
    if (!returned) return { ok: false, code: controller.signal.aborted ? 'chatgpt.authorization_cancelled' : invalid }
    const clientId = returned.clientId || (grant.client_id === 'dynamic_agent_client' ? '' : grant.client_id)
    if (!clientId.startsWith('oaiapp_') || (grant.client_id !== 'dynamic_agent_client' && clientId !== grant.client_id)) {
      return { ok: false, code: invalid }
    }
    if (options.currentBaseUrl() !== options.baseUrl) return { ok: false, code: invalid }
    if (grant.client_id === 'dynamic_agent_client') {
      // Save the issued client before consuming its authorization code.
      const retained = await retainChatGptRegistration({ ...shared,
        body: { state: grant.state, client_id: clientId },
      })
      if (!retained.response?.ok) return { ok: false, code: publicCode(retained.error) }
    }
    // Code exchange happens on the machine hosting the local callback.
    const exchanged = await fetch('https://auth.openai.com/api/accounts/oauth/token', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ grant_type: 'authorization_code', client_id: clientId,
        code: returned.code, code_verifier: grant.code_verifier, redirect_uri: redirectUri,
        resource: 'https://api.openai.com/v1' }),
      signal: controller.signal,
      redirect: 'error',
    })
    if (!exchanged.ok) return { ok: false, code: invalid }
    const tokens = await exchanged.json()
    if (options.currentBaseUrl() !== options.baseUrl) return { ok: false, code: invalid }
    const completed = await completeChatGptAuthorization({ ...shared,
      body: { state: grant.state, client_id: clientId, tokens },
    })
    if (!completed.data) return { ok: false, code: publicCode(completed.error) }
    return { ok: true }
  } catch {
    // SDK/fetch diagnostics may carry credential-bearing request data.
    return { ok: false, code: controller.signal.aborted ? 'chatgpt.authorization_cancelled' : unavailable }
  } finally {
    clearTimeout(timeout)
    options.signal.removeEventListener('abort', abort)
    controller.signal.removeEventListener('abort', cancel)
    server.closeAllConnections()
    server.close()
  }
}
