import { reactive } from 'vue'
import type { ChatMessage, ChatViewTarget } from './types'

// The lifecycle of the first message sent from an empty draft. The draft is
// promoted to a session only when the server names it (session_created), so
// the pane cannot tell "welcome" from "chat" by whether a session id exists:
// between Enter and session_created the pane already shows the sent turn.
// The store advances this phase; panes only read it.
//
//   optimistic  the turn is on screen; the message is on the wire
//   admitted    session_created arrived; the view is now a session view
//   streaming   run_accepted arrived; the run is the server's
//   rolledBack  startup failed; exitTurns are animating out and the composer
//               holds the restored input until the pane acknowledges
//
// No entry means idle. The phase is keyed by (bot, view id) because both the
// promotion (draft -> session) and the rollback (session -> fresh draft)
// keep the pane's view id, so the phase survives both rebinds.
export type FirstSendPhase = 'optimistic' | 'admitted' | 'streaming' | 'rolledBack'

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
  // The turns removed by a rollback, kept only so the pane can animate them
  // out. They are no longer part of any transcript.
  exitTurns: ChatMessage[]
}

const PHASE_ORDER: Record<Exclude<FirstSendPhase, 'rolledBack'>, number> = {
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

  // A new first send replaces whatever the view still held, including an
  // unacknowledged rollback: the send guards only admit one send per view.
  function begin(target: ChatViewTarget, invocationId: string, requestedWorkdirId: string) {
    const key = firstSendKey(target.botId, target.viewId)
    entries.set(key, {
      key,
      invocationId: invocationId.trim(),
      phase: 'optimistic',
      stopRequested: false,
      requestedWorkdirId: requestedWorkdirId.trim(),
      exitTurns: [],
    })
  }

  // Phases only move forward. Events can repeat (a reconnect replays
  // reliable requests) and a stale one must not reopen an earlier phase.
  function advance(invocationId: string, phase: 'admitted' | 'streaming') {
    const entry = entryForInvocation(invocationId)
    if (!entry || entry.phase === 'rolledBack') return
    if (PHASE_ORDER[phase] > PHASE_ORDER[entry.phase]) entry.phase = phase
  }

  // True while the run cannot yet be addressed by an abort control, which is
  // when a stop must wait for the server instead of failing the stream.
  function isAwaitingRun(invocationId: string): boolean {
    const phase = entryForInvocation(invocationId)?.phase
    return phase === 'optimistic' || phase === 'admitted'
  }

  function requestStop(invocationId: string) {
    const entry = entryForInvocation(invocationId)
    if (entry && entry.phase !== 'rolledBack') entry.stopRequested = true
  }

  function requestedWorkdirFor(invocationId: string): string {
    return entryForInvocation(invocationId)?.requestedWorkdirId ?? ''
  }

  // Terminal without rollback: the send succeeded, was stopped, or failed
  // after the reply started. The view is an ordinary session from here on.
  function finish(invocationId: string) {
    const entry = entryForInvocation(invocationId)
    if (entry && entry.phase !== 'rolledBack') entries.delete(entry.key)
  }

  function rollBack(invocationId: string, exitTurns: ChatMessage[]) {
    const entry = entryForInvocation(invocationId)
    if (!entry) return
    entry.phase = 'rolledBack'
    entry.stopRequested = false
    entry.exitTurns = exitTurns
  }

  // The pane has finished (or skipped) the exit animation. Guarded by
  // invocation so a late acknowledgement cannot clear a newer send.
  function acknowledgeRollback(target: ChatViewTarget, invocationId: string) {
    const key = firstSendKey(target.botId, target.viewId)
    const entry = entries.get(key)
    if (entry?.phase === 'rolledBack' && entry.invocationId === invocationId) {
      entries.delete(key)
    }
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
    isAwaitingRun,
    requestStop,
    requestedWorkdirFor,
    finish,
    rollBack,
    acknowledgeRollback,
    entryFor,
    reset,
  }
}

export type FirstSendTracker = ReturnType<typeof createFirstSendTracker>
