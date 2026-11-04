# External Agent Lifecycle Model

Every long-lived thing an external agent runtime keeps — an agent process,
and on an ACP process each conversation's session — follows one lifecycle.
Code that starts, uses, retires or closes one of them follows this model;
a change that needs another state or signal changes this file first.

## States

Each object moves one way through these states. Nothing moves back.

| State    | Meaning                                                        |
|----------|----------------------------------------------------------------|
| starting | being started; whoever needs it waits for the start to end     |
| live     | in service: it may be handed out                               |
| retired  | out of service; current users finish on it, nobody new gets it |
| closing  | being torn down                                                |
| gone     | torn down: what it held (processes, files, routes) is released |

`retired` exists only where an object can be in use when it leaves service
(a leased process). An object nobody uses goes from `live` to `closing`
directly.

How `starting` ends:

- the start succeeded → `live`;
- the start failed and holds nothing → `gone`;
- the object was taken out of service while starting → it is never handed
  out; once the start ends it goes `closing` (or `gone` if it failed).

`starting` is "not yet in service", not "out of service": waiters on a start
wait for its result, not for the object to be gone.

## Signals

Two signals, and every waiter names the one it needs:

- **out of service** — the object left `live`. Whoever hands objects out
  checks it: a lease, a session lookup. It is raised at once, before any
  teardown work starts, so nothing hands out an object being torn down.
- **gone** — the object reached `gone`. Whoever changes what the object held
  waits for it: a drain before deleting a login file or clearing history,
  runtime-directory cleanup, a conversation reopening the native session an
  old session still holds.

Never wait on "out of service" for something that needs "gone", or the
reverse.

## What proves "gone"

"gone" is announced only with proof that what the object held is released:

- **process generation**: its `Close` returned. `Close` returns only once the
  process exited and every session on it is gone (below).
- **`Child`**: its exec stream ended (`Exited`). The stream is cancelled
  locally, so its end never waits on the command. That the command is gone
  rests on the bridge's contract, not on an ordering Memoh observes: the
  bridge kills the command's process group when its stream ends
  (`bridgesvc` `configureCommandCancellation`), on its own side and
  moments later.
- **ACP conversation session**: the agent confirmed `session/close`, or the
  process that held it exited. A close the agent did not confirm (it offers
  none, did not answer in time, or the process was being torn down) proves
  nothing: the agent may still hold the session until its process ends. A
  close's result is settled once: closing again, or concurrently, returns
  the same result, and a forced close is never a confirmed one.
- **ACP session outside a conversation** (a discuss turn's throwaway, the
  options and probe sessions) is gone once Memoh's own cleanup is done. The
  agent may still hold it; nothing ever restores such a session, so nothing
  waits for the agent to let it go.

## Parents and children

A session belongs to its process from the moment it is created — while it
opens, while it is live, while it closes — until it is gone; it never sits
between two tables. The process is gone only after every session it owns is
gone, one still opening included, and takes no new session once it is
closing. A session gone through its process's end is gone only after that
end. Tearing the process down still never waits on the agent: it waits on
the process's exit and on Memoh's own cleanup.

## Where each lives

| Object | Owner | out of service | gone |
|---|---|---|---|
| process generation | `Supervisor` (`gen.state`) | `gen.state` past `live`; for the process itself `Process.Done()` | `gen.gone` (also what `Drain` and `Close` wait on) |
| `Child` (the exec stream) | `Child` | `Done()` (closes on exit or `Kill`) | `Exited()` |
| ACP conversation session | `acp.Host` (`Binding`) | `Binding.retired`, dropped from the host's tables | `Binding.gone`, tracked per conversation in `Host.closing` |

## Rules

- **An object that stops answering leaves service at once.** A write the
  agent leaves unread, a request abandoned before its answer, a turn that
  will not stop: what the object holds is no longer known. The owner raises
  "out of service" immediately and tears the object down; it is never handed
  out again.
- **Writes never wait on the agent** (`Child.Write`): an agent that stops
  reading is killed, so no caller is held by it.
- **A forced teardown first releases what Memoh itself holds up**: killing a
  process closes its local input and output, so no pump waits on a reader or
  writer that will never come back.
- **A wrapper is out of service no later than what it wraps, and no later
  than its own close**: a process wrapping a `Child` reports the child's own
  `Done`, not a signal its cleanup raises afterwards, and closing the wrapper
  kills the child before any cleanup of its own.
- **Tearing a process down never waits on the agent.** Closing a process
  abandons its sessions without protocol requests (`session/close`); the
  process's end ends them.
- **"gone" is reached only after the teardown returned**, and an object is
  tracked until then: an object already closing when a drain starts is waited
  out like one the drain closes itself.
- **A conversation restores a native session only after every old session
  holding that same session is gone**, on whichever process it was. An old
  session that ends only with its process retires that process so it exits;
  when that is the process the restore would use, the restore moves to a new
  process. A new native session (after a reset, say) waits for nothing.
- **One server owns every process and session.** Memoh runs one server
  replica, so these tables are the whole truth: a warm session is never
  behind turns another server ran. More replicas need a single-owner route
  per Bot before any of this holds.
