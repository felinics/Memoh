import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/api-client', () => ({
  sdkAuthQuery: vi.fn(() => ({ token: 'test-only-token' })),
  sdkApiUrl: vi.fn(({ path, query }) => `/api/bots/${path.bot_id}/media/${path.content_hash}?token=${query.token}`),
}))
import { sdkAuthQuery } from '@/lib/api-client'
import { markdownAssetUrl } from './media-url'

describe('Markdown asset authentication', () => {
  beforeEach(() => vi.clearAllMocks())

  it('does not send credentials to external, workspace or malformed references', () => {
    for (const value of ['https://example.com/a.png', '//example.com/a.png', '/data/a.png', '/bots/../../media/a', '/bots/abc/media/not-a-hash']) {
      expect(markdownAssetUrl(value)).toBeNull()
    }
    expect(sdkAuthQuery).not.toHaveBeenCalled()
  })

  it('uses the authenticated media route for archived assets', () => {
    const hash = 'a'.repeat(64)
    expect(markdownAssetUrl(`/bots/abc-def/media/${hash}`)).toBe(`/api/bots/abc-def/media/${hash}?token=test-only-token`)
  })
})
