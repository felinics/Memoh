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
  it('protects the displayed snapshot while attachments are being prepared', async () => {
    const state = createComposerPairSync()
    const request = deferred<string>()
    const attachments = deferred<void>()
    let display = 'A'
    const read = state.refresh(() => request.promise, v => { display = v })
    const snapshot = display
    const release = state.holdReads()
    let sent = ''
    const send = (async () => {
      await attachments.promise
      const finish = state.beginSend()
      sent = snapshot
      finish(true)
      release()
    })()
    request.resolve('B')
    await read
    expect(display).toBe('A')
    expect(sent).toBe('')
    attachments.resolve()
    await send
    expect(sent).toBe(display)
    expect(sent).toBe('A')
  })
  it('discards a pre-snapshot read even if it returns after preparation ends', async () => {
    const state = createComposerPairSync()
    const request = deferred<string>()
    let display = 'A'
    const read = state.refresh(() => request.promise, v => { display = v })
    const release = state.holdReads()
    release()
    request.resolve('B')
    await read
    expect(display).toBe('A')
    expect(state.refreshing.value).toBe(false)
    await state.refresh(async () => 'C', v => { display = v })
    expect(display).toBe('C')
  })
  it('resumes refresh after preparation fails and releases overlapping holds independently', async () => {
    const state = createComposerPairSync()
    let display = 'A'
    const first = state.holdReads()
    const second = state.holdReads()
    first()
    first()
    await state.refresh(async () => 'B', v => { display = v })
    expect(display).toBe('A')
    second()
    await state.refresh(async () => 'C', v => { display = v })
    expect(display).toBe('C')
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
  it('a failed send does not wedge later refreshes', async () => {
    const state = createComposerPairSync()
    const finish = state.beginSend()
    finish(false) // startup failure: nothing reached the server
    let applied = false
    await state.refresh(async () => 'server', () => { applied = true })
    expect(applied).toBe(true)
  })
  it('onError runs after the write settles, so a conflict handler can refresh immediately', async () => {
    const state = createComposerPairSync()
    let applied = ''
    const write = state.write(
      async () => 'rev',
      async () => { throw new Error('conflict') },
      () => {},
      () => {
        state.dropUnsavedChoice()
        void state.refresh(async () => 'winner', (v) => { applied = v })
      },
    )
    await write
    await tick()
    expect(applied).toBe('winner')
  })
})

describe('composer preference invalidation', () => {
  it('drops in-flight reads and writes and allows the next refresh at once', async () => {
    const state = createComposerPairSync()
    const read = deferred<string>()
    let display = 'old'
    const refresh = state.refresh(() => read.promise, v => { display = v })
    let saved = false
    const load = deferred<string>()
    const write = state.write(() => load.promise, async v => { saved = true; return v }, v => { display = v })
    state.invalidate()
    read.resolve('stale runtime')
    load.resolve('stale runtime')
    await refresh
    await write
    expect(display).toBe('old')
    expect(saved).toBe(false)
    await state.refresh(async () => 'new runtime', v => { display = v })
    expect(display).toBe('new runtime')
  })
})


describe('prepared send cancellation', () => {
  it('keeps an earlier picker write when preparation never becomes a message', async () => {
    const state = createComposerPairSync()
    const read = deferred<string>()
    let saved = ''
    const write = state.write(() => read.promise, async value => value, value => { saved = value })
    await tick()
    const send = state.prepareSend()
    send.finish(false)
    send.release()
    read.resolve('picked')
    await write
    expect(saved).toBe('picked')
  })

  it('resumes preference refresh after a command returns without starting a turn', async () => {
    const state = createComposerPairSync()
    let display = 'A'
    const command = state.prepareSend()
    // /help returns before onBeforeMessageSend; the pane still runs finally.
    command.finish(false)
    command.release()
    command.finish(false)
    await state.refresh(async () => 'B', value => { display = value })
    expect(display).toBe('B')

    const finish = state.beginSend()
    finish(true)
    await state.refresh(async () => 'C', value => { display = value })
    expect(display).toBe('C')
  })

  it('counts each admitted send once even if lifecycle callbacks repeat', async () => {
    const state = createComposerPairSync()
    const send = state.prepareSend()
    send.begin()
    send.begin()
    send.finish(false)
    send.finish(true)
    send.release()
    let refreshed = false
    await state.refresh(async () => 'server', () => { refreshed = true })
    expect(refreshed).toBe(true)
  })

  it('lets a confirmed retry clear the failed choice it actually carried', async () => {
    const state = createComposerPairSync()
    await state.write(async () => '', async () => { throw new Error('offline') }, () => {})
    const finish = state.beginSend()
    finish(false)
    finish(true)
    let refreshed = false
    await state.refresh(async () => 'confirmed pair', () => { refreshed = true })
    expect(refreshed).toBe(true)
  })

  it.each(['send', 'retry/edit'] as const)('preserves a newer failed pick when %s completes after preference settlement', async (path) => {
    const state = createComposerPairSync()
    let display = 'A'
    const send = path === 'send' ? state.prepareSend() : null
    const finish = send ? send.finish : state.beginSend()
    send?.begin()
    finish(false) // model_preference_settled releases the queued picker write.
    display = 'B'
    await state.write(async () => '', async () => { throw new Error('offline') }, value => { display = value })
    finish(false) // pane finally
    send?.release()
    finish(true) // terminal result for the older A request
    await state.refresh(async () => 'A', value => { display = value })
    expect(display).toBe('B')

    const retry = state.beginSend()
    retry(true)
    await state.refresh(async () => 'B confirmed', value => { display = value })
    expect(display).toBe('B confirmed')
  })

  it('does not confirm a failed choice from a replacement runtime', async () => {
    const state = createComposerPairSync()
    const finish = state.beginSend()
    finish(false)
    state.invalidate()
    await state.write(async () => '', async () => { throw new Error('offline') }, () => {})
    finish(true)
    let refreshed = false
    await state.refresh(async () => 'new runtime default', () => { refreshed = true })
    expect(refreshed).toBe(false)
  })

  it('does not confirm a newer failed pick when the older send succeeds', async () => {
    const state = createComposerPairSync()
    const send = state.prepareSend()
    const write = state.write(async () => '', async () => { throw new Error('offline') }, () => {})
    send.begin()
    send.finish(true)
    send.release()
    await write
    let refreshed = false
    await state.refresh(async () => 'old pair', () => { refreshed = true })
    expect(refreshed).toBe(false)
  })
})
