import { describe, expect, it } from 'vitest'
import { cacheHitRate, formatCacheHitRate } from './cache-usage'

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
