import { lookup } from 'node:dns'
import { Agent as HttpAgent, get as httpGet } from 'node:http'
import { Agent as HttpsAgent, get as httpsGet } from 'node:https'
import { BlockList, isIP } from 'node:net'
import { pipeline } from 'node:stream'
import { createBrotliDecompress, createGunzip, createInflate } from 'node:zlib'
import type { Readable } from 'node:stream'
import type { IncomingMessage, RequestOptions } from 'node:http'

// Only globally routable artwork may cross this privileged IPC boundary.
const blockedIPv4 = new BlockList()
for (const [address, prefix] of [
  ['0.0.0.0', 8], ['10.0.0.0', 8], ['100.64.0.0', 10], ['127.0.0.0', 8],
  ['169.254.0.0', 16], ['172.16.0.0', 12], ['192.0.0.0', 24], ['192.0.2.0', 24],
  ['192.88.99.0', 24], ['192.168.0.0', 16], ['198.18.0.0', 15],
  ['198.51.100.0', 24], ['203.0.113.0', 24], ['224.0.0.0', 3],
] as const) blockedIPv4.addSubnet(address, prefix)
const globalIPv6 = new BlockList()
globalIPv6.addSubnet('2000::', 3, 'ipv6')
const blockedIPv6 = new BlockList()
// Exclude special-purpose, documentation, and transition ranges. In particular,
// transition addresses must not tunnel an apparently public request to IPv4 LANs.
blockedIPv6.addSubnet('2001::', 23, 'ipv6')
blockedIPv6.addSubnet('2001:db8::', 32, 'ipv6')
blockedIPv6.addSubnet('2002::', 16, 'ipv6')
blockedIPv6.addSubnet('3fff::', 20, 'ipv6')

function isPublicAddress(address: string): boolean {
  if (isIP(address) === 4) return !blockedIPv4.check(address)
  return isIP(address) === 6
    && globalIPv6.check(address, 'ipv6') && !blockedIPv6.check(address, 'ipv6')
}

function validateURL(input: string): URL {
  const url = new URL(input)
  const hostname = url.hostname.replace(/^\[|\]$/g, '')
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password
    || (isIP(hostname) && !isPublicAddress(hostname))) {
    throw new Error('Invalid icon URL')
  }
  return url
}

// Validation happens inside the socket lookup, returning the exact checked IP
// to Node. A second DNS resolution after validation would allow DNS rebinding.
const publicLookup: NonNullable<RequestOptions['lookup']> = (hostname, options, callback) => {
  lookup(hostname, { all: true }, (error, addresses) => {
    if (error) return callback(error, '', 4)
    if (!addresses.length || addresses.some(({ address }) => !isPublicAddress(address))) {
      return callback(new Error('Icon host must be public'), '', 4)
    }
    if (options.all) callback(null, addresses)
    else callback(null, addresses[0]!.address, addresses[0]!.family)
  })
}

const httpAgent = new HttpAgent({ keepAlive: true, maxSockets: 6, timeout: 30_000 })
const httpsAgent = new HttpsAgent({ keepAlive: true, maxSockets: 6, timeout: 30_000 })

function requestIcon(url: URL, signal: AbortSignal): Promise<IncomingMessage> {
  return new Promise((resolve, reject) => {
    // Dedicated Node agents keep-alive public TLS/TCP sockets without touching
    // Electron's cookie jar, auth headers, or proxy; hostname still supplies Host/SNI.
    const isHttps = url.protocol === 'https:'
    const request = (isHttps ? httpsGet : httpGet)(url, {
      agent: isHttps ? httpsAgent : httpAgent,
      lookup: publicLookup,
      signal,
      headers: { 'Accept-Encoding': 'gzip, deflate, br' },
    }, resolve)
    request.on('error', reject)
  })
}

const maxBytes = 512 * 1024
const maxConcurrent = 6
const maxRedirects = 5
let active = 0
const waiting: Array<() => void> = []

export async function loadRemoteIcon(input: unknown): Promise<string> {
  if (typeof input !== 'string') throw new Error('Invalid icon URL')
  let url = validateURL(input)
  if (active >= maxConcurrent) await new Promise<void>(resolve => waiting.push(resolve))
  else active++
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), 15_000)
  let response: IncomingMessage | undefined
  let body: Readable | undefined
  try {
    for (let redirects = 0; ; redirects++) {
      response = await requestIcon(url, controller.signal)
      if (![301, 302, 303, 307, 308].includes(response.statusCode ?? 0)) break
      const location = response.headers.location
      response.destroy()
      if (!location || redirects >= maxRedirects) throw new Error('Invalid icon redirect')
      url = validateURL(new URL(location, url).href)
    }
    const type = response.headers['content-type']?.split(';')[0].trim().toLowerCase() ?? ''
    if (!response.statusCode || response.statusCode < 200 || response.statusCode >= 300
      || !type.startsWith('image/')) throw new Error('Icon request failed')
    if (Number(response.headers['content-length']) > maxBytes) throw new Error('Icon too large')
    // Some hosts compress even when identity is requested. Bound the decoded
    // stream, matching fetch semantics without buffering a decompression bomb.
    const encoding = response.headers['content-encoding']?.trim().toLowerCase()
    if (!encoding || encoding === 'identity') body = response
    else {
      const decoder = encoding === 'gzip' ? createGunzip()
        : encoding === 'deflate' ? createInflate()
          : encoding === 'br' ? createBrotliDecompress() : undefined
      if (!decoder) throw new Error('Unsupported icon encoding')
      body = decoder
      pipeline(response, decoder, () => { /* Iteration reports stream errors. */ })
    }
    const chunks: Uint8Array[] = []
    let size = 0
    for await (const chunk of body) {
      size += chunk.byteLength
      if (size > maxBytes) throw new Error('Icon too large')
      chunks.push(chunk)
    }
    if (!size) throw new Error('Empty icon')
    return `data:${type};base64,${Buffer.concat(chunks, size).toString('base64')}`
  } finally {
    body?.destroy()
    response?.destroy()
    clearTimeout(timeout)
    controller.abort()
    const next = waiting.shift()
    if (next) next()
    else active--
  }
}
