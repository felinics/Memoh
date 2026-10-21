export function normalizePublicWebhookBase(raw: string): string {
  try {
    if (hasAmbiguousIPv4Host(raw)) return ''
    const parsed = new URL(raw)
    if (parsed.protocol !== 'https:' || parsed.username || parsed.password || parsed.search || parsed.hash) return ''
    const host = canonicalPublicWebhookHost(parsed.hostname)
    if (!host) return ''
    if (parsed.port || hasExplicitPort(raw) || parsed.pathname !== '/') return ''
    return `https://${host}`
  } catch {
    return ''
  }
}

function canonicalPublicWebhookHost(hostname: string): string {
  // Keep this in sync with internal/channel/public_host.go; this copy lets the
  // settings UI reject unusable webhook origins before it calls the server.
  const host = hostname.trim().toLowerCase().replace(/^\[(.*)\]$/, '$1').replace(/\.+$/, '')
  if (!host || host === 'localhost' || host.endsWith('.localhost') || host.endsWith('.local')) return ''

  if (isIPv4Literal(host)) return isPublicIPv4(host) ? host : ''
  if (host.includes(':') || !host.includes('.') || host.split('.').some(part => part === '')) return ''
  if (isSpecialUseDNSName(host)) return ''
  return host
}

function hasExplicitPort(raw: string): boolean {
  const authority = rawAuthority(raw)
  const host = authority.includes('@') ? authority.slice(authority.lastIndexOf('@') + 1) : authority
  if (host.startsWith('[')) return /\]:\d+$/.test(host)
  return /:\d+$/.test(host)
}

function hasAmbiguousIPv4Host(raw: string): boolean {
  const host = rawHost(raw)
  return isIPv4Literal(host) && host.split('.').some(part => part.length > 1 && part.startsWith('0'))
}

function rawHost(raw: string): string {
  const authority = rawAuthority(raw)
  const host = authority.includes('@') ? authority.slice(authority.lastIndexOf('@') + 1) : authority
  if (host.startsWith('[')) return host.replace(/^\[(.*)\](?::\d+)?$/, '$1').toLowerCase()
  return (host.split(':', 1)[0] ?? '').trim().toLowerCase().replace(/\.+$/, '')
}

function rawAuthority(raw: string): string {
  return raw.trim()
    .replace(/^[a-z][a-z0-9+.-]*:\/\//i, '')
    .split(/[/?#]/, 1)[0] ?? ''
}

function isSpecialUseDNSName(host: string): boolean {
  return host === 'localhost' ||
    host.endsWith('.localhost') ||
    host.endsWith('.local') ||
    host.endsWith('.internal') ||
    host.endsWith('.test') ||
    host.endsWith('.invalid') ||
    host.endsWith('.example') ||
    host.endsWith('.home.arpa')
}

function isIPv4Literal(host: string): boolean {
  return /^\d{1,3}(?:\.\d{1,3}){3}$/.test(host)
}

function isPublicIPv4(host: string): boolean {
  const parts = host.split('.').map(part => Number(part))
  if (parts.length !== 4 || parts.some(part => !Number.isInteger(part) || part < 0 || part > 255)) return false
  const [a, b, c] = parts as [number, number, number, number]
  return !(a === 0 ||
    a === 10 ||
    a === 127 ||
    (a === 169 && b === 254) ||
    (a === 172 && b >= 16 && b <= 31) ||
    (a === 192 && b === 168) ||
    (a === 100 && b >= 64 && b <= 127) ||
    (a === 192 && b === 0 && c === 0) ||
    (a === 192 && b === 0 && c === 2) ||
    (a === 198 && (b === 18 || b === 19)) ||
    (a === 198 && b === 51 && c === 100) ||
    (a === 203 && b === 0 && c === 113) ||
    a >= 224)
}
