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
