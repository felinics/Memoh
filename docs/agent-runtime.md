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
- Internal chat state lives under `internal/chat/`: `thread` owns Thread lifecycle/forks, `message` owns persistence, `timeline` owns canonical event projection, and `view` owns API/UI history projection. These packages do not orchestrate Agent execution.
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
input the event keeps in place. When the replaced turn had compacted in place,
the same step moves the session to a new compaction epoch, so the attempt folds
the history before the cutoff into a summary of its own. Subagent and external
runtimes have separate ownership and remain outside this capability.

A native Discuss run writes each step as it commits, under the rows ingress
already wrote for its input. Each composed message names the inbound message,
stored reply or summary Channel rendered it from, at the version it rendered;
the version crosses the internal RPC with the command. The inputs of the batch
the run answers are anchors, so a fold reads only what the run's request
renders: a message that arrives during the run is the next batch's input, not
part of the summary. The steps before the run's answer mark their rows as such:
they are context for later runs but do not prove the batch consumed, so a worker
that comes back without a durable cursor still answers the input of a run that
failed after committing steps. The answer, the completed step that calls no
tool, is that proof.

`compaction.Service.Compact` executes one event that folds the rendered active
summary and every eligible source into one rolling summary. A pass folds what one
summarizer call can read; the event passes again only for what that call could
not, and each further pass absorbs the summary before it. The current request and
steers are verbatim anchors, not independent summary segments. The latest
tool/reasoning closure is protected. At approval or ask_user resume, the deferred
call/result is the tail; earlier committed work remains eligible. The summarizer
sees anchors inline but never claims their coverage.

The native runtime applies each committed replacement to its own request snapshot.
Uncovered messages retain their bytes. A result the read path writes for a deferred
call whose decision ended without one renders from the call's row, so a fold that
covers the call removes it too; a splice that leaves a call without its result, or a
result without its call, fails the event. `turn.PlaceSummary` also governs record and
pipeline replay: retained entries inside the covered span precede the summary.
Per-source coverage times and event cursors survive rollup, so refolding a summary
cannot hide an edit it never read. Stored message hashes survive replay projections;
RC versions are checked against their persisted event cursors. Restart replay keeps
the complete event chain for newer edits, including edits received before compaction
started. Artifact commit additionally verifies the full durable source hash.

Rendered artifacts, candidate payloads and claim verification use bounded reads.
In-run recovery does not load the raw history span or reread ancestor summaries;
legacy artifacts without exact coverage fail explicitly. Owner fencing, claim
reclamation and cancellation remain authoritative. Failed passes publish nothing;
successful earlier passes and tool effects stay committed.

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
- Context budget recovery before and during a native run pauses the model
  watchdog while its synchronous compaction runs; the model window restarts
  when recovery returns. Cancellation and execution budgets still apply.

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
record a marker and therefore retain the existing `lost` behavior. Once a resumed
run is admitted, ordinary failures are reported rather than automatically replayed.

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
