import type { CompactionLog, HandlersSessionCompactionsResponse } from '@memohai/sdk'

export interface MergedCompactionPages {
  // Oldest first, so the trajectory can interleave them with the turns.
  items: CompactionLog[]
  hasMore: boolean
  nextCursor: string | null
}

// Pages are keyset slices newest first; older pages are immutable, so they
// always join the first page, and a compaction repeated across a boundary
// keeps its first occurrence.
export function mergeCompactionPages(
  first: HandlersSessionCompactionsResponse | null | undefined,
  older: HandlersSessionCompactionsResponse[],
): MergedCompactionPages {
  const pages = first ? [first, ...older] : []
  const seen = new Set<string>()
  const newestFirst: CompactionLog[] = []
  for (const page of pages) {
    for (const item of page.items ?? []) {
      if (!item.started_at) continue
      const key = item.id ?? ''
      if (key && seen.has(key)) continue
      if (key) seen.add(key)
      newestFirst.push(item)
    }
  }
  const last = pages[pages.length - 1]
  const hasMore = last?.has_more === true
  return {
    items: newestFirst.sort((a, b) => Date.parse(a.started_at!) - Date.parse(b.started_at!)),
    hasMore,
    nextCursor: hasMore && last?.next_cursor ? last.next_cursor : null,
  }
}
