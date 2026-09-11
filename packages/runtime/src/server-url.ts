/**
 * Normalizes the HTTP/WebSocket spellings of the same Memoh server endpoint.
 */
export function normalizeRuntimeServerUrl(serverUrl: string): string {
  const url = new URL(serverUrl.trim())
  if (url.protocol === 'ws:') {
    url.protocol = 'http:'
  } else if (url.protocol === 'wss:') {
    url.protocol = 'https:'
  }
  url.pathname = url.pathname.replace(/\/+$/, '') || '/'
  url.search = ''
  url.hash = ''
  return url.href
}

export function runtimeConnectUrl(serverUrl: string): URL {
  const url = new URL(serverUrl)
  if (url.protocol === 'http:') {
    url.protocol = 'ws:'
  } else if (url.protocol === 'https:') {
    url.protocol = 'wss:'
  }
  const basePath = url.pathname.replace(/\/+$/, '')
  url.pathname = `${basePath}/runtimes/connect`
  url.search = ''
  url.hash = ''
  return url
}

export function assertSecureRuntimeUrl(url: URL, insecureLocalhost = false): void {
  if (url.username || url.password) {
    throw new Error('runtime connection URL must not contain credentials')
  }
  if (url.protocol === 'wss:') {
    return
  }
  const hostname = url.hostname.replace(/^\[|\]$/g, '')
  const local = hostname === '127.0.0.1' || hostname === 'localhost' || hostname === '::1'
  if (url.protocol === 'ws:' && insecureLocalhost && local) {
    return
  }
  throw new Error('runtime connections require wss://; use --insecure-localhost only for localhost development')
}
