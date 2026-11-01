# Agent Runtime

> Moved from `AGENTS.md` to keep the always-on project rules under the context injection limit. `AGENTS.md` links here where this material is needed.

## Agent Development

- The AI agent runs **in-process** within the Go server — there is no separate agent gateway service.
- `internal/agent/` is a bounded-package namespace. It intentionally contains no root Go package.
- `internal/agent/application/` owns turn orchestration: message assembly, history, memory, compaction, decisions, persistence, and runtime dispatch.
- The Twilight AI native runtime lives in `internal/agent/runtime/native/`; `agent.go` there provides `Stream()` (SSE streaming) and `Generate()` (non-streaming) methods.
- `internal/agent/turn/` is the pure port used by Channel and by the authenticated in-process/gRPC transports. Internal code says Thread; compatibility adapters keep external `session_id` and existing gRPC fields stable.
- Model/client types are defined in `internal/models/types.go`: `openai-completions`, `openai-responses`, `anthropic-messages`, `google-generative-ai`, `openai-codex`, `github-copilot`, `edge-speech`.
- Model types: `chat`, `embedding`, `speech`.
- Tools are implemented as `ToolProvider` instances in `internal/agent/tool/`, loaded via setter injection to avoid FX dependency cycles.
- **Tool usage lives with the tool, never in the static prompt.** Per-tool usage goes in `sdk.Tool.Description`; cross-tool workflow guidance goes in an optional `tools.ToolUsage` `Usage()` method that `assembleTools` injects only when that provider registers tools for the session. Because both are gated with the tool itself, the prompt template never names conditionally-registered tools (guarded by `internal/agent/runtime/native/prompt_test.go`) and can't drift — the cause of the original `speak` / `search_memory` / `schedule` bugs.
- Prompt templates are embedded from `internal/agent/runtime/native/prompts/`. Partials (reusable fragments) are prefixed with `_` (e.g., `_memory.md`, `_identities.md`). System prompting combines `system_common.md` with mode-specific prompts such as `mode_chat.md` and `mode_discuss.md`.
- Internal chat state lives under `internal/chat/`: `thread` owns Thread lifecycle/forks, `message` owns persistence, `checkpoint` owns history checkpoints, `timeline` owns canonical event projection, and `view` owns API/UI history projection. These packages do not orchestrate Agent execution.
- The chat timeline (`internal/chat/timeline/`) owns canonical events, projection, rendering, persistence, and context composition. Inbound adaptation lives in `internal/channel/inbound/`, while discuss-mode driving lives in `internal/channel/discuss/`.
- Browser Use and Computer Use capabilities live in `internal/agent/tool/browser.go` (plus `internal/agent/tool/computer_a11y.go`) and are exposed only when the bot's workspace display is enabled. `browser_action` / `browser_observe` operate the headed workspace Chrome/Chromium instance over CDP, `browser_remote_session` exposes the same CDP endpoint for code-driven Playwright/CDP sessions, and the Computer Use pair (`computer_observe` / `computer_action`) drives the broader GUI desktop: snapshots come from the AT-SPI accessibility tree via the bundled `a11y-cli` Rust helper at `/opt/memoh/toolkit/display/bin/a11y-cli`, and raw RFB pointer/keyboard input remains as a fallback when accessibility cannot reach the target. Both browser and computer screenshots are saved to a workspace path and never auto-attached to the conversation, so the model must explicitly read the path when it wants the image. Prefer Browser Use for web pages; use Computer Use for native dialogs, non-browser apps, or GUI states that CDP cannot reach.
- Headless Playwright scripts are still ordinary workspace commands, but they are not the same path as the headed workspace browser/display stack. Use the headed Browser Use tools when the user needs to inspect or operate the visible workspace browser.
- External agent runtimes (Codex, Claude Code, Grok Build, generic ACP) are registered in one `external.Registry`, which application, handlers and wiring share. An optional capability (model catalog, fork, compaction, goal, plan mode, commands, modes, device login, account, quota, sign-out) is the interface the driver implements, found with `external.As[T]`; it is never repeated as a flag. A driver's static `external.Caps` holds only what no interface says: its managed dependency and command, whether a fork must go through the runtime and the metadata key of the only turn it may start from, and whether read commands wait for an idle conversation. The shared pieces every driver uses live in `external`: approval decision and card (`DecideApproval`, `ApprovalCard`), questions (`AskUser`, `UserInputCard`), the steering pump (`PumpSteering`), the permission menu (`PermissionModeState`, `SelectedPermissionMode`), plan mode (`PlanModeState`), the compaction turn (`CompactionTurn`) and process sign-out (`agentproc.Supervisor.SignOut`).
- The compaction service (`internal/agent/context/compaction/`) handles LLM-based conversation summarization.
- Loop detection (text and tool loops) is built into the agent with configurable thresholds.
- Tag extraction system processes inline tags in streaming output (attachments, reactions, speech/TTS).

## Native context recovery

All durable native entries bind step persistence and continuation through
`bindNativeRun`. Web, pipeline IM, trigger, follow-up, decision continuations,
retry, edit and native Discuss can recover context at a committed step boundary
and continue the same run. An empty initial history does not disable recovery. A retry or edit
folds what its request renders: the history before its cutoff and the steps of
the attempt its first step selected. A retry renders its input from the request
it replaces, which that step hides; from then on the copy the step wrote is the
input the event keeps in place. When the session's history checkpoint depends
on the replaced turn, the same step moves the session back to the newest
checkpoint that does not, or the attempt's first compaction does when it needs
one before that step, so the attempt folds onto the checkpoint the replacement
keeps. Subagent and external runtimes have separate ownership and remain
outside this capability.

A native Discuss run writes each step as it commits, under the rows ingress
already wrote for its input. Each composed message names the inbound message,
stored reply or summary Channel rendered it from; the name crosses the
internal RPC with the command. The inputs of the batch
the run answers are anchors, so a fold reads only what the run's request
renders: a message that arrives during the run is the next batch's input, not
part of the summary, and a row no timeline event renders stays raw for the chat
view instead of being summarized unread. The steps before the run's answer mark their rows as such:
they are context for later runs but do not prove the batch consumed, so a worker
that comes back without a durable cursor still answers the input of a run that
failed after committing steps. The answer, the completed step that calls no
tool, is that proof.

An armed run compacts with `compaction.Service.Fold`, before any step whose
request does not fit, the first one included: one event over the request the
run sends next. Before its first pass the event decides what of the request
stays verbatim (`retain`), whoever calls it. Runtime context stays and takes
nothing. Everything else it keeps takes from one verbatim budget: a quarter
of what the allowance leaves beside the system prompt, the tools and the
runtime context, at most 32000 tokens; first the messages no context row holds and
the anchors after the newest call group, the input or steers the next call
answers, which stay whatever they take; then the other anchors newest first;
then the newest complete call groups, a call with all its results, up to the
first that does not fit. Everything else folds, the active checkpoint's
summaries included. A message takes what a request prices it at or what a
history load admits it by, its encoded bytes, whichever is more: an inline
image a request prices flat stays only if the byte-bounded load can still
hold it, so a history of large screenshots folds rather than stay too large
to load. The latest step is such a call group, and so is the
deferred call and answer a decision continuation resumes from: a step within
the budget stays, a larger one folds in the same pass as what is before it.
32000 tokens holds three results at the tool output bound, or two beside a
long input. The instruction asks the summary to stay within another quarter
of the room, so a fold that keeps no more than the budget and gets such a
summary leaves about half of it to the steps after it. That half is the
target, not a guarantee: a pass commits any summary shorter than what it
replaces, and the summary the next request carries only has to fit the room
that request has, so a longer one brings the next fold sooner rather than
failing the run; an in-run event logs the request's size after it against
its allowance (`context compacted`). What stays whatever it takes, the input
the next call answers and the messages no row holds, can take more, and a
Discuss batch is kept by its own rule. The 32000-token bound was calibrated on
call-group sizes of real agent sessions, and every event logs
`compaction retention` with the budget, the call groups it kept and the
latest group's size, to check it against Memoh's own traffic.
Each pass forks that request up to the newest messages it keeps and asks the
run's own model for the summary that replaces what folds there. The
instruction quotes the input the next call answers, and asks the summary to
end with where the work stands; the tool results it folds are listed by the
ref `get_messages` reads them back by, which holds only for a message that is
its row as stored: a result the read path rendered from its call's row, or a
part of a row it split across messages, is folded without one. The fork keeps the run's system
prompt, tools, cache key, reasoning and output settings and ends with one
compaction instruction, so the summarizer reads what it replaces in place and
the fork shares the run's prompt cache. It asks for the output the run's
requests ask for, or none where they leave the cap to the provider, and reads
only what the window holds beside the run's output allowance, as each of
those requests does: a provider setting sized for that output, such as a
legacy Anthropic thinking budget, stays valid. It streams outside the run's
call observer, and a stream that sends nothing for the run's model window is
cut off; a transient failure is asked once more, and an answer that calls a
tool once more with a firmer instruction, which the pass priced in, so the
second request fits with the same output. A fork that does not fit the window
reads a shorter prefix ending at a tool-group boundary, and the event passes
again over the request the native runtime spliced, each pass absorbing the
summary before it. A pass reads a call group whole or not at all, so the
window's bound is the group's: a group larger than what the window leaves one
group beside the system prompt, tools, the longest summary, the instruction
and the least output reaches the summarizer by the head and tail of its larger
parts, the text, call arguments and results of all its messages, an equal
share each of what the smaller ones leave; its reasoning stays whole, as a
provider that signs reasoning takes a tool loop back only with it unchanged. No group is too large to fold; the
rows keep the whole messages, which `get_messages` reads back, so a folded row
is one its summary stands for, read whole or in part.

Every message of the request names what it renders: a history row, an inbound
or self-sent message, or a summary. The run's query names the row that holds
the run's request, which admission or the run's first step writes; runtime
context the run rebuilds for every request names nothing and stays where it
is, and so does a message no context row holds, which no checkpoint can stand
for. A run's input and steers are anchors, kept as the verbatim budget keeps
them; the inputs a Discuss run answers are kept newest first while they leave
a tenth of the room for the summary, and its call groups take what they leave
of the budget. Older anchors fold, and a folded input records the images it
carried as omitted. When nothing but summaries is left,
they are rewritten only if they do not fit the room the next request has, and
a remainder too small for any summary to replace ends the event once the
request fits. The context view keeps an armed run's whole history, without
trimming, and every dispatch checks the allowance.

The allowance is what the provider counts, and a run measures its request by
an estimate that prices bytes at a fixed rate; content a provider's tokenizer
counts more densely, such as structured JSON, would pass the estimate while
the provider counts it over the window. An armed run therefore calibrates the
estimate by the provider: each step it sends records what the estimate priced
the request at, and once the provider reports the input tokens it counted, the
run measures its next requests by the ratio of the two (at least 1, at most
4), comparing them and every window an event reads within in the estimate's
units. The step's assistant row keeps the estimate in its metadata
(`input_estimate_tokens`) beside the provider's `usage.inputTokens`, so the
session's next run on the same model starts from the latest such step among
its newest assistant rows (`GetLatestInputEstimateSample`) and measures its
first request the same way. An in-run event that measured by a ratio above 1
logs it as `density`.

`compaction.Service.Compact` folds history from rows instead, with the same
engine: for a chat or continuation load the byte budget truncated, before the
request is built, and for Discuss input Channel omitted or a batch it could
not compose. It builds the request from the rows: the active checkpoint's
items in their order, then the context rows after its position, oldest first
within a byte bound, each row the message a run's request holds for it, so a
pass plans and folds it the way it folds a run's. A read takes whole call
groups, a row and the tool rows after it that carry its calls' results: those
that end within the bound, and the one that crosses it when what it reads past
the bound fits 8 MiB. No checkpoint, even one an unfinished event leaves, has
a call behind its position and a result of it after, and a call larger than
the bound still folds with its results; a read that leaves nothing to fold
while rows are left after it, a group larger than any read admits, fails the
event as an input overflow. The compaction model, whatever provider it runs
on, is offered no tools: its request writes every message out as text under
its role, tool calls and tool results included, media named. It is asked for
as much output as the window leaves, up to its cap, and never less than half
of what the summary may need; a pass reads less rather than ask for less. The
summarizer streams through the client the runs stream through. An event folds
every row the caller does not keep into one rolling summary, reading again
for what one bounded read left; it keeps what a run's fold keeps, with the
input, a continuation's steers or a Discuss batch as anchors.

A committed pass hands its caller the request it leaves: the engine orders
the messages it read once, by `turn.PlaceSummary`, with every message it
replaces gone and the ones it keeps inside the folded span before the summary,
the order its checkpoint keeps them in; the native runtime and a request built
from rows apply that order. Every request renders a summary by
`turn.SummaryText`: one
fixed sentence saying it stands for earlier messages no longer shown and that
the verbatim messages before it and everything after it take precedence, then
the summary in `<summary>` tags. The engine prices it that way too. A result the read path writes for a deferred call whose decision
ended without one renders from the call's row, so a fold that covers the call
removes it too; a splice that leaves a call without its result, or a result
without its call, fails the event. A pass refuses a request composed on
another checkpoint: the summaries it rendered must be the ones the active
checkpoint holds.

A thread's history has no time window. What stands in a request for the
history before its context is the session's active history checkpoint
(`bot_sessions.history_checkpoint_id`, `internal/chat/checkpoint`): a position
in the session's turn order, `(turn_position, turn_message_seq)`, and what
represents everything up to it: ordered items, rows kept by reference and
summaries, always at least one summary. A request renders the items in their
order and then the session's context rows after the position: the visible rows of complete
turns, other than legacy `passive_sync` rows. Chat and turn-response loads
read those rows newest first within the byte budget, so a load costs what the
checkpoint leaves raw, not the thread's length. A chat or continuation load
the byte budget truncated, the record or the pipeline one, folds the rows it
left out from rows before the request is built and loads again, rather than
send a history that silently starts later; it fails if the history still does
not load whole.

Compaction folds the checkpoint's summaries and the oldest complete foldable
units after it into one summary and commits a checkpoint on top of the active
one (`checkpoint.Store.Commit`): its position is the last row it folds, and it
keeps every context row up to there it does not fold, in history order: the ones
the pass read before its summary, where the request that read them places it,
and the ones it never read after it. Kept rows are the anchors a fold keeps, a
run's inputs and steers newest first, which the summarizer reads but does not
fold, so a run that folds its own earlier steps leaves `[input, summary, latest
steps]`; once the run is over the input is an ordinary candidate and the next
compaction folds it. A pass that cannot read
everything at once repeats the step, and each further pass folds onto the
summary the one before wrote: an event whose session moved back between passes
fails instead of folding the same history again. Only a commit moves the
position; neither a byte window nor a cutoff does. A fold over a run's request
reads only what that request renders. A fold from rows reads candidates that end
where the caller's history does: before the cutoff of an edit or retry, else
before the turn the request runs as, so a fold never reaches a row admitted
after the request's own. A commit whose parent is no longer the session's active
checkpoint fails with `checkpoint.ErrMoved`. A row it keeps that the pass never read is logged and
counted (`history.checkpoint.unseen_rows_total`).

The rows of a message a checkpoint folded record the message as it was when
they were written. A message changed after that, edited or deleted, composes
again after the checkpoint with what its summary never read; Discuss event
replay keeps its complete event chain.

Every checkpoint names its parent and depends on every row up to its
position, which its summary may have read. A write that removes rows (hiding,
replacing or superseding a turn, deleting messages, retiring a run's
projections) moves the session back, in the same statement, to the newest
checkpoint on its chain that does not depend on them
(`memoh_rewind_history_checkpoint`), and deletes the checkpoints it moved back
past: they depend on a removed row, so none holds again, and a fork copies its
own. A fold that read one of them as the active checkpoint finds the history
moved when it commits. Nothing else changes the history a
checkpoint accounts for: a session runs one turn at a time (a run waiting on a
decision still holds it), a run writes only into its own turn or the latest
turn, a new turn takes the next position, and a run that lost the session is
fenced out of every write, deletes included. Rows are only ever added after
the active checkpoint's position. Adding an asset to a row changes nothing,
and clearing history drops the session's checkpoints.

An edit or retry renders the history as it stood at its cutoff: the checkpoint
the replacement keeps (`checkpoint.Store.KeptBeforeTurn`) and the context rows
after it before the cutoff. A retry after in-run compaction thus refolds from
the summary before the replaced turn rather than from the start of the thread.
An edit or retry that must compact before its first step commits rewinds the
same way first (`RewindHistoryCheckpointBeforeTurn`), so its fold builds on the
checkpoint the replacement keeps.

Sessions from before checkpoints keep their context: 0170 turned each one's
active summaries, with the rows in their span they left raw, into its
checkpoint. A session restored by a bot import brings its history without
summaries, so its first request that does not fit compacts it.

A fork copies the source's visible history through its turn under the source's
share lock and carries the newest checkpoint on the source's active chain that
accounts for no history past the fork point (`checkpoint.CopyToFork`), in the
transaction that creates the fork. Its summaries are copied with the rows
they cite named by their copies, so a ref `get_messages` reads a folded tool
result back by, which reads only the session's own rows, reads the fork's
copy; its kept rows are named by their copies, and its position mapped to the
copy's, so a fork's first request does not fold again what the source had
folded.

Candidate payloads are read within byte bounds. A fold from rows walks its
candidates oldest first and stops at the end of the call group that crosses
its bound, so catching up on a long backlog a pass at a time reads each row
once. Every bounded history read steps through the context-order index
`idx_bot_history_messages_context_order` one row at a time and measures a row
(`octet_length(content::text)`) only once it has taken it, so no read detoasts
a row past its budget. Each step is bounded by the previous row's values, not
by the session, so a session the planner's statistics put at one row is still
walked through the index rather than scanned and sorted at every step. A
request reads the active
checkpoint; an edit or retry, a rewind and a fork walk its chain for the
checkpoint they keep. A fold over a run's request
does not load the raw history span or reread ancestor summaries. Owner fencing and
cancellation remain authoritative. Failed passes publish nothing; successful
earlier passes and tool effects stay committed.

The caller checks the complete next request once, after the event; one that still
does not fit fails instead of compacting again. Summary bounds include rendering
and envelope costs, and insufficient room fails before a summarizer call. Armed
runs fail visibly instead of using step reselection or result stubs. Successful
events log `context compacted`; the run result records failures, and
`MutationContextSwitch` records the structured reason. The Compaction Logs page
shows committed attempts.

## External runtime notices

`runtime_notice` carries a public conversation fact, not temporary activity
(`runtime_status`) or model prose. The application collects notices across
driver startup and execution, including scheduled runs. Successful, failed,
and stopped rounds store each distinct notice once under `runtime_notices` on
their first assistant row. Terminal snapshots and history reads both project
those records into notice blocks, so refresh, reconnect, and forks retain them.
The metadata stays outside the provider transcript and memory-extraction text.
Use a stable code, public fallback text, and public string arguments; never put
private diagnostics in a notice. A round that cannot commit still follows the
normal persistence failure/rollback contract.

## Native session state of external runtimes

Codex, Claude Code and Grok Build keep their own conversation record on the
bot's data volume: Codex writes rollouts under `CODEX_HOME`
(`/data/.codex/agents/<agent>/`), Claude Code writes transcripts under
`/data/.claude/projects/`, Grok Build writes sessions under `GROK_HOME`
(`/data/.grok/agents/<agent>/`). These files are
the authoritative native conversation record; Memoh does not snapshot them into the
database. Chat history stays in PostgreSQL as usual, so losing the native file
costs the model its own memory of tool details, not the visible conversation.

Workspace exports, rebuilds with data preservation, and workspace snapshots
archive and restore native sessions, CLI indexes, and credentials together.
Workspace archives do not filter runtime files, and importing an archive does
not clear existing credentials or sessions absent from that archive.

Session runtime metadata records what resumes the native session: the Codex
thread id and, once Codex has reported it, the rollout path
(`codex_rollout_path`); the Claude Code session id. Codex resumes by path
first, which also works when its own SQLite index no longer lists the thread,
and by id when the path is refused. Claude Code checks that the transcript
still exists before passing `--resume`. Grok Build records its session id and
reattaches with ACP `session/resume`, passing the permission mode again
because Grok does not store it with the session.

When the native session cannot be resumed — Codex refuses `thread/resume`,
the Claude transcript is gone, or Grok answers `session/resume` that the
session file is missing — the turn
starts a new native session and says
so with a `native_history_lost` runtime notice; the next turns continue from
Memoh's composed context. Any other failure while resuming is reported
instead, since a retry may still resume the thread: as
`external_runtime.session_resume_failed`, or, for a failure the user can fix
such as Grok rejecting its sign-in, as that failure. Nothing here can make a
thread permanently unusable.

Grok Build drives the rest of its session through Grok's own ACP extension
methods, read as Grok 1.0.62 defines them (`internal/agent/runtime/external/grok/config.go`
lists them; a process of another version is logged at start): compaction is
`_x.ai/compact_conversation` on the open session, the `status` command reads
`_x.ai/session/info` for the model and context use, and the model catalog
follows `_x.ai/models/update` because the snapshot in initialize goes stale.
A fork is `_x.ai/session/fork`, which copies the session whole; the turn's
prompt id is recorded as the fork anchor and a fork is refused from any turn
but the latest. Fork preparation and Grok's `status` command take the session's
turn slot, as a configuration-only run, and return busy while a turn runs.
Grok runs on the ACP core (`internal/agent/runtime/external/acp`, see its
`AGENTS.md`) as an extension of its own: one Grok process serves every
conversation of the Agent, each on a session of its own, and also its
sign-in, account and model catalog. A finished sign-in retires the Agent's
process, so the next turn starts from the new login.
Grok reports a Memoh tool gateway it could not reach through
`_x.ai/mcp/server_status`, which the turn surfaces as a tools-unavailable
notice. Grok's ACP surface has no mid-turn input: a prompt sent while one runs
is queued until the turn ends, so steering is not offered, and `mcp/upsert`
writes a server into the Agent's config for every later session, so a session
opened without the gateway is reopened with it rather than amended.

Forks use Codex's native `thread/fork` on the running app-server; the branch
records its own rollout path. Threads created before this behaviour still
resume by their thread id and gain a rollout path after their next turn.

Custom ACP agents run with `HOME=/data` and keep their sessions wherever the
agent stores them under it. The thread records the agent's session id with the
thread it belongs to (`acp_native_session`). ACP lets one connection host
several concurrent sessions, so an Agent's conversations share its process,
each on a session of its own; an agent that cannot host concurrent sessions
is not supported. A conversation moving to a new process has its session
closed on the old one first, so it is never continued on two at once. A
new process or session — after closing for idleness, a server restart or a
failed turn — reattaches with `session/resume`
when the agent offers it, and with `session/load` when it does not or refuses;
history replayed by either is dropped. ACP names no error for a missing session and agents answer with
different codes, so any error the agent itself returns, except
`auth_required`, counts as a lost session and is replaced with a
`native_history_lost` notice. A timeout or a broken connection fails the turn
with `external_runtime.session_resume_failed` and keeps the stored id, since
the session may still be intact; the Web chat offers a button on that failure
to give the session up (`DELETE .../acp-runtime/native-session`), which keeps
the messages and starts the next turn on a new session. A forked or restored
thread carries the source thread's id, which it does not own, so it starts a
new session with the same notice; changing an empty thread's agent or project
drops the id. Discuss turns always start a new session. A round that fails to
commit after the agent finished it stays in the agent's session, as with
Codex.

Migration 0156 removes the obsolete `agent_session_states` and
`agent_session_state_lines` tables and their saved snapshots. Downgrading
restores the old table structure, not deleted snapshot data.
`agent_session_publications` held the run ID that fenced ACP warm processes
before they restored native sessions; nothing reads or writes it now.


## Account sign-in of direct runtimes

Codex and Grok Build sign their account in inside the Agent's own process,
through `POST /bots/{bot_id}/agents/{id}/login/device/{authorize,poll,cancel}`
(`external.DeviceLoginProvider`). The runtime obtains the device code, the
user opens the URL and confirms it, and the runtime writes its own login file
in the Agent's home on the bot's data volume, where it keeps and refreshes
it. Memoh stores no copy; a finished sign-in retires the Agent's process so
the next turn starts from the new login. On a Grok home that already holds a
login, ACP `authenticate` answers from it without asking anyone, so signing
in again first removes it. Grok hands the device URL out through its
`_x.ai/auth/get_url` request while `authenticate` waits for the user.

A ChatGPT or Grok account can therefore not be staged before the Bot exists:
the Agent is created without a credential and signs in from its settings.
The staged authorizations (`/agent-authorizations`) cover API keys and the
Claude browser login only. Starting a sign-in ends the Agent's earlier ones,
approved or not, and a disconnect or Agent deletion ends them too.

## Execution timeouts and budgets

Model silence, tool silence, human decisions, and total execution budgets have
separate owners. Streaming chat, external runtimes, scheduled turns, and decision
continuations use the same phase-aware application watchdog:

- Model requests have a five-minute inactivity window. Reasoning effort can
  extend that individual window, capped at fifteen minutes.
- Active tools have a fifteen-minute inactivity window, with headroom for a
  foreground command's ten-minute soft wait. A tool's own shorter operation
  deadline still applies. Completed tools do not extend later model requests.
- A pending approval or user-input request pauses only its own tool watchdog.
  Its decision flow retains the ten-minute response window. Parallel tools
  remain supervised; explicit cancellation and runtime ownership loss still win.
- Managed subagents have a ten-minute progress watchdog, with no implicit total
  duration. Detachment preserves an explicit ancestor execution deadline.
- A compaction inside a native run, a fold before a step or a Discuss run's
  budget recovery, pauses the model watchdog while it runs; the model window
  restarts when it returns. A chat load folded before the request is built
  runs before the watchdog starts. Cancellation and execution budgets still
  apply.

Conversation messages only append between compaction events. A tool result is
bounded once, when the tool returns (`tool_output_max_bytes`, 32 KiB by default, and
`tool_output_max_lines`, keeping its head and tail), and every later request
carries it as stored. No run prunes, stubs, or drops a stored conversation message
it already sent. Dynamic background-task snapshots and before-model hooks keep
their existing refresh behavior.
When the next call does not fit, a run armed for in-run compaction folds its
eligible sources into the rolling summary. Any other run, or an event that
cannot make the call fit, ends with `context.budget_unsatisfied` or
`context.protected_overflow`.

After a Discuss run stops over budget, the next turn keeps its retained,
unanswered inputs as anchors. Completed tool steps between those inputs can
be compacted before dispatch; retaining an input does not pin every later step.

Schedules expose `max_run_seconds` (default 3600, range 300–86400), independently
of the selected runtime. Every fire uses its existing log UUID for turn admission,
so repeated fires targeting the same session remain distinct. The execution
context enforces the budget and passes it to descendants. Budget expiry uses
`schedule.execution_timeout`, independently of model inactivity. This feature adds
only the `schedule.max_run_seconds` column; schedule ownership, restart recovery,
and the log schema are unchanged.

The `exec` tool has separate soft waiting and process budgets:

- `timeout` controls foreground waiting (default 30 seconds, maximum 600). When
  that wait expires, the same process is adopted by the background manager.
- `max_duration_seconds` controls a finite command's total runtime (minimum 30,
  default 7200, maximum 86400). Adoption never restarts this budget. Foreground
  waiting ends before the execution budget when the two would otherwise coincide.
- `background_mode: "service"` requires `run_in_background: true` and omitting
  `max_duration_seconds`. The process lives until explicitly stopped, its
  workspace closes, or an explicit ancestor execution budget expires.
- Stop cancels the underlying bridge stream. Opt-in bridge heartbeat frames
  report process liveness separately from stdout/stderr. The receiver enables
  missing-heartbeat detection only after a bridge advertises it by sending a
  heartbeat, so older bridges remain compatible. Loss of supervision without
  an EXIT frame is `unknown`, not proof of successful completion or safe retry.

Video generation has its own configurable monitoring budget (default two hours,
maximum 24 hours). A receipt under the bot workspace's `.memoh/video-jobs/` records
its provider job identity when storage is available. Monitoring expiry checks the
provider once more; unfinished or unobservable jobs retain their identity and an
`unknown` outcome. They are not automatically canceled or resubmitted. An explicit
user stop still requests provider cancellation. Receipts support operator recovery;
they do not constitute automatic job adoption after a server restart.

## Model call failures

The native runtime retries a failed model call from the last committed step,
by default up to five times per run with a backoff capped at eight seconds. Only
the failure of the provider call itself is considered: an error returned by
`DoStream` or `DoGenerate`, an `ErrorPart` in the stream, or a stream that ends
before its finish-step part. A failure of the loop's own work (the
provider-attempt handoff, a step commit, a capability refresh, the tool batch)
ends the run, since its tools may already have run. The streaming and the
non-streaming loop share the rule, the budget and the backoff
(`internal/models/modelretry`).

A provider call is retried when the SDK classifies its `*sdk.APIError` as
`rate_limited` or `server_error`, when the stream was cut off
(`sdk.ErrStreamIncomplete`, `io.ErrUnexpectedEOF`, or a stream that closed
before its finish-step part), or when the chain holds a `net.Error`.
Cancellation and expired deadlines are never retried, and every other `Kind` is
final. Error text is never read. A `Retry-After` or `retry-after-ms` header on
the provider's answer sets the wait when it is at most one minute; otherwise the
backoff does.

A retried attempt is recorded once, as a WARN event for `agent.model_call`, and
the stream reports it as a `retry` event carrying the attempt counters, the wait
in `retryDelayMs` and the failure's class in `retryReason` (`rate_limited`,
`server_error`, `stream_incomplete` or `network`), never the provider's text.
The stream publishes an `error` event only for the failure that ends the run,
with the failure as its `Cause`; after the last retry that is the last attempt's
failure, wrapped. A subagent attempt is run again only when its watchdog ended
it.

The runtime projection keeps the wait visible to subscribers. A `retry` event
sets `retry` on the current run (`attempt`, `max_attempt`, `delay_ms`, `reason`
and `retry_at`, the server time of the next attempt) and sends it in the run
patch. The next visible event, a terminal event or a terminal status clears it;
a patch removes it with `clear_retry: true`, and a patch carrying neither field
leaves it unchanged. Snapshots carry the field, so a reconnecting client sees it.

Single model calls outside a run use the same rule: the context compaction
summary, the memory calls and the session title. Image generation is retried
only after a `rate_limited` answer, since a failed call may still have produced
a billed image, and the DashScope task API not at all.

The application names the failure in one translation function shared by the
WebSocket, IM, discuss, scheduled, decision-continuation and subagent paths. A
context the budget cannot fit is `context.protected_overflow` or
`context.budget_unsatisfied`. A provider's answer is named by its `Kind`:
`agent.provider_auth_failed`, `agent.provider_permission_denied`,
`agent.provider_quota_exhausted`, `agent.provider_rate_limited` or
`agent.provider_overloaded`, and `agent.provider_request_rejected` for any other
`Kind` with an HTTP status. A request that got no response (a `*url.Error` in
the chain) is `agent.provider_unreachable`. Anything else is
`agent.response_interrupted`. Published events carry the code and its catalog
detail; the cause stays in the run's result record.

## Graceful shutdown and session resume

Server shutdown closes admission and records `session_runtime.interrupted` on
running session runs before canceling producers or stopping HTTP, RPC, schedules,
external runtimes and workspaces. The existing `lost` terminal state carries this
specific reason; no schema migration is needed. Completed finish proposals and
explicit user aborts retain their authoritative outcomes. With a distributed live backend, native parked decisions remain
`waiting_decision`. After the producer has
committed its assistant tool call, shutdown advances the run, session and every
pending decision to a new persistence fence in one transaction, then revokes
local execution. It leaves the old Redis lease index in place as the recovery
pointer. Once the lease expires, the existing reaper reserves a fresh owner;
failed reservations retain that pointer for retry. No answer is synthesized.
Decision commands cannot begin after shutdown closes admission. A previously
accepted native answer moves its run to `running` in the same fenced PostgreSQL
transaction as the decision response. `input_json.decision_continuations` records
each accepted decision; approved tools checkpoint `executing` before dispatch.
Shutdown therefore classifies an accepted continuation as running work even while
the tool is still executing and has not emitted `agent_start`. A state CAS prevents a running-row
snapshot from terminalizing a decision that parked concurrently.

An expired decision after graceful handoff is retired under the authoritative
database token, after checking that no live successor owns it. An unapplied
terminal CAS on an active row retains the recovery index for retry. A renewed
same-token owner retains its index; only an obsolete entry may be removed while
a successor remains live.

Codex, Claude Code and ACP inline waiters still depend on the exiting process;
they are not preserved as resumable native decisions. Expired requests, explicit
abort, reset, deleted sessions and newer persistence owners must not be revived.
Transient database/runtime-classification failures retain the recovery pointer
instead of canceling decisions. This does not guarantee recovery of a producer
whose tool-call persistence failed or exceeded the shutdown budget.

The continuation keeps its original durable turn and request message ID. A
response retry uses the existing control-ID deduplication contract; tests cover
shutdown, a fresh manager's lease recovery, the answer and duplicate submission.
An accepted continuation interrupted during execution uses the ordinary
`session_runtime.interrupted` recovery path. Recovery validates saved decision
records against their Bot, session, run and token, and reconstructs the reasoning
with the accepted answers and approval status. It does not dispatch the approved
tool again. An executing tool without a saved result may already have produced
side effects: inspect history, workspace state and external receipts, and request
a new user decision if the outcome cannot be established. This is recovery of the
continuation, not an exactly-once guarantee for arbitrary external tools.

**First upgrade and rollback:** the old executable still runs the shutdown hook
on the first rollout, so deploying this fix cannot protect waits already ended
by that executable. Drain those waits before upgrading, or use a separately
approved recovery procedure with authoritative pending decisions, no newer turn,
and tenant-scoped checks. Do not revive historical `lost` rows automatically.
Rolling back reintroduces the old shutdown behavior on the next exit; drain
pending native waits before rollback. No schema migration is required.

Local browser verification used the documented development stack with Redis,
a synthetic model and one active Server. The original decision remained visible
after SIGTERM and a fresh owner resumed the same run and turn after the user
submitted the answer. The original user message was not duplicated.
The separate-containerd two-Server fixture could not complete a continuation on
the peer because its workspace bridge was unavailable; this is not a passed
multi-node workspace acceptance test. Hosted deployments still need their
shared workspace integration verified after downstream synchronization.

At turn start, the server saves a versioned resume context in `session_runs.input_json`.
It preserves the original query, identity, execution location, model selection and
absolute ancestor deadline. Only verified credential scopes are retained, never
bearer tokens. Account-backed credentials are revalidated against current account
state and Bot chat permissions before renewal; scoped chat credentials retain their
original Bot, chat and route scope. External runtimes retain their existing owner
and Workspace Exec authorization checks.
An External Discuss turn saves its composed batch instead of a query, once when it
starts and again after final admission. A resume admits that batch again behind the
resume instructions, without compaction, and records the input that no longer fits.

After startup, a bounded worker discovers interrupted sessions, waits for the
workspace bridge to become reachable, and submits a continuation with the stable
invocation ID `resume:<interrupted-run-id>`. Normal admission and fencing prevent
two workers from executing that continuation. A later admitted turn supersedes the
old intent. Expired budgets do not restart. Each continuation reloads committed
history and instructs the model to inspect interrupted tool outcomes before taking
further action. Native, direct-agent and subagent sessions use their existing
runtime dispatch; this is session-level continuation, not process-memory restore.
Recovered output is published into the session runtime and saved to history.

The worker lists interrupted runs through the partial index
`idx_session_runs_resume_pending`, which covers only lost runs with the
interrupted code that still carry `input_json.resume`; it never walks a team's
run history. An interrupted run stays `lost` permanently, so the worker retires
its intent (removes the `resume` key) once it can no longer continue. At the
start of every pass over a team it retires intents a later turn answered or
whose session was deleted, and it retires an individual intent when its budget
expired, its context version is unsupported, its credential scope was revoked,
or admission reports it superseded or already continued. Retired intents leave
the index and are not retried; transient failures such as an unready workspace
keep the intent for the next pass.

Uncommitted streaming tokens can be lost. Background command handles from the old
process are not reattached; workspace output and external job receipts must be
checked. Subagent sessions can continue, but old in-memory parent waiters are gone.
SIGKILL, OOM, host failure and a shutdown unable to write PostgreSQL cannot reliably
record a marker and therefore retain the existing `lost` behavior. A continuation records its resolved request in its own intent. On another
restart it first checks its own applied inputs, then validates that saved
request, so image feedback and subsequent output remain in the same history turn. The runtime
view carries that turn and its committed prefix separately from the new run's
admission identity; Web extends the prefix rather than showing a second reply.
Once admitted, a continuation retries temporary workspace unavailability during
initial target/config preparation for up to 30 seconds per stage, within the
original context deadline. Model and tool execution are not replayed by this
startup retry. Other failures are reported normally.

The server has a 30-second graceful shutdown budget. Compose allows 45 seconds,
and the entrypoint waits for Server exit before terminating embedded containerd.
The development Air supervisor gives the server the same shutdown allowance.

### Hosted tenant scope integration

The composition root installs `SetSessionResumeScopeProvider` before startup.
Its provider enumerates a bounded page of opaque Team IDs through the hosted
scope catalog, binds each ID to the ordinary database context, and reports the
current binding. A hosted adapter can delegate to its existing session-runtime
reaper scope provider, converting the page to `SessionResumeScopePage`.

Every run query, workspace readiness probe, authorization check, admission,
stream and terminal write retains that bound context. The worker rejects a run
whose `team_id` differs from the current scope. Discovery never uses a privileged
cross-team run query. The OSS fallback uses the composition root's allowed Team;
if neither an allowed Team nor a hosted scope provider is installed, startup
fails closed. No schema change or additional role is required by this port.

A recovery admission also carries `ResumeRunID`. Under the same parent lock as
ordinary admission, PostgreSQL verifies that the source is still interrupted and
has no newer turn. This closes the gap between discovery and admission, including
when an intervening user turn has already completed. Invocation replay retains
its existing result. Hosted credential renewal and remote workspace adapters
remain separate integration responsibilities.

HTTP shutdown cancels the server request base context after run interruption has
been recorded. Long-lived SSE requests can then finish before the later event-hub
cleanup hooks; they must not consume the entire graceful shutdown deadline.
The old run loses write ownership after its terminal marker is durable. The Server
then waits for active Web, Channel, schedule and subagent turns to finish their
application-side cleanup before closing the PostgreSQL pool. This drain uses the
remaining graceful shutdown deadline; a turn that ignores cancellation reports a
shutdown error instead of being mistaken for a clean drain.
