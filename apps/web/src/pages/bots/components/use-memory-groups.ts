import { computed } from 'vue'
import type { ComputedRef } from 'vue'
import type { AdaptersMemoryItem } from '@memohai/sdk'

export interface MemoryGroup<T extends AdaptersMemoryItem = AdaptersMemoryItem> {
  /** ISO date string: yyyy-MM-dd */
  date: string
  /** Human-readable relative label, e.g. "Today", "Yesterday", "Jun 14" */
  label: string
  items: T[]
}

export interface MemoryGroupStats {
  totalCount: number
  /** ISO timestamp of the most recent updated_at (falls back to created_at), or null when empty */
  lastUpdatedAt: string | null
}

export interface UseMemoryGroupsResult<T extends AdaptersMemoryItem = AdaptersMemoryItem> {
  groups: ComputedRef<MemoryGroup<T>[]>
  stats: ComputedRef<MemoryGroupStats>
}

/**
 * Group memory items by their `created_at` date and compute summary stats.
 * Groups are sorted newest-first. Each group label is a locale-aware relative
 * day label (Today / Yesterday / calendar date) so the page reads as a natural
 * timeline without a second column.
 */
export function useMemoryGroups<T extends AdaptersMemoryItem>(
  memories: ComputedRef<T[]>,
  locale: ComputedRef<string>,
): UseMemoryGroupsResult<T> {
  const groups = computed<MemoryGroup<T>[]>(() => {
    const list = memories.value
    if (list.length === 0) return []

    const bucketMap = new Map<string, T[]>()

    for (const item of list) {
      const dateKey = dateKeyFromISO(item.created_at)
      const bucket = bucketMap.get(dateKey)
      if (bucket) {
        bucket.push(item)
      } else {
        bucketMap.set(dateKey, [item])
      }
    }

    const sortedKeys = [...bucketMap.keys()].sort((a, b) => (a < b ? 1 : a > b ? -1 : 0))

    const rtf = new Intl.RelativeTimeFormat(locale.value, { numeric: 'auto' })

    return sortedKeys.map((dateKey) => {
      const items = bucketMap.get(dateKey)!
      items.sort((a, b) => {
        const ta = a.created_at ? new Date(a.created_at).getTime() : 0
        const tb = b.created_at ? new Date(b.created_at).getTime() : 0
        return tb - ta
      })
      return {
        date: dateKey,
        label: formatGroupLabel(dateKey, rtf, locale.value),
        items,
      }
    })
  })

  const stats = computed<MemoryGroupStats>(() => {
    const list = memories.value
    if (list.length === 0) {
      return { totalCount: 0, lastUpdatedAt: null }
    }
    let latest: string | null = null
    let latestTs = -Infinity
    for (const item of list) {
      const ts = item.updated_at
        ? new Date(item.updated_at).getTime()
        : item.created_at
          ? new Date(item.created_at).getTime()
          : 0
      if (Number.isFinite(ts) && ts > latestTs) {
        latestTs = ts
        latest = item.updated_at ?? item.created_at ?? null
      }
    }
    return { totalCount: list.length, lastUpdatedAt: latest }
  })

  return { groups, stats }
}

function dateKeyFromISO(iso: string | undefined | null): string {
  if (!iso) {
    return new Date().toISOString().slice(0, 10)
  }
  const parsed = new Date(iso)
  if (Number.isNaN(parsed.getTime())) {
    return new Date().toISOString().slice(0, 10)
  }
  return parsed.toISOString().slice(0, 10)
}

function formatGroupLabel(dateKey: string, rtf: Intl.RelativeTimeFormat, locale: string): string {
  const today = new Date()
  const todayStart = new Date(today.getFullYear(), today.getMonth(), today.getDate()).getTime()
  const parsed = new Date(dateKey + 'T00:00:00')
  const dayDiff = Math.round((parsed.getTime() - todayStart) / 86_400_000)

  if (dayDiff === 0) {
    return capitalize(rtf.format(0, 'day'))
  }
  if (dayDiff === -1) {
    return capitalize(rtf.format(-1, 'day'))
  }

  const sameYear = parsed.getFullYear() === today.getFullYear()
  return parsed.toLocaleDateString(locale, {
    year: sameYear ? undefined : 'numeric',
    month: 'short',
    day: 'numeric',
  })
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}
