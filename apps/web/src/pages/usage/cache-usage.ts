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
