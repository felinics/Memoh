import { reactive } from 'vue'
import type { ChatViewTarget } from './types'

// The lifecycle of the first message sent from an empty draft. Nothing of the
// send is shown until the server confirms it: the turns, the draft -> session
// promotion, the sidebar row and the composer leaving welcome all happen at
// run_accepted. Before that the pane stays on welcome with the composer locked
// and the input still in it, so a failure has nothing on screen to undo; the
// input and the error are simply shown in place. The store updates the
// entry; panes only read it.
//
// The server confirms in two events, in either order: session_created names
// the session (sessionId) and run_accepted takes the run (accepted). The send
// is revealed once both have arrived. No entry means no first send. Until the
// send is revealed the session it created is tentative (see
// isSessionTentative). Entries are keyed by (bot, view id) because the
// promotion (draft -> session) keeps the pane's view id, so the entry
// survives that rebind.
export interface FirstSendEntry {
  readonly key: string
  readonly invocationId: string
  // run_accepted arrived.
  accepted: boolean
  // The workdir this send asked the server to bind the new session to. Empty
  // when none was requested.
  readonly requestedWorkdirId: string
  // The session the server created for this send, and the workdir it bound.
  // Empty until session_created.
  sessionId: string
  workdirId: string
  // True once the held turns are on screen (run_accepted). From here on the
  // view is an ordinary session and a failure stays in its history.
  revealed: boolean
}

export function firstSendKey(botId: string, viewId: string): string {
  return `${botId.trim()}:${viewId.trim()}`
}

export function createFirstSendTracker() {
  const entries = reactive(new Map<string, FirstSendEntry>())

  function entryForInvocation(invocationId: string): FirstSendEntry | undefined {
    const id = invocationId.trim()
    if (!id) return undefined
    for (const entry of entries.values()) {
      if (entry.invocationId === id) return entry
    }
    return undefined
  }

  // A new first send replaces whatever the view still held: the send guards
  // only admit one send per view.
  // `reveal` puts the held turns on screen; it runs once, at confirmation.
  const reveals = new Map<string, () => void>()

  function begin(
    target: ChatViewTarget,
    invocationId: string,
    requestedWorkdirId: string,
    reveal: () => void,
  ) {
    const key = firstSendKey(target.botId, target.viewId)
    const previous = entries.get(key)
    if (previous) reveals.delete(previous.invocationId)
    const id = invocationId.trim()
    reveals.set(id, reveal)
    entries.set(key, {
      key,
      invocationId: id,
      accepted: false,
      requestedWorkdirId: requestedWorkdirId.trim(),
      sessionId: '',
      workdirId: '',
      revealed: false,
    })
  }

  function accept(invocationId: string) {
    const entry = entryForInvocation(invocationId)
    if (entry) entry.accepted = true
  }

  // The server named the send's session (session_created, or the REST
  // creation that preceded the send). Events can repeat (a reconnect replays
  // reliable requests); the first name stands.
  function admit(invocationId: string, sessionId: string, workdirId = '') {
    const entry = entryForInvocation(invocationId)
    if (!entry || entry.sessionId) return
    entry.sessionId = sessionId.trim()
    entry.workdirId = workdirId.trim()
  }

  // Puts the held turns on screen. Returns false when there is nothing to
  // reveal (no such send, or already revealed).
  function reveal(invocationId: string): boolean {
    const entry = entryForInvocation(invocationId)
    if (!entry || entry.revealed) return false
    entry.revealed = true
    const show = reveals.get(entry.invocationId)
    reveals.delete(entry.invocationId)
    show?.()
    return true
  }

  function isRevealed(invocationId: string): boolean {
    return entryForInvocation(invocationId)?.revealed === true
  }

  // A held send, i.e. one the server has not confirmed yet.
  function isAwaitingConfirmation(invocationId: string): boolean {
    const entry = entryForInvocation(invocationId)
    return !!entry && !entry.revealed
  }

  function awaitingConfirmationIds(): string[] {
    return [...entries.values()].filter(entry => !entry.revealed).map(entry => entry.invocationId)
  }

  // A first send's session exists server-side from admission, but until the
  // server confirms the run it is the send's to delete again on failure.
  // Until then, session lists leave it out (an activity event can announce it
  // first) and titles show the draft it came from.
  function isSessionTentative(sessionId: string): boolean {
    const id = sessionId.trim()
    if (!id) return false
    for (const entry of entries.values()) {
      if (entry.sessionId === id) return !entry.revealed
    }
    return false
  }

  function requestedWorkdirFor(invocationId: string): string {
    return entryForInvocation(invocationId)?.requestedWorkdirId ?? ''
  }

  // Terminal: the send succeeded, was stopped, failed after it was revealed
  // (the view is an ordinary session from here on), or failed before it (the
  // view is still the draft it was at Enter).
  function finish(invocationId: string) {
    const entry = entryForInvocation(invocationId)
    if (!entry) return
    reveals.delete(entry.invocationId)
    entries.delete(entry.key)
  }

  function entryFor(target: Pick<ChatViewTarget, 'botId' | 'viewId'>): FirstSendEntry | undefined {
    const botId = target.botId?.trim() ?? ''
    const viewId = target.viewId?.trim() ?? ''
    if (!botId || !viewId) return undefined
    return entries.get(firstSendKey(botId, viewId))
  }

  function reset() {
    entries.clear()
    reveals.clear()
  }

  return {
    begin,
    accept,
    admit,
    reveal,
    isRevealed,
    isAwaitingConfirmation,
    awaitingConfirmationIds,
    isSessionTentative,
    requestedWorkdirFor,
    entryForInvocation,
    finish,
    entryFor,
    reset,
  }
}

export type FirstSendTracker = ReturnType<typeof createFirstSendTracker>
