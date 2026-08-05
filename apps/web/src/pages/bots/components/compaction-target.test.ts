import { describe, expect, it } from 'vitest'
import {
  compactionTargetPercentAfterToggle,
  isCompactionTargetPercentInvalid,
} from './compaction-target'

describe('isCompactionTargetPercentInvalid', () => {
  it.each([
    [null, false],
    [1, false],
    [99, false],
    [0, true],
    [100, true],
    [1.5, true],
  ])('validates %s as %s', (target, invalid) => {
    expect(isCompactionTargetPercentInvalid(target)).toBe(invalid)
  })
})

describe('compactionTargetPercentAfterToggle', () => {
  it('restores the saved target before hiding an invalid disabled draft', () => {
    expect(compactionTargetPercentAfterToggle(false, 100, 55)).toBe(55)
    expect(compactionTargetPercentAfterToggle(false, 1.5, null)).toBeNull()
  })

  it('preserves valid drafts and enabled validation state', () => {
    expect(compactionTargetPercentAfterToggle(false, 45, 55)).toBe(45)
    expect(compactionTargetPercentAfterToggle(true, 100, 55)).toBe(100)
  })
})
