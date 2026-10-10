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
- The compaction service (`internal/agent/context/compaction/`) handles LLM-based conversation summarization.
- Loop detection (text and tool loops) is built into the agent with configurable thresholds.
- Tag extraction system processes inline tags in streaming output (attachments, reactions, speech/TTS).

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

## Native session state of direct runtimes

Codex and Claude Code keep their own conversation record on the bot's data
volume: Codex writes rollouts under `CODEX_HOME` (`/data/.codex/agents/<agent>/`),
Claude Code writes transcripts under `/data/.claude/projects/`. These files are
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
still exists before passing `--resume`.

When the native session cannot be resumed — Codex refuses `thread/resume`, or
the Claude transcript is gone — the turn starts a new native session and says
so with a `native_history_lost` runtime notice; the next turns continue from
Memoh's composed context. A transport failure while resuming is reported as
`external_runtime.session_resume_failed` instead, since a retry may still resume
the thread. Nothing here can make a thread permanently unusable.

Forks use Codex's native `thread/fork` on the running app-server; the branch
records its own rollout path. Threads created before this behaviour still
resume by their thread id and gain a rollout path after their next turn.

Migration 0156 removes the obsolete `agent_session_states` and
`agent_session_state_lines` tables and their saved snapshots.
`agent_session_publications` retains only the run ID used to fence ACP warm
processes; it contains no conversation state. Downgrading restores the old table
structure, not deleted snapshot data.


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
- Context budget recovery before the first native model call pauses the model
  watchdog while its synchronous compaction runs; the model window restarts
  when recovery returns. Cancellation and execution budgets still apply.

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
