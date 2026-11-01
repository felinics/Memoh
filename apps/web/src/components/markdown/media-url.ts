import { sdkApiUrl, sdkAuthQuery } from '@/lib/api-client'

// Only server-published asset references receive credentials. Never append
// authentication to model-authored external URLs or workspace paths.
export function markdownAssetUrl(value: string): string | null {
  const match = /^\/bots\/([0-9a-f-]+)\/media\/([0-9a-f]{64})$/i.exec(value)
  if (!match) return null
  return sdkApiUrl({
    url: '/bots/{bot_id}/media/{content_hash}',
    path: { bot_id: match[1], content_hash: match[2] },
    query: sdkAuthQuery(),
  })
}
