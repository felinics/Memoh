import { reactive } from 'vue'
import { hasVisibleAssistantBlocks } from './transcript'
import type { ChatAssistantTurn, ChatViewTarget } from './types'

// The lifecycle of the first message sent from an empty draft. The draft is
// promoted to a session only when the server names it (session_created), so
// the pane cannot tell "welcome" from "chat" by whether a session id exists:
// between Enter and session_created the pane already shows the sent turn.
// The store advances this phase; panes only read it.
//
//   optimistic  the turn is on screen; the message is on the wire
//   admitted    session_created arrived; the view is now a session view
//   streaming   run_accepted arrived; the run is the server's
//
// No entry means idle. A send that fails before its reply starts is undone
// by the store in one step (turns removed, session deleted, input handed
// back), and its entry goes away with it. Until the reply starts, the session
// it created is therefore tentative (see isSessionTentative). The phase is
// keyed by (bot, view id) because the promotion (draft -> session) keeps the
// pane's view id, so the phase survives that rebind.
export type FirstSendPhase = 'optimistic' | 'admitted' | 'streaming'

export interface FirstSendEntry {
  readonly key: string
  readonly invocationId: string
  phase: FirstSendPhase
  // Stop pressed before the run could be addressed. The stream stays open
  // until the server confirms the abort, so the pane shows a pending stop.
  stopRequested: boolean
  // The workdir this send asked the server to bind the new session to. Empty
  // when none was requested.
  readonly requestedWorkdirId: string
  // The session the server created for this send. Empty until admitted.
  sessionId: string
  // The assistant turn of this send. Its first visible block is the point
  // after which a failure no longer rolls the send back (send.ts decides the
  // rollback on the same turn), so it also ends the session's tentativeness.
  readonly reply: ChatAssistantTurn
}

const PHASE_ORDER: Record<FirstSendPhase, number> = {
  optimistic: 0,
  admitted: 1,
  streaming: 2,
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
  function begin(
    target: ChatViewTarget,
    invocationId: string,
    requestedWorkdirId: string,
    reply: ChatAssistantTurn,
  ) {
    const key = firstSendKey(target.botId, target.viewId)
    entries.set(key, {
      key,
      invocationId: invocationId.trim(),
      phase: 'optimistic',
      stopRequested: false,
      requestedWorkdirId: requestedWorkdirId.trim(),
      sessionId: '',
      reply,
    })
  }

  // Phases only move forward. Events can repeat (a reconnect replays
  // reliable requests) and a stale one must not reopen an earlier phase.
  function advance(invocationId: string, phase: 'streaming') {
    const entry = entryForInvocation(invocationId)
    if (!entry) return
    if (PHASE_ORDER[phase] > PHASE_ORDER[entry.phase]) entry.phase = phase
  }

  // The server named the send's session (session_created, or the REST
  // creation that preceded the send).
  function admit(invocationId: string, sessionId: string) {
    const entry = entryForInvocation(invocationId)
    if (!entry) return
    if (!entry.sessionId) entry.sessionId = sessionId.trim()
    if (PHASE_ORDER.admitted > PHASE_ORDER[entry.phase]) entry.phase = 'admitted'
  }

  // A first send's session exists server-side from admission, but until the
  // reply starts it is the send's to undo: a failure deletes it again. Until
  // then, session lists leave it out and titles (tab, window, mobile top bar)
  // show the draft it came from, so a rollback has nothing on screen to take
  // back.
  function isSessionTentative(sessionId: string): boolean {
    const id = sessionId.trim()
    if (!id) return false
    for (const entry of entries.values()) {
      if (entry.sessionId === id) return !hasVisibleAssistantBlocks(entry.reply)
    }
    return false
  }

  // True while the run cannot yet be addressed by an abort control, which is
  // when a stop must wait for the server instead of failing the stream.
  function isAwaitingRun(invocationId: string): boolean {
    const phase = entryForInvocation(invocationId)?.phase
    return phase === 'optimistic' || phase === 'admitted'
  }

  function requestStop(invocationId: string) {
    const entry = entryForInvocation(invocationId)
    if (entry) entry.stopRequested = true
  }

  function requestedWorkdirFor(invocationId: string): string {
    return entryForInvocation(invocationId)?.requestedWorkdirId ?? ''
  }

  // Terminal: the send succeeded, was stopped, failed after the reply
  // started (the view is an ordinary session from here on), or was rolled
  // back (the view is the draft it was before Enter).
  function finish(invocationId: string) {
    const entry = entryForInvocation(invocationId)
    if (entry) entries.delete(entry.key)
  }

  function entryFor(target: Pick<ChatViewTarget, 'botId' | 'viewId'>): FirstSendEntry | undefined {
    const botId = target.botId?.trim() ?? ''
    const viewId = target.viewId?.trim() ?? ''
    if (!botId || !viewId) return undefined
    return entries.get(firstSendKey(botId, viewId))
  }

  function reset() {
    entries.clear()
  }

  return {
    begin,
    advance,
    admit,
    isAwaitingRun,
    isSessionTentative,
    requestStop,
    requestedWorkdirFor,
    finish,
    entryFor,
    reset,
  }
}

export type FirstSendTracker = ReturnType<typeof createFirstSendTracker>
