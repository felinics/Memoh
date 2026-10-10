import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import { useComposerPrefillStore } from './composer-prefill'

describe('composer prefill store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('is consumed once by the requested bot', () => {
    const store = useComposerPrefillStore()
    store.request('bot-b', 'hello')
    expect(store.take('bot-b')).toBe('hello')
    expect(store.take('bot-b')).toBeNull()
  })

  it('is not consumed by another bot', () => {
    const store = useComposerPrefillStore()
    store.request('bot-b', 'hello')
    expect(store.take('bot-a')).toBeNull()
    expect(store.pending?.botId).toBe('bot-b')
  })

  it('keeps only the latest request', () => {
    const store = useComposerPrefillStore()
    store.request('bot-a', 'first')
    store.request('bot-a', 'second')
    expect(store.take('bot-a')).toBe('second')
  })

  it('gives each request a new id so watchers re-run for identical text', () => {
    const store = useComposerPrefillStore()
    store.request('bot-a', 'same')
    const first = store.pending?.id
    store.request('bot-a', 'same')
    expect(store.pending?.id).not.toBe(first)
  })

  it('drops the request on clear', () => {
    const store = useComposerPrefillStore()
    store.request('bot-a', 'hello')
    store.clear()
    expect(store.take('bot-a')).toBeNull()
  })
})
