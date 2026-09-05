import { describe, expect, it } from 'vitest'
import { createComposerPairSync } from './composer-pair-sync'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(r => { resolve = r })
  return { promise, resolve }
}
const tick = async () => { for (let n = 0; n < 6; n++) await Promise.resolve() }

describe('composer preference operation ordering', () => {
  it('shows cache while refreshing, then adopts the server', async () => {
    const state = createComposerPairSync()
    const request = deferred<string>()
    let display = 'cached'
    const read = state.refresh(() => request.promise, v => { display = v })
    expect(display).toBe('cached')
    expect(state.refreshing.value).toBe(true)
    request.resolve('server')
    await read
    expect(display).toBe('server')
  })
  it('keeps a send snapshot when an older read arrives', async () => {
    const state = createComposerPairSync()
    const request = deferred<string>()
    let display = 'A'
    const read = state.refresh(() => request.promise, v => { display = v })
    const sent = display
    const finish = state.beginSend()
    request.resolve('B')
    await read
    finish(true)
    expect(sent).toBe('A')
    expect(display).toBe('A')
    await state.refresh(async () => 'new confirmed', v => { display = v })
    expect(display).toBe('new confirmed')
  })
  it('serializes PATCHes and never starts an obsolete queued selection', async () => {
    const state = createComposerPairSync()
    const first = deferred<string>()
    const writes: string[] = []
    let displayed = ''
    const a = state.write(async () => '', async () => { writes.push('A'); return first.promise }, v => { displayed = v })
    await tick()
    const b = state.write(async () => '', async () => { writes.push('B'); return 'B' }, v => { displayed = v })
    const c = state.write(async () => '', async () => { writes.push('C'); return 'C' }, v => { displayed = v })
    first.resolve('A')
    await Promise.all([a, b, c])
    expect(writes).toEqual(['A', 'C'])
    expect(displayed).toBe('C')
  })
  it('does not PATCH if a send overtakes the revision read', async () => {
    const state = createComposerPairSync()
    const revision = deferred<string>()
    let saved = false
    const patch = state.write(() => revision.promise, async v => { saved = true; return v }, () => {})
    await tick()
    const finish = state.beginSend()
    revision.resolve('old revision')
    await patch
    finish(true)
    expect(saved).toBe(false)
  })
  it('protects a new selection during a send and persists it after the send', async () => {
    const state = createComposerPairSync()
    const finish = state.beginSend()
    let writes = 0
    const patch = state.write(async () => 'revision', async () => { writes++; return 'B' }, () => {})
    await tick()
    expect(writes).toBe(0)
    finish(true)
    await patch
    expect(writes).toBe(1)
  })
  it('does not overwrite a dirty pick with a cache refresh', async () => {
    const state = createComposerPairSync()
    await state.write(async () => '', async () => { throw new Error('offline') }, () => {})
    let applied = false
    await state.refresh(async () => 'old', () => { applied = true })
    expect(applied).toBe(false)
  })
})
