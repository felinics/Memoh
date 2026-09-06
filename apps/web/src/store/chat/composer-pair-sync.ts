import { ref } from 'vue'

// Shared by every panel of a session. Reads may refresh confirmed cache, but
// never a choice/send made after the read began. Picker writes are serialized;
// their server-side revision check also fences requests overtaken by a send.
export function createComposerPairSync() {
  let epoch = 0
  let read = 0
  let dirty = false
  let readGeneration = 0
  let readHolds = 0
  let writes = Promise.resolve()
  let sending = Promise.resolve()
  const refreshing = ref(false)

  async function refresh<T>(load: () => Promise<T>, apply: (value: T) => void) {
    if (dirty || readHolds) return
    const generation = readGeneration
    const operation = epoch
    const request = ++read
    refreshing.value = true
    try {
      const value = await load()
      if (operation === epoch && request === read && generation === readGeneration && !dirty && !readHolds) apply(value)
    } catch { /* Keep the displayed cache when offline. */ }
    finally { if (request === read) refreshing.value = false }
  }

  function write<T>(load: () => Promise<T>, save: (value: T) => Promise<T>, apply: (value: T) => void) {
    const operation = ++epoch
    dirty = true
    const barrier = sending
    writes = writes.then(async () => {
      await barrier
      if (operation !== epoch) return
      const current = await load()
      if (operation !== epoch) return
      const saved = await save(current)
      if (operation !== epoch) return
      apply(saved)
      dirty = false
    }).catch(() => { /* Keep the optimistic choice; the next send retries it. */ })
    return writes
  }

  // Snapshot preparation can await attachments or resolve to a command. Block
  // stale reads immediately without cancelling or confirming picker writes.
  function holdReads() {
    readGeneration++
    readHolds++
    let released = false
    return () => {
      if (released) return
      released = true
      readHolds--
    }
  }

  // The pair's runtime namespace changed (Agent / runtime switch on the same
  // view): whatever the old namespace had in flight — a picker write, a
  // pending read — must not land on the new one. The next refresh is allowed
  // immediately.
  function invalidate() {
    epoch++
    dirty = false
  }

  // Reserve ordering at snapshot time, before attachment conversion. Choices
  // made afterwards wait for this send (or its cancellation) and must not be
  // invalidated when that older snapshot is finally admitted.
  function prepareSend() {
    const snapshotEpoch = epoch
    const releaseReads = holdReads()
    let release!: () => void
    const pending = new Promise<void>((resolve) => { release = resolve })
    sending = Promise.all([sending, pending]).then(() => {})
    let operation: number | undefined
    return {
      begin() {
        if (epoch === snapshotEpoch) operation = ++epoch
        dirty = true
      },
      finish(confirmed: boolean) {
        if (operation === epoch && confirmed) dirty = false
        release()
      },
      release() {
        releaseReads()
        release()
      },
    }
  }

  function beginSend() {
    const send = prepareSend()
    send.begin()
    return (confirmed: boolean) => {
      send.finish(confirmed)
      send.release()
    }
  }

  return { refreshing, refresh, write, holdReads, beginSend, prepareSend, invalidate }
}
