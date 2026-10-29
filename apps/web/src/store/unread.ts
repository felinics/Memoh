import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import {
  fetchUnreadSessions,
  markSessionRead,
  streamUserAttentionEvents,
  type AttentionKind,
  type UnreadSessionWire,
  type UserAttentionEvent,
} from '@/composables/api/attention-api'
import { useRetryingStream } from '@/composables/useRetryingStream'

export type { AttentionKind } from '@/composables/api/attention-api'

/** How long after a live arrival a newly rendered mark still plays its arrival motion. */
const FRESH_ATTENTION_MS = 2000

export interface UnreadEntry {
  sessionId: string
  botId: string
  title: string
  kind: AttentionKind
  attentionAt: string
}


function fromWire(item: UnreadSessionWire): UnreadEntry {
  return {
    sessionId: item.session_id,
    botId: item.bot_id,
    title: item.title,
    kind: item.attention_kind,
    attentionAt: item.attention_at,
  }
}


/**
 * The current user's unread sessions across every bot of the team. It is
 * seeded from the unread summary, kept live by the per-user attention stream,
 * and is the single source for dots, the "Needs you" inbox, counts and
 * notifications. Only live `session_attention` frames reach `onAttention`
 * listeners, so a page load never replays notifications.
 */
export const useUnreadStore = defineStore('unread', () => {
  const entries = ref<Record<string, UnreadEntry>>({})
  const listeners = new Set<(entry: UnreadEntry) => void>()
  const stream = useRetryingStream()
  let generation = 0
  let snapshotVersion = 0
  let pendingChanges: Map<string, UnreadEntry | null> | null = null
  const seen = new Map<string, number>()
  const reads = new Map<string, number>()
  const reading = new Map<string, Promise<void>>()
  let running = false
  const arrivals = new Map<string, number>()

  const total = computed(() => Object.keys(entries.value).length)
  const countByBot = computed(() => {
    const counts: Record<string, number> = {}
    for (const entry of Object.values(entries.value)) {
      counts[entry.botId] = (counts[entry.botId] ?? 0) + 1
    }
    return counts
  })
  const inbox = computed(() => Object.values(entries.value)
    .sort((a, b) => Date.parse(b.attentionAt) - Date.parse(a.attentionAt)))

  function isUnread(sessionId: string) {
    return sessionId in entries.value
  }

  function kindOf(sessionId: string): AttentionKind | null {
    return entries.value[sessionId]?.kind ?? null
  }

  function put(entry: UnreadEntry) {
    entries.value = { ...entries.value, [entry.sessionId]: entry }
    pendingChanges?.set(entry.sessionId, entry)
  }

  function remove(sessionId: string) {
    if (!(sessionId in entries.value)) return
    const next = { ...entries.value }
    delete next[sessionId]
    entries.value = next
  }

  /** Removes the entry only when the read cursor covers its attention. */
  function removeIfReadThrough(sessionId: string, readAt: string) {
    const through = Math.max(reads.get(sessionId) ?? 0, Date.parse(readAt))
    reads.set(sessionId, through)
    const entry = entries.value[sessionId]
    if (!entry || through >= Date.parse(entry.attentionAt)) {
      remove(sessionId)
      pendingChanges?.set(sessionId, null)
    }
  }

  /** Replay concurrent stream changes over the snapshot so a slower GET cannot undo them. */
  async function refetch(expected: number) {
    const version = ++snapshotVersion
    const changes = new Map<string, UnreadEntry | null>()
    pendingChanges = changes
    try {
      const items = await fetchUnreadSessions()
      if (expected !== generation || version !== snapshotVersion) return
      const next: Record<string, UnreadEntry> = {}
      for (const item of items) {
        const at = Date.parse(item.attention_at)
        if ((reads.get(item.session_id) ?? 0) >= at) continue
        next[item.session_id] = fromWire(item)
        seen.set(item.session_id, Math.max(seen.get(item.session_id) ?? 0, at))
      }
      for (const [id, entry] of changes) {
        if (entry) next[id] = entry
        else delete next[id]
      }
      entries.value = next
    } catch (error) {
      console.error('Failed to load unread sessions:', error)
    } finally {
      if (pendingChanges === changes) pendingChanges = null
    }
  }

  function handle(event: UserAttentionEvent, expected: number) {
    if (expected !== generation) return
    switch (event.type) {
      case 'attention_ready':
        void refetch(expected)
        return
      case 'session_attention': {
        const entry = fromWire(event)
        const at = Date.parse(entry.attentionAt)
        if (at <= Math.max(seen.get(entry.sessionId) ?? 0, reads.get(entry.sessionId) ?? 0)) return
        seen.set(entry.sessionId, at)
        const arrivedAt = Date.now()
        for (const [key, time] of arrivals) if (arrivedAt - time >= FRESH_ATTENTION_MS) arrivals.delete(key)
        arrivals.set(`${entry.sessionId}@${entry.attentionAt}`, arrivedAt)
        put(entry)
        for (const listener of listeners) listener(entry)
        return
      }
      case 'session_read':
        removeIfReadThrough(event.session_id, event.read_at)
        return
      case 'dropped':
        void refetch(expected)
    }
  }

  function start() {
    if (running) return
    running = true
    const expected = ++generation
    void refetch(expected)
    stream.start(signal => streamUserAttentionEvents(signal, event => handle(event, expected)))
  }

  function stop() {
    running = false
    generation += 1
    pendingChanges = null
    stream.stop()
  }

  function reset() {
    stop()
    entries.value = {}
    seen.clear()
    reads.clear()
    reading.clear()
    arrivals.clear()
  }

  /**
   * Whether this attention arrived live a moment ago. Arrival motion keys off this,
   * not off mounting, so expanding a list, reloading or switching bots never replays it.
   */
  function isFresh(sessionId: string, attentionAt: string) {
    const arrived = arrivals.get(`${sessionId}@${attentionAt}`)
    return arrived !== undefined && Date.now() - arrived < FRESH_ATTENTION_MS
  }

  /** Coalesce viewers of the same session; a newer attention is read by the next watcher pass. */
  function markRead(sessionId: string): Promise<void> {
    const entry = entries.value[sessionId]
    if (!entry) return Promise.resolve()
    const key = `${sessionId}@${entry.attentionAt}`
    const pending = reading.get(key)
    if (pending) return pending
    const expected = generation
    const request = markSessionRead(entry.botId, sessionId, entry.attentionAt)
      .then(() => {
        if (expected === generation) removeIfReadThrough(sessionId, entry.attentionAt)
      })
      .catch(error => console.error('Failed to mark session read:', error))
      .finally(() => {
        if (reading.get(key) === request) reading.delete(key)
      })
    reading.set(key, request)
    return request
  }


  function onAttention(listener: (entry: UnreadEntry) => void) {
    listeners.add(listener)
    return () => listeners.delete(listener)
  }

  return { entries, total, countByBot, inbox, isUnread, kindOf, isFresh, start, stop, reset, markRead, onAttention }
})
