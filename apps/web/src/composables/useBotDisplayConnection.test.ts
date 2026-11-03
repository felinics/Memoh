import { afterAll, beforeEach, describe, expect, it, vi } from 'vitest'

const postDisplayPrepareStream = vi.fn()
const getDisplay = vi.fn()
const localStorageStub = {
  getItem: (key: string) => key === 'language' ? 'zh' : null,
  setItem: vi.fn(),
  removeItem: vi.fn(),
  clear: vi.fn(),
}

vi.stubGlobal('localStorage', localStorageStub)

vi.mock('@memohai/sdk', () => ({
  deleteBotsByBotIdContainerDisplaySessionsBySessionId: vi.fn(),
  getBotsByBotIdContainerDisplay: (...args: unknown[]) => getDisplay(...args),
  postBotsByBotIdContainerDisplayWebrtcOffer: vi.fn(),
}))

vi.mock('@/composables/api/useDisplayPrepareStream', () => ({
  postBotsByBotIdContainerDisplayPrepareStream: (...args: unknown[]) => postDisplayPrepareStream(...args),
}))

const { BotDisplayConnection } = await import('./useBotDisplayConnection')

describe('BotDisplayConnection structured preparation errors', () => {
  afterAll(() => {
    vi.unstubAllGlobals()
  })

  beforeEach(() => {
    postDisplayPrepareStream.mockReset()
    getDisplay.mockReset()
    vi.stubGlobal('localStorage', localStorageStub)
  })

  it('renders a structured preparation error through the public readiness flow', async () => {
    getDisplay.mockResolvedValue({
      data: {
        enabled: true,
        available: false,
        running: false,
        prepare_supported: true,
      },
    })
    postDisplayPrepareStream.mockResolvedValue({
      stream: (async function* () {
        yield {
          type: 'error',
          code: 'workspace.unreachable',
          args: {},
          message: 'The workspace could not be reached.',
          request_id: 'req-display-1',
        }
      })(),
    })

    const connection = new BotDisplayConnection('bot-1')
    const ready = await connection.ensureReady()

    expect(ready).toBe(false)
    expect(connection.status.value).toBe('unavailable')
    expect(connection.unavailableReason.value).toBe('暂时无法连接工作区，请稍后重试')
  })
})
