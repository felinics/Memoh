import { describe, expect, it } from 'vitest'
import { createReasoningTimingStore } from './reasoning-timing'

describe('reasoning timing', () => {
  it('keeps the original start while streamed reasoning content grows', () => {
    let now = 1_000
    const timings = createReasoningTimingStore(() => now)
    const block = { id: 3, type: 'reasoning', content: 'first token' }

    timings.markSeen('assistant-turn', block)
    now = 4_000
    block.content = 'first token and many more tokens'
    timings.markSeen('assistant-turn', block)
    now = 9_001
    timings.finalize('assistant-turn', block)

    expect(timings.getDuration('assistant-turn', block)).toBe(8_001)
  })

  it('measures identical reasoning text independently across messages', () => {
    let now = 10_000
    const timings = createReasoningTimingStore(() => now)
    const block = { id: 1, type: 'reasoning', content: 'shared text' }

    timings.markSeen('first-message', block)
    now = 11_000
    timings.markSeen('second-message', block)
    now = 13_000
    timings.finalize('first-message', block)
    timings.finalize('second-message', block)

    expect(timings.getDuration('first-message', block)).toBe(3_000)
    expect(timings.getDuration('second-message', block)).toBe(2_000)
  })

  it('finalizes a block once and keeps the existing minimum-duration behavior', () => {
    let now = 20_000
    const timings = createReasoningTimingStore(() => now)
    const block = { id: 2, type: 'reasoning' }

    timings.markSeen('instant-message', block)
    timings.finalize('instant-message', block)
    now = 25_000
    timings.finalize('instant-message', block)

    expect(timings.getDuration('instant-message', block)).toBe(1)
  })
})
