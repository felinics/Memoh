import { EventEmitter } from 'node:events'
import { Readable } from 'node:stream'
import { gzipSync, deflateSync, brotliCompressSync } from 'node:zlib'
import type { IncomingMessage, RequestOptions } from 'node:http'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { loadRemoteIcon } from './remote-icon'

const mocks = vi.hoisted(() => ({
  lookup: vi.fn(),
  get: vi.fn(),
  HttpAgent: vi.fn(),
  HttpsAgent: vi.fn(),
}))
vi.mock('node:dns', () => ({ lookup: mocks.lookup }))
vi.mock('node:http', () => ({ get: mocks.get, Agent: mocks.HttpAgent }))
vi.mock('node:https', () => ({ get: mocks.get, Agent: mocks.HttpsAgent }))

function response(body: string | Buffer = '<svg/>', headers: IncomingMessage['headers'] = { 'content-type': 'image/svg+xml' }, statusCode = 200) {
  return Object.assign(Readable.from([Buffer.from(body)]), { headers, statusCode }) as unknown as IncomingMessage
}

const connect = vi.fn()
let responses: IncomingMessage[]
beforeEach(() => {
  vi.useRealTimers()
  vi.clearAllMocks()
  responses = [response()]
  mocks.lookup.mockImplementation((_hostname, _options, callback) => {
    callback(null, [{ address: '93.184.216.34', family: 4 }])
  })
  mocks.get.mockImplementation((url: URL, options: RequestOptions, callback: (value: IncomingMessage) => void) => {
    const request = new EventEmitter()
    queueMicrotask(() => {
      // Emulate Node's socket lookup. No second resolution occurs between this
      // callback and connection; record exactly the address supplied to Node.
      options.lookup!(url.hostname, { all: true }, (error, addresses) => {
        if (error) request.emit('error', error)
        else {
          connect(addresses)
          const next = responses.shift()
          if (next) callback(next)
        }
      })
    })
    options.signal?.addEventListener('abort', () => request.emit('error', new Error('aborted')), { once: true })
    return request
  })
})
afterEach(() => vi.useRealTimers())

it('returns public image bytes using a stateless keep-alive pool without authenticated headers', async () => {
  expect(await loadRemoteIcon('https://example.com/icon.svg')).toBe('data:image/svg+xml;base64,PHN2Zy8+')
  expect(mocks.get.mock.calls[0]![0].hostname).toBe('example.com')
  expect(mocks.get.mock.calls[0]![1]).toEqual({
    agent: expect.any(Object), lookup: expect.any(Function), signal: expect.any(AbortSignal),
    headers: { 'Accept-Encoding': 'gzip, deflate, br' },
  })
  expect(connect).toHaveBeenCalledWith([{ address: '93.184.216.34', family: 4 }])
  expect(mocks.lookup).toHaveBeenCalledTimes(1)
})

const privateAddresses = [
  '0.0.0.0', '10.0.0.1', '100.64.0.1', '127.0.0.1', '169.254.169.254', '172.16.0.1',
  '192.0.0.1', '192.0.2.1', '192.88.99.1', '192.168.1.1', '198.18.0.1',
  '198.51.100.1', '203.0.113.1', '224.0.0.1', '255.255.255.255',
  '::', '::1', '::ffff:127.0.0.1', '64:ff9b::a00:1', '100::1', 'fc00::1',
  'fe80::1', 'ff02::1', '2001::1', '2001:db8::1', '2002:7f00:1::', '3fff::1',
]
it.each(privateAddresses)('rejects non-public literal %s without opening a request', async (address) => {
  await expect(loadRemoteIcon(`http://${address.includes(':') ? `[${address}]` : address}/icon`)).rejects.toThrow()
  expect(mocks.get).not.toHaveBeenCalled()
})
it.each(['file:///etc/passwd', 'data:image/png;base64,AA==', 'https://user:pass@example.com/icon',
  'http://2130706433/', 'http://0x7f000001/', 'http://127.1/', null])(
  'rejects invalid and normalized loopback URL %s', async (url) => {
    await expect(loadRemoteIcon(url)).rejects.toThrow()
    expect(mocks.get).not.toHaveBeenCalled()
  },
)
it.each(privateAddresses)('rejects DNS answer %s before connecting', async (address) => {
  mocks.lookup.mockImplementation((_hostname, _options, callback) => callback(null, [
    { address, family: address.includes(':') ? 6 : 4 },
  ]))
  await expect(loadRemoteIcon('https://example.com/icon')).rejects.toThrow('public')
  expect(connect).not.toHaveBeenCalled()
})
it('rejects mixed public and private DNS answers', async () => {
  mocks.lookup.mockImplementation((_hostname, _options, callback) => callback(null, [
    { address: '93.184.216.34', family: 4 }, { address: '::1', family: 6 },
  ]))
  await expect(loadRemoteIcon('https://example.com/icon')).rejects.toThrow('public')
  expect(connect).not.toHaveBeenCalled()
})
it('accepts public IPv6 DNS addresses', async () => {
  mocks.lookup.mockImplementation((_hostname, _options, callback) => callback(null, [
    { address: '2606:4700:4700::1111', family: 6 },
  ]))
  await expect(loadRemoteIcon('https://example.com/icon')).resolves.toContain('data:image/')
})
it('rejects private redirects before a second request', async () => {
  responses = [response('', { location: 'http://127.0.0.1/' }, 302)]
  await expect(loadRemoteIcon('https://example.com/icon')).rejects.toThrow()
  expect(mocks.get).toHaveBeenCalledTimes(1)
})
it('validates each redirect DNS result even for the same hostname', async () => {
  responses = [response('', { location: '/next' }, 302), response()]
  mocks.lookup.mockImplementationOnce((_hostname, _options, callback) => callback(null, [
    { address: '93.184.216.34', family: 4 },
  ])).mockImplementationOnce((_hostname, _options, callback) => callback(null, [
    { address: '127.0.0.1', family: 4 },
  ]))
  await expect(loadRemoteIcon('https://example.com/icon')).rejects.toThrow('public')
  expect(connect).toHaveBeenCalledTimes(1)
})
it('follows relative public redirects and caps redirect loops', async () => {
  responses = [response('', { location: '/next' }, 302), response()]
  await expect(loadRemoteIcon('https://example.com/icon')).resolves.toContain('data:image/')
  expect(mocks.get.mock.calls[1]![0].href).toBe('https://example.com/next')
  responses = Array.from({ length: 6 }, () => response('', { location: '/loop' }, 302))
  await expect(loadRemoteIcon('https://example.com/icon')).rejects.toThrow('redirect')
})
it.each([
  () => response('bad', {}, 403),
  () => response('<html/>', { 'content-type': 'text/html' }),
  () => response(''),
  () => response('x', { 'content-type': 'image/png', 'content-length': '524289' }),
  () => response('x'.repeat(524289)),
  () => response('compressed', { 'content-type': 'image/png', 'content-encoding': 'gzip' }),
])('rejects invalid or oversized responses and destroys the stream', async (create) => {
  const stream = create()
  responses = [stream]
  await expect(loadRemoteIcon('https://example.com/icon')).rejects.toThrow()
  expect(stream.destroyed).toBe(true)
})
it('bounds concurrent downloads and releases slots after failures', async () => {
  const pending: Array<(value: IncomingMessage) => void> = []
  mocks.get.mockImplementation((_url, _options, callback) => {
    pending.push(callback)
    return new EventEmitter()
  })
  const results = Promise.allSettled(Array.from({ length: 8 }, (_, i) => loadRemoteIcon(`https://example.com/${i}`)))
  expect(mocks.get).toHaveBeenCalledTimes(6)
  for (let i = 0; i < 8; i++) {
    await vi.waitFor(() => expect(pending[i]).toBeDefined())
    pending[i]!(response('bad', {}, 500))
  }
  expect((await results).every(result => result.status === 'rejected')).toBe(true)
})
it('aborts stalled requests at the shared deadline and releases the slot', async () => {
  vi.useFakeTimers()
  responses = []
  const result = expect(loadRemoteIcon('https://example.com/icon')).rejects.toThrow('aborted')
  await vi.advanceTimersByTimeAsync(15_000)
  await result
  responses = [response()]
  await expect(loadRemoteIcon('https://example.com/icon')).resolves.toContain('data:image/')
})

it.each([
  ['gzip', gzipSync], ['deflate', deflateSync], ['br', brotliCompressSync],
] as const)('decodes %s images and applies the limit to decoded bytes', async (encoding, compress) => {
  responses = [response(compress('<svg/>'), { 'content-type': 'image/svg+xml', 'content-encoding': encoding })]
  await expect(loadRemoteIcon('https://example.com/icon')).resolves.toBe('data:image/svg+xml;base64,PHN2Zy8+')
  responses = [response(compress('x'.repeat(524289)), { 'content-type': 'image/png', 'content-encoding': encoding })]
  await expect(loadRemoteIcon('https://example.com/icon')).rejects.toThrow('Icon too large')
})
