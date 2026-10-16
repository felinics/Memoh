import { beforeEach, describe, expect, it, vi } from 'vitest'
import { postBotsByBotIdAppsCheckUpdates } from '@memohai/sdk'
import { renderI18nMessage, UserFacingError } from '@/utils/api-error'
import { checkAppUpdates } from './useApps'

vi.mock('@memohai/sdk', () => ({
  postBotsByBotIdAppsCheckUpdates: vi.fn(),
}))

const checkUpdates = vi.mocked(postBotsByBotIdAppsCheckUpdates)

function answer(result: { data?: unknown, error?: unknown, status: number }) {
  checkUpdates.mockResolvedValueOnce({
    data: result.data,
    error: result.error,
    request: new Request('http://localhost/bots/bot-1/apps/check-updates', { method: 'POST' }),
    response: new Response(null, { status: result.status }),
  } as never)
}

describe('checkAppUpdates', () => {
  beforeEach(() => {
    checkUpdates.mockReset()
  })

  it('returns the refreshed list, including an incomplete verdict', async () => {
    answer({ data: { items: [], update_checks_incomplete: true }, status: 200 })

    await expect(checkAppUpdates('bot-1')).resolves.toEqual({ items: [], update_checks_incomplete: true })
  })

  it.each([504, 524])('describes a proxy timeout (%i) as one', async (status) => {
    answer({ error: '<html>origin timeout</html>', status })

    const error = await checkAppUpdates('bot-1').catch((caught: unknown) => caught)
    expect(error).toBeInstanceOf(UserFacingError)
    expect((error as Error).message).toBe(renderI18nMessage('apps.checkUpdatesTimedOut'))
  })

  it('passes any other failure through', async () => {
    const problem = { code: 'app.operation_failed', status: 502 }
    answer({ error: problem, status: 502 })

    await expect(checkAppUpdates('bot-1')).rejects.toBe(problem)
  })
})
