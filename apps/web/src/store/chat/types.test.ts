import { describe, expect, it } from 'vitest'
import { isRuntimeContinuationUserTurn, isRuntimeSteerTurnId } from './types'

describe('runtime queue turn classification', () => {
  it('recognizes only a continuation user turn as a follow-up input', () => {
    expect(isRuntimeContinuationUserTurn({
      role: 'user',
      turnId: 'continuation-turn',
      runtimeRunId: 'continuation-run',
      runtimeContinuation: true,
    })).toBe(true)

    expect(isRuntimeContinuationUserTurn({
      role: 'user',
      turnId: 'queue-steer:item-1',
      runtimeRunId: 'run-1',
      runtimeContinuation: true,
    })).toBe(false)

    expect(isRuntimeContinuationUserTurn({
      role: 'user',
      turnId: 'ordinary-turn',
      runtimeRunId: 'run-1',
      runtimeContinuation: false,
    })).toBe(false)
  })

  it('keeps the existing steer identity separate', () => {
    expect(isRuntimeSteerTurnId('queue-steer:item-1')).toBe(true)
    expect(isRuntimeSteerTurnId('continuation-turn')).toBe(false)
  })
})
