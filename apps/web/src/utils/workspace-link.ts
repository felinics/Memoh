import { tryParseLocalhostHref } from '@/utils/localhost-link'

export type WorkspaceLink =
  | { kind: 'file', path: string }
  | { kind: 'browser', address: string }
  | { kind: 'external' }
  | { kind: 'native' }

// The one place that decides what a chat link opens. `null` means Memoh cannot
// open it, and the link is shown as its label only. That covers relative paths
// such as `summary.md`: the browser would resolve them against the app itself
// and replace the page (on Desktop, with a bare "not found" and no way back).
// It also covers other schemes (`file:`, `vscode:`): they hand the click to
// a local program chosen by text the model wrote, and Desktop refuses them
// anyway.
//
// A single leading slash is a workspace path. Protocol-relative URLs and
// fragments retain their normal web meaning; never turn them into file reads.
// A query or fragment on a path (e.g. `#L10`) is not part of the file name.
export function classifyWorkspaceLink(href: string): WorkspaceLink | null {
  const value = href.trim()
  // The browser's own handling is safe here: a fragment scrolls within the
  // page, and mail links are on Desktop's external allowlist.
  if (value.startsWith('#') || /^mailto:/i.test(value)) return { kind: 'native' }
  if (value.startsWith('/') && !value.startsWith('//') && !value.includes('\\')) {
    try {
      const path = decodeURIComponent(value.split(/[?#]/, 1)[0]!)
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
