import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  getConnector: vi.fn(),
}))

vi.mock('@memohai/sdk', () => ({
  getBotsByBotIdConnectorsByConnectionId: mocks.getConnector,
}))

import { isConnectorOAuthCancelled, waitForConnectorOAuth } from './useConnectorOAuth'

describe('waitForConnectorOAuth', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.getConnector.mockResolvedValue({ data: { status: 'pending' } })
  })

  it('resolves once the connection turns active', async () => {
    mocks.getConnector.mockResolvedValue({ data: { status: 'active' } })
    await expect(waitForConnectorOAuth('bot', 'conn', null)).resolves.toBeUndefined()
  })

  it('rejects as cancelled when the caller aborts mid-wait', async () => {
    const controller = new AbortController()
    const popup = { closed: false, close: vi.fn() } as unknown as Window

    const wait = waitForConnectorOAuth('bot', 'conn', popup, controller.signal)
    // Let the first poll run and settle on `pending` before cancelling, so the
    // abort has to interrupt a wait that would otherwise keep polling.
    await vi.waitFor(() => expect(mocks.getConnector).toHaveBeenCalled())
    controller.abort()

    await expect(wait).rejects.toSatisfy(isConnectorOAuthCancelled)
    expect(popup.close).toHaveBeenCalled()
  })

  it('rejects immediately when the signal is already aborted', async () => {
    await expect(
      waitForConnectorOAuth('bot', 'conn', null, AbortSignal.abort()),
    ).rejects.toSatisfy(isConnectorOAuthCancelled)
    expect(mocks.getConnector).not.toHaveBeenCalled()
  })

  it('does not treat a real failure as a cancellation', async () => {
    mocks.getConnector.mockResolvedValue({ data: { status: 'authorization_failed' } })
    await expect(waitForConnectorOAuth('bot', 'conn', null)).rejects.toSatisfy(
      error => !isConnectorOAuthCancelled(error),
    )
  })
})
