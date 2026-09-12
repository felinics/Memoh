# Codebase Map

> Moved from `AGENTS.md` to keep the always-on project rules under the context injection limit. `AGENTS.md` links here where this material is needed.

## Project Structure

```
Memoh/
├── cmd/                        # Go application entry points
│   ├── agent/                  #   Main backend server (embedded all-in-one or split-mode server process)
│   ├── channel/                #   Standalone channel service (split mode): platform adapters, email, webhooks
│   ├── internal/               #   Shared FX composition roots
│   │   ├── core/               #     Core (agent/db/api) module assembly
│   │   └── channel/            #     Channel runtime module assembly (ServerLocal / Runtime / Embedded)
│   ├── bridge/                 #   In-container gRPC bridge (UDS-based, runs inside bot containers; supervises optional display/browser helpers)
│   │   └── template/           #     Prompt templates for bridge (TOOLS.md, SOUL.md, IDENTITY.md, etc.)
│   ├── gen-bridge-mtls/        #   Bridge mTLS certificate generator
│   ├── mcp/                    #   MCP stdio transport binary
│   └── synccaps/               #   Build-time sync of provider template capabilities from the LiteLLM registry
├── internal/                   # Go backend core code (domain packages)
│   ├── accounts/               #   User account management (CRUD, password hashing)
│   ├── acl/                    #   Access control list (source-aware chat trigger ACL)
│   ├── arch/                   #   Architecture guard tests (channel-boundary import rules, spec §8)
│   ├── agent/                  #   Agent bounded package; root is a namespace, not a Go package
│   │   ├── adapter/            #     Adapters from lower-level domain ports to runtime implementations
│   │   ├── application/        #     Turn orchestration: context, persistence, memory, approvals, runtime dispatch
│   │   ├── background/         #     Background task manager (spawned subagents, video tasks)
│   │   ├── context/            #     Context fragments, history projection, compaction, and output limits
│   │   ├── decision/           #     User input, tool approval, and stable user-facing feedback
│   │   ├── event/              #     Agent events and transport payload vocabulary
│   │   ├── runtime/            #     Runtime implementations
│   │   │   ├── acp/            #       ACP pool, client process manager, and the generic custom-agent profile
│   │   │   ├── claudecode/     #       Claude Code direct runtime (external.Driver)
│   │   │   ├── codex/          #       Codex direct runtime (external.Driver)
│   │   │   ├── external/       #       Neutral Driver port between the application layer and external runtimes
│   │   │   ├── native/         #       Twilight AI native runtime, prompts, streaming, hooks, and guards
│   │   │   ├── agentstate/     #       External Agent session publication heads and state storage port
│   │   │   ├── session/        #       Per-thread runtime state and control
│   │   │   └── toolmount/      #       Memoh tool gateway mounts for direct runtimes
│   │   ├── sessionmode/        #     Session mode resolution
│   │   ├── tool/               #     Native tool providers (package name remains tools)
│   │       ├── message.go      #       Send message tool
│   │       ├── contacts.go     #       Contact list tool
│   │       ├── schedule.go     #       Schedule management tool
│   │       ├── memory.go       #       Memory read/write tool
│   │       ├── web.go          #       Web search tool
│   │       ├── webfetch.go     #       Web page fetch tool
│   │       ├── browser.go      #       Browser Use (headed workspace Chrome over CDP)
│   │       ├── computer_a11y.go #      Computer Use (AT-SPI accessibility + RFB input)
│   │       ├── container.go    #       Container file/exec tools
│   │       ├── fsops.go        #       Filesystem operations tool
│   │       ├── apply_patch.go  #       Patch application tool
│   │       ├── ask_user.go     #       In-conversation user input tool
│   │       ├── background.go   #       Background task tool
│   │       ├── email.go        #       Email send tool
│   │       ├── subagent.go     #       Sub-agent invocation tool
│   │       ├── skill.go        #       Skill activation tool
│   │       ├── tts.go          #       Text-to-speech tool
│   │       ├── transcribe.go   #       Audio transcription tool
│   │       ├── federation.go   #       MCP federation tool
│   │       ├── image_gen.go    #       Image generation tool
│   │       ├── video_gen.go    #       Video generation tool
│   │       ├── prune.go        #       Pruning tool
│   │       ├── history.go      #       History access tool
│   │       └── read_media.go   #       Media reading tool
│   │   └── turn/               #     Pure Turn port plus authenticated gRPC transport
│   ├── attachment/             #   Attachment normalization (MIME types, base64)
│   ├── audio/                  #   Audio/TTS processing utilities
│   ├── auth/                   #   JWT authentication middleware and utilities
│   ├── boot/                   #   Runtime configuration provider (container backend detection)
│   ├── bots/                   #   Bot management (CRUD, lifecycle)
│   ├── botbackup/              #   Bot backup/export/import service
│   ├── capabilities/           #   Model reasoning capability derivation (LiteLLM registry)
│   ├── channel/                #   Channel adapter system
│   │   ├── adapters/           #     Platform adapters: telegram, discord, feishu, qq, dingtalk, weixin, wecom, wechatoa, matrix, misskey, line, slack, local
│   │   ├── discuss/            #     Discuss-mode driver
│   │   ├── inbound/            #     Inbound adaptation and Turn dispatch
│   │   ├── route/              #     External conversation/thread to internal Thread routing
│   │   └── identities/         #     Channel identity service
│   ├── channelaccess/          #   Effective per-bot Manage capability (channel binding + override)
│   ├── chat/                   #   Chat bounded package
│   │   ├── event/              #     Persisted chat event hub
│   │   ├── message/            #     Message persistence
│   │   ├── thread/             #     Internal Thread lifecycle and forks
│   │   ├── timeline/           #     Canonical events, projection, rendering, and persistence
│   │   └── view/               #     API/UI history projection
│   ├── command/                #   Slash command system (extensible command handlers)
│   ├── config/                 #   Configuration loading and parsing (TOML + YAML providers)
│   ├── container/              #   Container runtime abstraction + adapters (containerd, Apple, Docker)
│   ├── copilot/                #   GitHub Copilot client integration
│   ├── db/                     #   Database connection and migration utilities
│   │   ├── postgres/           #     PostgreSQL store adapters
│   │   │   └── sqlc/           #     ⚠️ Auto-generated by sqlc — DO NOT modify manually
│   │   └── store/              #     Transitional Queries interface shared by domain services
│   ├── email/                  #   Email provider and outbox management (Mailgun, generic SMTP, OAuth)
│   ├── embedded/               #   Embedded filesystem assets (web only)
│   ├── display/                #   Workspace display service (Xvnc/RFB/WebRTC sessions and input forwarding)
│   ├── fetchproviders/         #   Web-fetch provider management (native, Jina, Cloudflare Markdown)
│   ├── handlers/               #   HTTP request handlers (REST API endpoints)
│   ├── healthcheck/            #   Health check adapter system (MCP, channel checkers)
│   ├── hooks/                  #   Bot-defined lifecycle hooks (PreToolUse, TurnEnd, … from hooks.json)
│   ├── identity/               #   Identity type utilities (human vs bot)
│   ├── i18n/                   #   Command and message internationalization
│   ├── logger/                 #   Structured logging (slog)
│   ├── mcp/                    #   MCP protocol manager (connections, OAuth, tool gateway)
│   ├── media/                  #   Content-addressed media asset service
│   ├── memory/                 #   Long-term memory system (multi-provider: Qdrant, BM25, LLM extraction)
│   ├── messaging/              #   Outbound message executor
│   ├── models/                 #   LLM model management (CRUD, variants, client types, probe)
│   ├── network/                #   Workspace container network configuration
│   ├── oauthclients/           #   Built-in OAuth client registry (TOML)
│   ├── oauthctx/               #   OAuth context helpers
│   ├── policy/                 #   Access policy resolution (guest access)
│   ├── providers/              #   LLM provider management (OpenAI, Anthropic, etc.)
│   ├── prune/                  #   Text pruning utilities (truncation with head/tail)
│   ├── registry/               #   Provider registry service (YAML provider templates)
│   ├── rpc/                    #   Internal server↔channel RPC (shared-secret auth, runtime method fan-out)
│   ├── schedule/               #   Scheduled task service (cron)
│   ├── searchproviders/        #   Search engine provider management (Brave, etc.)
│   ├── server/                 #   HTTP server wrapper (Echo setup, middleware, shutdown)
│   ├── settings/               #   Bot settings management
│   ├── apps/          #   Installed Supermarket App state
│   ├── skills/                 #   Skill registry and activation
│   ├── slash/                  #   Slash-command classification and metadata (channel + web surfaces)
│   ├── storage/                #   Storage provider interface (filesystem, container FS)
│   ├── supermarket/            #   Supermarket protocol client and App installer
│   ├── team/                   #   Singleton team identity (DefaultTeamID)
│   ├── textutil/               #   UTF-8 safe text utilities
│   ├── timezone/               #   Timezone utilities
│   ├── toolapproval/           #   Tool call approval flow
│   ├── userinput/              #   In-conversation user input requests (ask_user tool)
│   ├── version/                #   Build-time version information
│   ├── video/                  #   Video generation provider/model service
│   ├── webhooktunnel/          #   Webhook tunnel manager (cloudflared) for channels behind NAT
│   └── workspace/              #   Workspace container lifecycle management
│       ├── manager.go          #     Container reconciliation, gRPC connection pool
│       ├── manager_lifecycle.go #    Container create/start/stop operations
│       ├── bridge/             #     gRPC client for in-container bridge service
│       └── bridgepb/           #     Protobuf definitions (bridge.proto)
├── apps/                       # Application services
│   ├── desktop/                #   Native Electron app (@memohai/desktop): hosted-server renderer, tray, menus, preload IPC
│   └── web/                    #   Main web app (@memohai/web, Vue 3) — see apps/web/AGENTS.md
├── packages/                   # Shared TypeScript libraries
│   ├── ui/                     #   Shared UI component library (@felinic/ui) — git submodule → github.com/felinics/ui; its AGENTS.md routes agents to the UI-owned Web guidance
│   ├── sdk/                    #   TypeScript SDK (@memohai/sdk, auto-generated from OpenAPI)
│   ├── icons/                  #   Brand/provider icon library (@memohai/icon)
│   └── config/                 #   Shared configuration utilities (@memohai/config)
├── crates/                     # Rust crates packaged into the workspace toolkit
│   └── a11y-cli/               #   AT-SPI accessibility helper used by Computer Use
├── spec/                       # OpenAPI specifications (swagger.json, swagger.yaml)
├── db/                         # Database
│   └── postgres/               #   PostgreSQL SQL resources
│       ├── migrations/         #   SQL migration files
│       └── queries/            #   SQL query files (sqlc input)
├── conf/                       # Configuration
│   ├── providers/              #   Provider YAML templates (openai, anthropic, codex, github-copilot, etc.)
│   ├── app.example.toml        #   Default config template
│   ├── app.docker.toml         #   Docker deployment config
│   ├── app.apple.toml          #   macOS (Apple Virtualization) config
│   └── app.windows.toml        #   Windows config
├── devenv/                     # Dev environment
│   ├── docker-compose.yml      #   Main dev compose
│   ├── docker-compose.selinux.yml # SELinux overlay compose
│   └── app.dev.toml            #   Dev config (connects to devenv docker-compose)
├── docker/                     # Production Docker (Dockerfiles, entrypoints, nginx.conf, toolkit/)
├── scripts/                    # Utility scripts (db-up, db-drop, release, install, sync-openrouter-models)
├── docker-compose.yml          # Docker Compose orchestration (production)
├── mise.toml                   # mise tasks and tool version definitions
├── sqlc.yaml                   # sqlc code generation config
├── openapi-ts.config.ts        # SDK generation config (@hey-api/openapi-ts)
├── bump.config.ts              # Version bumping config (bumpp)
├── vitest.config.ts            # Test framework config (Vitest)
├── tsconfig.json               # TypeScript monorepo config
└── eslint.config.mjs           # ESLint config
```

## Desktop App

- `apps/desktop/` is an [electron-vite](https://electron-vite.github.io/) project (`@memohai/desktop`) with its own managed renderer bootstrap for Memoh Cloud or a hosted Memoh server. It reuses exported `@memohai/web` pages, layouts, stores, i18n, API setup, and design tokens, but owns the Electron shell instead of importing the full web `main.ts`.
- The desktop app boots its renderer with a memory-history router, desktop shell injection, native menu/keyboard integration, native chrome, and system tray reopen/quit behavior.
- Desktop connects to the target server through `MEMOH_DESKTOP_BASE_URL` and must not start a server, package database files, embed Qdrant, or install a companion CLI.
- Packaging is handled by `electron-builder` (config in `apps/desktop/electron-builder.yml`); output lands in `apps/desktop/dist/`.
- When desktop needs to diverge from the web experience, extend the desktop bootstrap or add explicit `@memohai/web` subpath exports plus desktop type stubs. Do **not** fork `apps/web` itself.

## Container / Workspace Management

- Each bot can have an isolated **workspace container** for file editing, command execution, MCP tool hosting, and optional headed browser/desktop display sessions.
- Container workspaces communicate with the host via a **gRPC bridge** over Unix Domain Sockets (UDS), not TCP.
- The bridge binary (`cmd/bridge/`) runs inside each container as a read-only file mount, with UDS sockets under `/run/memoh/`. Toolkit binaries (node, python, uv), display dependencies, and runtime scripts come from the workspace image; agent CLIs and other managed dependencies are installed per bot into `/data` by the workspace dependency manager (`internal/workspacedeps/`). When display is enabled the bridge can supervise Xvnc and a headed Chrome/Chromium process with CDP on port `9222`; the web UI then exposes a Display pane backed by screenshots/WebRTC/input forwarding. Treat VNC as the container desktop transport, not as the whole browser automation feature.
- The canonical workspace image is built from `docker/Dockerfile.workspace`. There is no image-level compatibility check: custom/provider images that expose the same toolkit and script paths (`/opt/memoh/toolkit`, `/opt/memoh/scripts`) get the same base capabilities, and anything missing is reported at the point of use or discovered as an installable dependency.
- `internal/workspace/` manages workspace lifecycle (create, start, stop, reconcile) and maintains a bridge gRPC connection pool for container runtimes.
- `internal/container/` provides the container runtime abstraction layer and adapter subpackages (`docker`, `containerd`, `apple`). Snapshot/storage semantics differ by backend; do not assume containerd-style snapshot lineage for Docker or archive-backed flows.
- SSE-based progress feedback is provided during container image pull and creation.


## Recent Major Subsystems

The codebase has grown beyond the original agent/channel/container core. When working near these areas, read the local `AGENTS.md` and treat the corresponding `internal/` package as the source of truth; do not guess tool or schema details.

- **External coding-agent runtimes (`internal/agent/runtime/external/`, `codex/`, `claudecode/`)** — the neutral `external.Driver` port plus the direct Codex and Claude Code runtimes (pinned protocol assets, device-code/OAuth login, native thread resume/fork, Memoh tool gateway mounts via `toolmount/`). **ACP (`internal/agent/runtime/acp/`)** is the generic channel for custom user-supplied ACP agents (single generic profile with a managed launch command), folded into the same driver port. Stable user-facing runtime errors live in `internal/agent/decision/feedback/`.
- **Workspace dependencies (`internal/workspacedeps/`)** — per-bot installation of agent CLIs and other managed dependencies into `/data`. The permission invariants (read-only launcher resolution, Manage-authorized install confirmation, no remote runtime support) are normative rules — see `AGENTS.md` → Container / Workspace Management. The Server/image upgrade boundary is documented in `docs/workspace-dependencies-upgrade.md`.
- **Apps (`internal/apps/`, `internal/supermarket/`)** — Supermarket App discovery and installation state. Installed Apps expand into immutable Registry Skills in the selected workspace target.
- **User input / `ask_user` (`internal/agent/decision/input/`)** — lets the in-process agent ask the user a question mid-conversation and wait for an answer.
- **Bot backup / import / export (`internal/botbackup/`)** — archive-based bot portability with preview and merge/replace/skip strategies.
- **Workspace resource limits (`internal/workspace/resource_limits.go`)** — per-bot CPU/memory/storage quotas and runtime metrics.
