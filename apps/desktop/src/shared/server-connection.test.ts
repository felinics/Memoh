import { describe, expect, it, vi } from 'vitest'
import {
  normalizeBaseUrl,
  normalizeServerInput,
  probeServerBaseUrl,
  resolveDesktopBaseUrl,
} from './server-connection'

describe('desktop server connection', () => {
  it('normalizes local and remote server addresses', () => {
    expect(normalizeBaseUrl('localhost:18080/')).toBe('http://localhost:18080/api')
    expect(normalizeBaseUrl('memoh.example.com')).toBe('https://memoh.example.com/api')
    expect(normalizeBaseUrl('https://memoh.example.com/path?query=1#hash'))
      .toBe('https://memoh.example.com/path/api')
  })

  it('lets explicit launch configuration override a persisted profile', () => {
    expect(resolveDesktopBaseUrl({
      proxy: 'http://localhost:18080',
      profile: 'http://141.98.75.24:28083',
      fallback: 'http://localhost:8080',
    })).toBe('http://localhost:18080/api')
  })

  it('uses the persisted profile when no launch override is present', () => {
    expect(resolveDesktopBaseUrl({
      profile: 'https://memoh.example.com',
      fallback: 'http://localhost:8080',
    })).toBe('https://memoh.example.com/api')
  })

  it('keeps a verified in-app server switch for the rest of the process', () => {
    expect(resolveDesktopBaseUrl({
      session: 'http://localhost:18080',
      proxy: 'http://localhost:18081',
      profile: 'http://localhost:18080',
      fallback: 'http://localhost:8080',
    })).toBe('http://localhost:18080/api')
  })

  it('rejects empty, malformed, and unsupported addresses', () => {
    expect(normalizeServerInput('')).toMatchObject({ ok: false, error: 'required' })
    expect(normalizeServerInput('https://')).toMatchObject({ ok: false, error: 'invalid-url' })
    expect(normalizeServerInput('ftp://memoh.example.com')).toMatchObject({
      ok: false,
      error: 'unsupported-protocol',
    })
  })

  it('accepts only a successful Memoh ping response', async () => {
    const success = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(JSON.stringify({ status: 'ok', version: '0.15.0' }), { status: 200 }),
    )
    await expect(probeServerBaseUrl('https://memoh.example.com/api', success))
      .resolves.toEqual({ ok: true, baseUrl: 'https://memoh.example.com/api' })
    expect(success).toHaveBeenCalledWith('https://memoh.example.com/api/ping', expect.objectContaining({
      headers: { Accept: 'application/json' },
    }))

    const notFound = vi.fn<typeof fetch>().mockResolvedValue(new Response('', { status: 404 }))
    await expect(probeServerBaseUrl('https://memoh.example.com', notFound))
      .resolves.toMatchObject({ ok: false, error: 'http-error', status: 404 })

    const unrelated = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(JSON.stringify({ status: 'healthy' }), { status: 200 }),
    )
    await expect(probeServerBaseUrl('https://memoh.example.com', unrelated))
      .resolves.toMatchObject({ ok: false, error: 'invalid-response' })
  })

  it('reports request failures as unreachable', async () => {
    const request = vi.fn<typeof fetch>().mockRejectedValue(new TypeError('fetch failed'))
    await expect(probeServerBaseUrl('https://memoh.example.com', request))
      .resolves.toMatchObject({ ok: false, error: 'unreachable' })
  })

  it('aborts slow requests and reports a timeout', async () => {
    vi.useFakeTimers()
    try {
      const request = vi.fn<typeof fetch>((_input, init) => new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => {
          reject(new DOMException('The operation was aborted', 'AbortError'))
        }, { once: true })
      }))
      const result = probeServerBaseUrl('https://memoh.example.com', request, 5000)

      await vi.advanceTimersByTimeAsync(5000)

      await expect(result).resolves.toMatchObject({ ok: false, error: 'timeout' })
    } finally {
      vi.useRealTimers()
    }
  })
})
