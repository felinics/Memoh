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
