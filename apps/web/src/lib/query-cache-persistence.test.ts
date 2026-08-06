import { describe, expect, it } from 'vitest'
import type { UseQueryEntry } from '@pinia/colada'
import { QUERY_CACHE_STORAGE_KEY, queryCachePersistFilter } from './query-cache-persistence'

// The predicate only reads entry.key[0] and entry.state.value.status, so a
// minimal stub beats constructing a full UseQueryEntry.
function entryWith(key: readonly unknown[], status: 'success' | 'pending' | 'error' = 'success') {
  return { key, state: { value: { status } } } as unknown as UseQueryEntry
}

const { predicate } = queryCachePersistFilter as {
  predicate: (entry: UseQueryEntry) => boolean
}

describe('queryCachePersistFilter', () => {
  it('persists whitelisted catalog and config namespaces', () => {
    const allowed = [
      ['models'],
      ['providers'],
      ['provider-models', 'provider-1'],
      ['memory-providers'],
      ['search-providers-meta'],
      ['speech-provider-models', 'sp-1'],
      ['transcription-providers'],
      ['video-provider-detail', 'vp-1'],
      ['acp-profiles'],
      ['channels'],
      ['connectors-catalog'],
      ['remote-runtimes'],
      ['platform'],
      ['bot', 'bot-1'],
      ['bot-settings', 'bot-1'],
    ]
    for (const key of allowed) {
      expect(predicate(entryWith(key)), `expected ${key[0]} to persist`).toBe(true)
    }
  })

  it('persists the SDK-object bots list key', () => {
    expect(predicate(entryWith([{ _id: 'getBots', baseUrl: 'http://x' }]))).toBe(true)
  })

  it('rejects volatile and unknown namespaces', () => {
    const rejected = [
      ['session-status', 'b', 's'],
      ['session-subagents', 'b', 's'],
      ['token-usage', 'b'],
      ['bot-email-outbox', 'b'],
      ['bot-container-overview', 'b'],
      ['bot-display-info', 'b'],
      ['bot-network-status', 'b'],
      ['bot-memory-status', 'b'],
      ['my-channel-identities'],
      ['user-has-im-bot'],
      ['something-new'],
    ]
    for (const key of rejected) {
      expect(predicate(entryWith(key)), `expected ${key[0]} to be excluded`).toBe(false)
    }
  })

  it('rejects object keys with other operations', () => {
    expect(predicate(entryWith([{ _id: 'getBotsByBotIdSessions', baseUrl: 'http://x' }]))).toBe(false)
  })

  it('rejects non-success entries even for whitelisted keys', () => {
    expect(predicate(entryWith(['models'], 'pending'))).toBe(false)
    expect(predicate(entryWith(['models'], 'error'))).toBe(false)
  })
})

describe('QUERY_CACHE_STORAGE_KEY', () => {
  it('is the contract auth-session.ts clears on auth changes', () => {
    expect(QUERY_CACHE_STORAGE_KEY).toBe('memoh:query-cache')
  })
})
