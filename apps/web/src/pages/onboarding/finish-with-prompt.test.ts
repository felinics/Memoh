import { describe, expect, it, vi } from 'vitest'
import { finishOnboardingWithPrompt } from './finish-with-prompt'

function deps(completeResult: boolean) {
  return {
    complete: vi.fn(async () => completeResult),
    prefill: { request: vi.fn(), clear: vi.fn() },
  }
}

describe('finishOnboardingWithPrompt', () => {
  it('queues the prompt for the new bot before finishing', async () => {
    const d = deps(true)
    d.complete.mockImplementation(async () => {
      expect(d.prefill.request).toHaveBeenCalledWith('bot-1', 'hello')
      return true
    })
    expect(await finishOnboardingWithPrompt({ ...d, botId: 'bot-1', prompt: 'hello' })).toBe(true)
    expect(d.prefill.clear).not.toHaveBeenCalled()
  })

  it('withdraws the prompt when finishing fails', async () => {
    const d = deps(false)
    expect(await finishOnboardingWithPrompt({ ...d, botId: 'bot-1', prompt: 'hello' })).toBe(false)
    expect(d.prefill.clear).toHaveBeenCalledOnce()
  })

  it('finishes without queuing when there is no prompt or no bot', async () => {
    const d = deps(true)
    await finishOnboardingWithPrompt({ ...d, botId: 'bot-1' })
    await finishOnboardingWithPrompt({ ...d, prompt: 'hello' })
    expect(d.prefill.request).not.toHaveBeenCalled()
    expect(d.complete).toHaveBeenCalledTimes(2)
  })
})
