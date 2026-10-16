import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import { useComposerPrefillStore } from './composer-prefill'

describe('composer prefill store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('is taken once by the requested bot', () => {
    const store = useComposerPrefillStore()
    void store.request('bot-b', 'hello')
    expect(store.take('bot-b')?.text).toBe('hello')
    expect(store.take('bot-b')).toBeNull()
  })

  it('is not taken by another bot', () => {
    const store = useComposerPrefillStore()
    void store.request('bot-b', 'hello')
    expect(store.take('bot-a')).toBeNull()
    expect(store.pending?.botId).toBe('bot-b')
  })

  it('resolves with the outcome its consumer settles', async () => {
    const store = useComposerPrefillStore()
    const outcome = store.request('bot-a', 'hello')
    store.take('bot-a')?.settle('applied')
    await expect(outcome).resolves.toBe('applied')
  })

  it('keeps only the latest request and cancels the one it replaces', async () => {
    const store = useComposerPrefillStore()
    const first = store.request('bot-a', 'first')
    void store.request('bot-a', 'second')
    await expect(first).resolves.toBe('cancelled')
    expect(store.take('bot-a')?.text).toBe('second')
  })

  it('cancels the pending request on clear', async () => {
    const store = useComposerPrefillStore()
    const outcome = store.request('bot-a', 'hello')
    store.clear()
    await expect(outcome).resolves.toBe('cancelled')
    expect(store.take('bot-a')).toBeNull()
  })

  it('settles only once', async () => {
    const store = useComposerPrefillStore()
    const outcome = store.request('bot-a', 'hello')
    const taken = store.take('bot-a')!
    taken.settle('cancelled')
    taken.settle('applied')
    await expect(outcome).resolves.toBe('cancelled')
  })

  it('gives each request a new id so watchers re-run for identical text', () => {
    const store = useComposerPrefillStore()
    void store.request('bot-a', 'same')
    const first = store.pending?.id
    void store.request('bot-a', 'same')
    expect(store.pending?.id).not.toBe(first)
  })
})
