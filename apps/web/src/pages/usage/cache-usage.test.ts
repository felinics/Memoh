import { describe, expect, it } from 'vitest'
import { buildDayMap, cacheHitRate, formatCacheHitRate } from './cache-usage'

describe('cache usage reporting', () => {
  it('shows an explicit zero as zero', () => {
    expect(formatCacheHitRate(cacheHitRate([{ input_tokens: 100, cache_read_tokens: 0, cache_read_tokens_reported: true }]))).toBe('0.0%')
  })

  it('keeps unreported and legacy zero usage unavailable', () => {
    expect(cacheHitRate([{ input_tokens: 100, cache_read_tokens: 0, cache_read_tokens_reported: false }])).toBeNull()
    expect(cacheHitRate([{ input_tokens: 100, cache_read_tokens: 0 }])).toBeNull()
  })

  it('does not infer a known rate from a mixed aggregate with positive cache reads', () => {
    expect(cacheHitRate([{ input_tokens: 1000, cache_read_tokens: 10, cache_read_tokens_reported: false }])).toBeNull()
    expect(cacheHitRate([
      { input_tokens: 100, cache_read_tokens: 10, cache_read_tokens_reported: true },
      { input_tokens: 900, cache_read_tokens: 0, cache_read_tokens_reported: false },
    ])).toBeNull()
  })

  it('does not infer reporting from legacy positive reads', () => {
    expect(cacheHitRate([{ input_tokens: 10, cache_read_tokens: 200 }])).toBeNull()
  })

  it('does not round positive cache use to zero', () => {
    expect(formatCacheHitRate(cacheHitRate([{ input_tokens: 350000, cache_read_tokens: 1, cache_read_tokens_reported: true }]))).toBe('<0.1%')
  })

  it('keeps an empty input unavailable', () => {
    expect(formatCacheHitRate(cacheHitRate([]))).toBe('—')
    expect(cacheHitRate([{ input_tokens: 0, cache_read_tokens: 0, cache_read_tokens_reported: true }])).toBeNull()
  })
})

describe('daily usage rows', () => {
  it('adds rows of the same day and reports cache reads only when every row does', () => {
    const days = buildDayMap([
      { day: '2026-09-30', input_tokens: 10, output_tokens: 1, cache_read_tokens: 200, reasoning_tokens: 0 },
      { day: '2026-09-30', input_tokens: 1000, output_tokens: 5, cache_read_tokens: 400, cache_read_tokens_reported: true, reasoning_tokens: 2 },
      { day: '2026-10-01', input_tokens: 3000, output_tokens: 7, cache_read_tokens: 0, cache_read_tokens_reported: true, reasoning_tokens: 3 },
      { day: '2026-10-01', input_tokens: 1000, output_tokens: 3, cache_read_tokens: 400, cache_read_tokens_reported: true, reasoning_tokens: 1 },
    ])
    expect(days.get('2026-09-30')).toEqual({ day: '2026-09-30', input_tokens: 1010, output_tokens: 6, cache_read_tokens: 600, cache_read_tokens_reported: false, reasoning_tokens: 2 })
    expect(days.get('2026-10-01')).toEqual({ day: '2026-10-01', input_tokens: 4000, output_tokens: 10, cache_read_tokens: 400, cache_read_tokens_reported: true, reasoning_tokens: 4 })
  })
})
