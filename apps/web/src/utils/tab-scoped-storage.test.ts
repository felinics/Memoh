import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { useTabScopedStorage } from './tab-scoped-storage'

// Verifies the two non-obvious invariants of the seed:
//   1. cold start (empty sessionStorage) reads the localStorage seed ONCE —
//      this is the migration path for an existing localStorage value;
//   2. a live sessionStorage value wins over the seed and later writes mirror
//      back into localStorage, so no cross-tab listener is ever attached.
describe('useTabScopedStorage', () => {
  let ls: Map<string, string>
  let ss: Map<string, string>

  const mock = (m: Map<string, string>) => ({
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => m.set(k, v),
    removeItem: (k: string) => m.delete(k),
    clear: () => m.clear(),
    key: () => null,
    get length() { return m.size },
  })

  beforeEach(() => {
    ls = new Map(); ss = new Map()
    vi.stubGlobal('localStorage', mock(ls))
    vi.stubGlobal('sessionStorage', mock(ss))
  })
  afterEach(() => vi.unstubAllGlobals())

  it('cold start seeds an object from localStorage (migration path)', () => {
    ls.set('k', JSON.stringify({ a: 1 })) // useStorage stores objects as JSON
    const r = useTabScopedStorage<{ a: number }>('k', { a: 0 }, { seed: true })
    expect(r.value).toEqual({ a: 1 })
  })

  it('cold start seeds a bare string from localStorage (migration path)', () => {
    ls.set('k', 'legacy') // useStorage stores strings bare — must NOT be JSON.parsed
    const r = useTabScopedStorage<string>('k', 'def', { seed: true })
    expect(r.value).toBe('legacy')
  })

  it('a live sessionStorage value wins over the seed', () => {
    ls.set('k', 'seed')
    ss.set('k', 'live')
    const r = useTabScopedStorage<string>('k', 'def', { seed: true })
    expect(r.value).toBe('live')
  })

  it('mirrors writes into the localStorage seed', async () => {
    const r = useTabScopedStorage<string>('k', 'def', { seed: true })
    r.value = 'next'
    await nextTick()
    expect(ls.get('k')).toBe('next') // raw mirror, byte-identical to sessionStorage
  })

  it('without seed, ignores localStorage entirely', () => {
    ls.set('k', 'seed')
    const r = useTabScopedStorage<string>('k', 'def')
    expect(r.value).toBe('def')
  })

  it('falls back to defaults when seeding into sessionStorage throws', () => {
    ls.set('k', 'seed')
    vi.stubGlobal('sessionStorage', {
      ...mock(ss),
      setItem: () => {
        throw new DOMException('QuotaExceededError')
      },
    })
    const r = useTabScopedStorage<string>('k', 'def', { seed: true })
    expect(r.value).toBe('def')
  })
})
