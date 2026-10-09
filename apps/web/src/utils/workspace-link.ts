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
//
// Pass the rendered `label` when it is known, so a bare name the parser
// auto-linked (see isBareDomainAutolink) is shown as text too.
export function classifyWorkspaceLink(href: string, label?: string): WorkspaceLink | null {
  const value = href.trim()
  if (label !== undefined && isBareDomainAutolink(label, value)) return null
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

// The parser auto-links bare domains, and file extensions such as .md, .sh and
// .ai are also domains, so `summary.md` in prose arrives as a link to
// http://summary.md. It is recognized by its label: `http://` plus the label
// gives the destination. `www.` names stay links, as in GFM. A written
// `[example.com](http://example.com)` looks the same and is shown as text too.
// The parsed link does not say whether it was auto-linked; replace this check
// once it does (https://github.com/Simon-He95/markstream-vue/issues/784).
function isBareDomainAutolink(label: string, href: string): boolean {
  const text = label.trim()
  if (!/^http:\/\//i.test(href) || /^www\./i.test(text)) return false
  try {
    return new URL(`http://${text}`).href === new URL(href).href
  } catch {
    return false
  }
}
