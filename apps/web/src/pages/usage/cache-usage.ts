import type { HandlersDailyTokenUsage } from '@memohai/sdk'

type CacheUsageRow = {
  input_tokens?: number
  cache_read_tokens?: number
  cache_read_tokens_reported?: boolean
}

export function cacheReadReported(row: CacheUsageRow): boolean {
  return row.cache_read_tokens_reported === true
}

export function cacheHitRate(rows: readonly CacheUsageRow[]): number | null {
  if (rows.length === 0 || rows.some(row => !cacheReadReported(row))) return null
  const input = rows.reduce((sum, row) => sum + (row.input_tokens ?? 0), 0)
  if (input <= 0) return null
  const read = rows.reduce((sum, row) => sum + (row.cache_read_tokens ?? 0), 0)
  return read / input * 100
}

export function formatCacheHitRate(rate: number | null): string {
  if (rate === null) return '—'
  if (rate > 0 && rate < 0.1) return '<0.1%'
  return `${rate.toFixed(1)}%`
}

export function buildDayMap(rows: readonly HandlersDailyTokenUsage[] | undefined): Map<string, HandlersDailyTokenUsage> {
  const map = new Map<string, HandlersDailyTokenUsage>()
  for (const row of rows ?? []) {
    if (!row.day) continue
    const seen = map.get(row.day)
    map.set(row.day, seen
      ? {
          day: row.day,
          input_tokens: (seen.input_tokens ?? 0) + (row.input_tokens ?? 0),
          output_tokens: (seen.output_tokens ?? 0) + (row.output_tokens ?? 0),
          cache_read_tokens: (seen.cache_read_tokens ?? 0) + (row.cache_read_tokens ?? 0),
          cache_read_tokens_reported: cacheReadReported(seen) && cacheReadReported(row),
          reasoning_tokens: (seen.reasoning_tokens ?? 0) + (row.reasoning_tokens ?? 0),
        }
      : row)
  }
  return map
}
