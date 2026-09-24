import { getSiteIcon, type HandlersSiteIconResponse } from '@memohai/sdk'

// Icons belong to a site, so every link to one origin shares a single lookup
// for the lifetime of the page. The response already carries both color
// schemes, which lets a theme switch pick the other icon without refetching.
// Failed lookups are dropped so a later render can retry.
const lookups = new Map<string, Promise<HandlersSiteIconResponse | null>>()

export function siteIconOrigin(href: string): string | null {
  try {
    const url = new URL(href, window.location.href)
    return url.protocol === 'http:' || url.protocol === 'https:' ? url.origin : null
  } catch {
    return null
  }
}

// The request is not tied to any one link's lifetime: other links may be
// waiting on the same promise, so callers ignore stale results instead of
// aborting it.
export function lookupSiteIcon(origin: string): Promise<HandlersSiteIconResponse | null> {
  let lookup = lookups.get(origin)
  if (!lookup) {
    lookup = getSiteIcon({ query: { url: origin } })
      .then(({ data }) => data ?? null)
      .catch(() => {
        lookups.delete(origin)
        return null
      })
    lookups.set(origin, lookup)
  }
  return lookup
}

export function clearSiteIconLookups() {
  lookups.clear()
}
