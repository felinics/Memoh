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
  }

  function remove(sessionId: string) {
    if (!(sessionId in entries.value)) return
    const next = { ...entries.value }
    delete next[sessionId]
    entries.value = next
  }

  /** Removes the entry only when the read cursor covers its attention. */
  function removeIfReadThrough(sessionId: string, readAt: string) {
    const entry = entries.value[sessionId]
    if (entry && Date.parse(readAt) >= Date.parse(entry.attentionAt)) remove(sessionId)
  }

  async function refetch(expected: number) {
    try {
      const items = await fetchUnreadSessions()
      if (expected !== generation) return
      const next: Record<string, UnreadEntry> = {}
      for (const item of items) next[item.session_id] = fromWire(item)
      entries.value = next
    } catch (error) {
      console.error('Failed to load unread sessions:', error)
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
    const expected = ++generation
    void refetch(expected)
    stream.start(signal => streamUserAttentionEvents(signal, event => handle(event, expected)))
  }

  function stop() {
    generation += 1
    stream.stop()
  }

  function reset() {
    stop()
    entries.value = {}
  }

  async function markRead(sessionId: string) {
    const entry = entries.value[sessionId]
    if (!entry) return
    try {
      await markSessionRead(entry.botId, sessionId, entry.attentionAt)
      removeIfReadThrough(sessionId, entry.attentionAt)
    } catch (error) {
      console.error('Failed to mark session read:', error)
    }
  }


  function onAttention(listener: (entry: UnreadEntry) => void) {
    listeners.add(listener)
    return () => listeners.delete(listener)
  }

  return { entries, total, countByBot, inbox, isUnread, kindOf, start, stop, reset, markRead, onAttention }
})
