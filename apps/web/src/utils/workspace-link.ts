import { tryParseLocalhostHref } from '@/utils/localhost-link'

export type WorkspaceLink =
  | { kind: 'file', path: string }
  | { kind: 'browser', address: string }
  | { kind: 'external' }

// A single leading slash is a workspace path. Protocol-relative URLs and
// fragments retain their normal web meaning; never turn them into file reads.
export function classifyWorkspaceLink(href: string): WorkspaceLink | null {
  const value = href.trim()
  if (value.startsWith('/') && !value.startsWith('//') && !value.includes('\\')) {
    try {
      const path = decodeURIComponent(value)
      if (/[\u0000-\u001f\u007f]/.test(path)) return null
      return { kind: 'file', path }
    } catch {
      return null
    }
  }
  const local = tryParseLocalhostHref(value)
  if (local) return { kind: 'browser', address: local.display }
  if (/^https?:\/\//i.test(value) || value.startsWith('//')) {
    try {
      const url = new URL(value, 'https://example.invalid')
      if (url.protocol === 'http:' || url.protocol === 'https:') return { kind: 'external' }
    } catch { /* Incomplete streaming links do not get an actionable type. */ }
  }
  return null
}
