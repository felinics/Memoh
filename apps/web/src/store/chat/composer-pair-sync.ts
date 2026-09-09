import { ref } from 'vue'

// Shared by every panel of a session. Reads may refresh confirmed cache, but
// never over an in-flight or unsaved choice. Picker writes are serialized;
// their server-side revision check also fences requests overtaken by a send.
//
// Two distinct gates:
// - `pending` counts in-flight writes/sends. It MUST return to zero on every
//   settle — success, failure, or invalidation alike. A stuck positive count
//   permanently disabled refresh for the view (a past bug: any failed send,
//   even one carrying no pick, left the flag set forever).
// - `unsaved` marks a settled picker write that never reached the server.
//   The optimistic choice stays displayed and the next send retries it, so
//   reads keep their hands off until then. Only a confirmed send, a
//   successful write, a conflict revert (dropUnsavedChoice), or a runtime
//   invalidate clears it.
export function createComposerPairSync() {
  let epoch = 0
  let read = 0
  let pending = 0
  let unsaved = false
  let readGeneration = 0
  let readHolds = 0
  let writes = Promise.resolve()
  let sending = Promise.resolve()
  const refreshing = ref(false)

  async function refresh<T>(load: () => Promise<T>, apply: (value: T) => void) {
    if (pending || unsaved || readHolds) return
    const generation = readGeneration
    const operation = epoch
    const request = ++read
    refreshing.value = true
    try {
      const value = await load()
      if (operation === epoch && request === read && generation === readGeneration && !pending && !unsaved && !readHolds) apply(value)
    } catch { /* Keep the displayed cache when offline. */ }
    finally { if (request === read) refreshing.value = false }
  }

  function write<T>(
    load: () => Promise<T>,
    save: (value: T) => Promise<T>,
    apply: (value: T) => void,
    onError?: (error: unknown) => void,
  ) {
    const operation = ++epoch
    pending++
    const barrier = sending
    writes = writes.then(async () => {
      let error: unknown
      let failed = false
      try {
        await barrier
        if (operation !== epoch) return
        const current = await load()
        if (operation !== epoch) return
        const saved = await save(current)
        if (operation !== epoch) return
        apply(saved)
        unsaved = false
      } catch (cause) {
        failed = true
        error = cause
        // Keep the optimistic choice; the next send retries it. A conflict
        // is different — the choice lost to a newer write — so the caller
        // reverts via dropUnsavedChoice() from onError.
        unsaved = true
      } finally {
        // Decrement BEFORE onError so a conflict handler can refresh
        // immediately (refresh refuses to run while a write is in flight).
        pending--
      }
      if (failed) onError?.(error)
    })
    return writes
  }

  // The displayed choice lost the revision race (409): drop the unsaved
  // protection so the next refresh shows the server's winning pair. Callers
  // follow with a refresh and a user-facing conflict hint.
  function dropUnsavedChoice() {
    unsaved = false
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
  // pending read, an unsaved choice — must not land on the new one. In-flight
  // operations stay fenced by the epoch bump and still decrement pending when
  // they settle; resetting the counter here would double-count them.
  function invalidate() {
    epoch++
    unsaved = false
  }

  // Reserve ordering at snapshot time, before attachment conversion. Choices
  // made afterwards wait for this send (or its cancellation) and must not be
  // invalidated when that older snapshot is finally admitted.
  function prepareSend() {
    const snapshotEpoch = epoch
    const releaseReads = holdReads()
    let release!: () => void
    const pendingWrite = new Promise<void>((resolve) => { release = resolve })
    sending = Promise.all([sending, pendingWrite]).then(() => {})
    let started = false
    let settled = false
    let confirmationEpoch: number | undefined
    return {
      begin() {
        if (started || settled) return
        started = true
        // The epoch bump is the fence: reads/writes started before this send
        // was admitted must not land after it. A send admitted after a newer
        // choice does not bump — the newer operation owns the epoch.
        if (epoch === snapshotEpoch) confirmationEpoch = ++epoch
        pending++
      },
      finish(confirmed: boolean) {
        // Idempotent: the pane finishes once at settle (success or failure)
        // and again from its finally safety net.
        if (!settled) {
          settled = true
          if (started) pending--
        }
        // Persistence may settle before generation ends. The later success
        // confirms only this snapshot, never a newer picker choice or runtime.
        if (confirmed && confirmationEpoch === epoch) unsaved = false
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

  return { refreshing, refresh, write, dropUnsavedChoice, holdReads, beginSend, prepareSend, invalidate }
}
