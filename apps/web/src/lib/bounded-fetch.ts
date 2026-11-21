import { client } from '@memohai/sdk/client'
import { sdkRawFetch } from './api-client'

export type BoundedFetchTarget =
  | { url: string, path?: Record<string, unknown>, query?: Record<string, unknown> }
  | { src: string }
export type BoundedBlobResult = { ok: true, blob: Blob } | { ok: false, reason: 'too-large', maxBytes: number }
export type BoundedTextResult = { ok: true, text: string } | { ok: false, reason: 'too-large', maxBytes: number }

function toHttpError(payload: unknown, response: Response): Record<string, unknown> {
  if (payload && typeof payload === 'object') {
    const rec = payload as Record<string, unknown>
    return { ...rec, status: typeof rec.status === 'number' ? rec.status : response.status, response }
  }
  return { detail: typeof payload === 'string' ? payload : '', status: response.status, response }
}

function resolveApiSubpath(rawSrc: string): string | null {
  const trimmed = rawSrc.trim()
  if (!trimmed || /^(?:blob:|data:)/i.test(trimmed)) return null
  const origin = typeof window !== 'undefined' && window.location?.origin && window.location.origin !== 'null' ? window.location.origin : 'http://localhost'
  try {
    const apiBase = new URL(client.getConfig().baseUrl?.trim() || '/api', `${origin}/`)
    const target = new URL(trimmed, `${apiBase.origin}/`)
    const basePath = apiBase.pathname.replace(/\/+$/, '')
    if (target.origin !== apiBase.origin) return null
    if (!basePath || basePath === '/') return `${target.pathname}${target.search}`
    if (target.pathname !== basePath && !target.pathname.startsWith(`${basePath}/`)) return null
    return `${target.pathname.slice(basePath.length) || '/'}${target.search}`
  } catch {
    return null
  }
}

async function openSdkStream(req: { url: string, path?: Record<string, unknown>, query?: Record<string, unknown> }, signal: AbortSignal): Promise<Response> {
  const res = await client.get({ ...req, parseAs: 'stream', signal, throwOnError: false })
  if (!res.response) throw res.error ?? new DOMException('The operation was aborted.', 'AbortError')
  if (!res.response.ok) throw toHttpError(res.error, res.response)
  return res.response
}

async function openResponse(target: BoundedFetchTarget, signal: AbortSignal): Promise<Response> {
  if ('url' in target) return await openSdkStream(target, signal)
  const subpath = resolveApiSubpath(target.src)
  if (subpath) return await openSdkStream({ url: subpath }, signal)
  const response = await sdkRawFetch()(target.src, /^(?:blob:|data:)/i.test(target.src.trim()) ? { signal } : { credentials: 'omit', signal })
  if (!response.ok) {
    const text = await response.text()
    let parsed: unknown = text
    try { if (text) parsed = JSON.parse(text) } catch { parsed = text }
    throw toHttpError(parsed, response)
  }
  return response
}

async function readBoundedChunks(target: BoundedFetchTarget, maxBytes: number, callerSignal?: AbortSignal) {
  const makeAbort = () => callerSignal?.reason ?? new DOMException('The operation was aborted.', 'AbortError')
  if (callerSignal?.aborted) throw makeAbort()
  const controller = new AbortController()
  let abortedForSize = false
  const onAbort = () => controller.abort(callerSignal?.reason)
  callerSignal?.addEventListener('abort', onAbort, { once: true })
  const failTooLarge = async (cancel?: () => Promise<void>) => {
    abortedForSize = true
    await cancel?.().catch(() => {})
    controller.abort()
    return { ok: false as const, reason: 'too-large' as const, maxBytes }
  }

  try {
    const response = await openResponse(target, controller.signal)
    if (callerSignal?.aborted) throw makeAbort()
    const contentType = response.headers.get('content-type')?.trim() ?? ''
    const rawLen = response.headers.get('content-length')?.trim()
    const declaredLen = rawLen && /^\d+$/.test(rawLen) ? Number(rawLen) : null
    if (declaredLen !== null && Number.isSafeInteger(declaredLen) && declaredLen > maxBytes) return await failTooLarge(() => response.body?.cancel() ?? Promise.resolve())
    if (!response.body) return { ok: true as const, chunks: [] as Uint8Array[], contentType }

    const reader = response.body.getReader()
    const chunks: Uint8Array[] = []
    let total = 0
    try {
      for (;;) {
        const { done, value } = await reader.read()
        if (callerSignal?.aborted) { await reader.cancel().catch(() => {}); throw makeAbort() }
        if (done) break
        if (!value?.byteLength) continue
        total += value.byteLength
        if (total > maxBytes) return await failTooLarge(() => reader.cancel())
        chunks.push(value)
      }
    } catch (err) {
      if (abortedForSize && !callerSignal?.aborted) return { ok: false as const, reason: 'too-large' as const, maxBytes }
      throw err
    }
    return { ok: true as const, chunks, contentType }
  } finally {
    callerSignal?.removeEventListener('abort', onAbort)
  }
}

export async function fetchBoundedBlob(options: { target: BoundedFetchTarget, maxBytes: number, mimeType?: string, signal?: AbortSignal }): Promise<BoundedBlobResult> {
  const res = await readBoundedChunks(options.target, options.maxBytes, options.signal)
  return res.ok ? { ok: true, blob: new Blob(res.chunks as BlobPart[], { type: options.mimeType?.trim() || res.contentType || 'application/octet-stream' }) } : res
}

export async function fetchBoundedText(options: { target: BoundedFetchTarget, maxBytes: number, signal?: AbortSignal }): Promise<BoundedTextResult> {
  const res = await readBoundedChunks(options.target, options.maxBytes, options.signal)
  const dec = new TextDecoder('utf-8')
  return res.ok ? { ok: true, text: res.chunks.map(c => dec.decode(c, { stream: true })).join('') + dec.decode() } : res
}
