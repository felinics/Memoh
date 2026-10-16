export interface BrowserAddress {
  port: number
  path: string
  display: string
}

const DEFAULT_BROWSER_ADDRESS = 'localhost:5173/'

export function defaultBrowserAddress(): string {
  return DEFAULT_BROWSER_ADDRESS
}

export function parseBrowserAddress(raw: string): BrowserAddress {
  let value = (raw || DEFAULT_BROWSER_ADDRESS).trim()
  if (!value) value = DEFAULT_BROWSER_ADDRESS
  if (/^:\d+($|[/?#])/.test(value)) {
    value = `localhost${value}`
  } else if (/^\d+($|[/?#])/.test(value)) {
    value = `localhost:${value}`
  }
  if (!/^https?:\/\//i.test(value)) {
    value = `http://${value}`
  }

  const portMatch = value.match(/^[a-z][a-z\d+.-]*:\/\/(?:\[[^\]]+\]|[^/:?#]+):(\d+)/i)
  if (portMatch) {
    const explicitPort = Number.parseInt(portMatch[1]!, 10)
    if (!Number.isInteger(explicitPort) || explicitPort < 1 || explicitPort > 65535) {
      throw new Error('Browser address must include a port from 1 to 65535')
    }
  }

  let parsed: URL
  try {
    parsed = new URL(value)
  } catch (error) {
    const message = error instanceof Error ? error.message.toLowerCase() : ''
    if (message.includes('port')) {
      throw new Error('Browser address must include a port from 1 to 65535')
    }
    throw new Error('Invalid browser address')
  }

  const hostname = parsed.hostname.toLowerCase()
  if (!['localhost', '127.0.0.1', '[::1]'].includes(hostname)) {
    throw new Error('Browser address must use localhost, 127.0.0.1 or [::1]')
  }

  const port = Number.parseInt(portMatch?.[1] || parsed.port || '', 10)
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error('Browser address must include a port from 1 to 65535')
  }

  const path = `${parsed.pathname || '/'}${parsed.search}${parsed.hash}`
  return {
    port,
    path,
    display: `localhost:${port}${path}`,
  }
}
