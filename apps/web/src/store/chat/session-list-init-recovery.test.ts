import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { toast } from '@felinic/ui'
import { createInitializeRecovery } from './session-list-init-recovery'

vi.mock('@felinic/ui', () => ({
  toast: { error: vi.fn() },
}))
vi.mock('@/i18n', () => ({
  default: { global: { t: (key: string) => key } },
}))

const toastError = vi.mocked(toast.error)

describe('chat initialize recovery', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    toastError.mockClear()
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('retries with backoff until initialize succeeds, toasting once (#1070)', async () => {
    const currentBotId = ref<string | null>('bot-1')
    const initialize = vi.fn()
      .mockRejectedValueOnce(new Error('down'))
      .mockRejectedValueOnce(new Error('down'))
      .mockRejectedValueOnce(new Error('down'))
      .mockResolvedValueOnce(undefined)
    const { initializeWithRecovery } = createInitializeRecovery({ currentBotId, initialize })

    const done = initializeWithRecovery()
    // First attempt runs synchronously before any backoff sleep.
    expect(initialize).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(1000)
    expect(initialize).toHaveBeenCalledTimes(2)
    expect(toastError).toHaveBeenCalledTimes(1)

    // Third failure must not re-toast; backoff keeps doubling (2s, then 4s).
    await vi.advanceTimersByTimeAsync(2000)
    expect(initialize).toHaveBeenCalledTimes(3)
    expect(toastError).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(4000)
    await expect(done).resolves.toBeUndefined()
    expect(initialize).toHaveBeenCalledTimes(4)
    expect(toastError).toHaveBeenCalledTimes(1)
  })

  it('stops retrying when the bot scope changes mid-outage', async () => {
    const currentBotId = ref<string | null>('bot-1')
    const initialize = vi.fn().mockRejectedValue(new Error('down'))
    const { initializeWithRecovery } = createInitializeRecovery({ currentBotId, initialize })

    const done = initializeWithRecovery()
    expect(initialize).toHaveBeenCalledTimes(1)

    // A bot switch (or sign-out) owns its own initialize; the old loop exits.
    currentBotId.value = 'bot-2'
    await vi.advanceTimersByTimeAsync(60_000)

    await expect(done).resolves.toBeUndefined()
    expect(initialize).toHaveBeenCalledTimes(1)
    expect(toastError).not.toHaveBeenCalled()
  })

  it('hands recovery off once a bot gets selected (first-open, no bot id)', async () => {
    const currentBotId = ref<string | null>(null)
    const initialize = vi.fn().mockRejectedValue(new Error('down'))
    const { initializeWithRecovery } = createInitializeRecovery({ currentBotId, initialize })

    const done = initializeWithRecovery()
    expect(initialize).toHaveBeenCalledTimes(1)

    // ensureBot inside a later attempt would set the id; simulate the watcher
    // taking over by setting it here — the ''-scoped loop must stop.
    currentBotId.value = 'bot-1'
    await vi.advanceTimersByTimeAsync(60_000)

    await expect(done).resolves.toBeUndefined()
    expect(initialize).toHaveBeenCalledTimes(1)
  })

  it('resolves immediately when initialize succeeds first try', async () => {
    const currentBotId = ref<string | null>('bot-1')
    const initialize = vi.fn().mockResolvedValue(undefined)
    const { initializeWithRecovery } = createInitializeRecovery({ currentBotId, initialize })

    await expect(initializeWithRecovery()).resolves.toBeUndefined()
    expect(initialize).toHaveBeenCalledTimes(1)
    expect(toastError).not.toHaveBeenCalled()
  })
})
