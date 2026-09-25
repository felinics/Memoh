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
- Browser Use / Computer Use calls are validated against a per-tool action contract (`internal/agent/tool/gui_contract.go`, with the definitions in `browser_contract.go` and `computer_contract.go`) before anything touches the browser or desktop. One spec per action drives the generated tool schema (the `action`/`observe` enum, including the compatibility aliases `dblclick`, `scrollintoview`, and `keyboard_inserttext`), the per-action reference text the model reads, and the pre-execution checks: parameters that do not belong to the chosen action, unknown enum values, out-of-range numbers, conflicting locators (`ref` + `selector`, an element plus `x`/`y`, half a coordinate pair), `timeout` together with `duration_ms`, and `double_click` with a conflicting `click_count` are all rejected with no side effect. `timeout` is only a readiness bound (navigate/reload/history/wait-for-element, default 30 s, failures are returned instead of swallowed); `duration_ms` is the fixed pause for `wait` without a target. `fill` accepts an empty string to clear a field on both tools; the Computer fallback path clears with Select All + BackSpace before typing.
- The `a11y-cli` helper and `internal/agent/tool/computer_a11y.go` share a versioned JSON contract (`PROTOCOL_VERSION` in `crates/a11y-cli/src/main.rs`, `a11yProtocolVersion` in Go). Snapshot output carries `protocol_version`, `helper_version`, `limit`, `truncated`, `lines` (array), per-item `x/y/width/height` and interesting `states`, plus walk diagnostics (counters are returned to the model; the bus address stays in server logs). Output from any other protocol version, or a helper that rejects the `locate` subcommand or `--limit`, is reported as an outdated workspace image instead of being decoded into an empty tree. `a11y-cli locate --ref eN` resolves a ref from the persisted index without re-walking the tree, which is how double/triple clicks and middle/right buttons on a ref are replayed as real pointer events (AT-SPI's default action fires once and has no button). Elements whose box is missing or unrealised (GTK tree cells report extents near `i32::MIN`) never become pointer targets; such refs return an explicit error for pointer-only actions.
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

## Codex session checkpoints

The direct Codex runtime persists its primary native rollout through the existing
`agent_session_state_lines`, `agent_session_states`, and
`agent_session_publications` tables. A completed turn or compaction, and a turn
the user interrupted, waits for its own terminal JSONL record (`task_complete`,
`turn_aborted`), proves the existing prefix unchanged, and stages only the new
records in PostgreSQL. The application publishes the checkpoint in the same
transaction as the round or compaction operation, so an aborted half-round the
user can see is also what Codex remembers. Failed turns retain the preceding
publication: Codex does not reliably write a terminal record for them.

The publication is authoritative; `codex_thread_id` is a lookup hint. Warm
threads are reused only while their checkpoint matches the publication. Cold
resume validates the local rollout against the committed record count and
digest, restores missing or different files, and passes the explicit rollout
path to `thread/resume`. This also works when Codex's local SQLite index is gone.
A dirty loaded thread must acknowledge `thread/closed` before its rollout is
restored.

Checkpointing never makes a thread unusable. A completed turn that cannot be
staged still commits its round and publishes a reset head instead of failing.
Restoring the snapshot is what rolls unpublished native writes back, and it is
best effort: with no snapshot to restore — the head is a reset, its rows are
missing or do not match, or the stale thread never unloads — the thread
continues from Codex's own files by `codex_thread_id`, as sessions without a
publication do. Those conditions are identical on every later turn, so failing
would never clear. Only when Codex itself refuses `thread/resume` is its memory
already gone: the turn starts a new native thread and says so with a
`native_history_lost` runtime notice. Failures a retry can cure (reading the
publication, restoring files, transport errors) still surface as
`session_runtime.history_inconsistent`.

New threads use self-contained legacy history, not paginated history with
external base references. Checkpoints contain the primary conversation rollout,
not workspace files, credentials, native goals, or live child-agent processes.
Existing sessions without a database publication continue from their local
native state and gain a checkpoint after their next completed turn. Newly
forked sessions likewise gain their own checkpoint after their first completed
turn; the fork operation reads an independent temporary copy of the source's
committed rollout without replacing files of a concurrently running source.
Rollouts saved by the former ACP integration under `state/sessions/` remain
readable.
