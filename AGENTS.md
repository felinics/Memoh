# AGENTS.md

## Project Overview

Memoh is a multi-member, structured long-memory AI agent platform with isolated workspace runtimes. Users can create AI bots and chat with them via Telegram, Discord, Lark (Feishu), DingTalk, WeChat, Matrix, Email, and more. Each bot can use an independent container workspace to edit files, execute commands, run tools, and build itself while keeping runtime ownership explicit.

The public documentation site is maintained separately in `felinics/memoh-docs`.

## Architecture Overview

Deploy/server mode consists of three core services:

| Service | Tech Stack | Port | Description |
|---------|-----------|------|-------------|
| **Server** (Backend) | Go + Echo | 8080 | Main service: REST API, auth, database, container management, **in-process AI agent** |
| **Channel** (Backend) | Go + Echo | 8081 | External channel adapters, email delivery, and channel webhook endpoints; delegates agent turns to Server over authenticated internal gRPC. Split mode is opt-in via `internal_rpc.shared_secret` (docker compose sets it); without it the Server embeds the channel runtime and runs all-in-one |
| **Web** (Frontend) | Vue 3 + Vite | 8082 | Management UI: visual configuration for Bots, Models, Channels, etc. |

The native desktop client is a separate distribution boundary for Memoh Cloud or a hosted Memoh server. `apps/desktop` reuses `@memohai/web` modules, but owns the Electron shell, system tray behavior, menus, preload IPC, cache invalidation, and packaged application resources.

Infrastructure dependencies:
- **PostgreSQL** — Relational data storage
- **Qdrant** — Vector database for memory semantic search
- **Workspace runtime** — Isolated containers per bot via Docker, containerd v2, or Apple Virtualization

## Tech Stack

### Backend (Go)
- **Framework**: Echo (HTTP)
- **Dependency Injection**: Uber FX
- **AI SDK**: [Twilight AI](https://github.com/felinics/twilight) (Go LLM SDK — OpenAI, Anthropic, Google)
- **Database Driver**: pgx/v5 (PostgreSQL)
- **Code Generation**: sqlc (SQL → Go)
- **API Docs**: Swagger/OpenAPI (swaggo)
- **MCP**: modelcontextprotocol/go-sdk
- **Containers / Workspaces**: Docker / containerd v2 / Apple Virtualization adapters

### Frontend (TypeScript)
- **Framework**: Vue 3 (Composition API)
- **Build Tool**: Vite 8
- **State Management**: Pinia 3 + Pinia Colada
- **UI**: Tailwind CSS 4 + custom component library (`@felinic/ui`) + Reka UI
- **Icons**: lucide-vue-next + `@memohai/icon` (brand/provider icons)
- **i18n**: vue-i18n
- **Markdown**: markstream-vue + Shiki + Mermaid + KaTeX
- **Desktop**: Electron 34 + [electron-vite](https://electron-vite.github.io/) 4 native client, reusing `@memohai/web` modules while managing the desktop renderer, tray behavior, menus, and preload IPC
- **Package Manager**: pnpm monorepo

### Tooling
- **Task Runner**: mise
- **Package Managers**: pnpm (frontend monorepo), Go modules (backend)
- **Linting**: golangci-lint (Go), ESLint + typescript-eslint + vue-eslint-parser (TypeScript)
- **Testing**: Vitest
- **Version Management**: bumpp
- **SDK Generation**: @hey-api/openapi-ts (with `@hey-api/client-fetch` + `@pinia/colada` plugins)

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
│   │   │   ├── acp/            #       ACP pool, client process manager, and profiles
│   │   │   ├── native/         #       Twilight AI native runtime, prompts, streaming, hooks, and guards
│   │   │   └── session/        #       Per-thread runtime state and control
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
│   ├── skillpackages/          #   Installed Supermarket Package state
│   ├── skills/                 #   Skill registry and activation
│   ├── slash/                  #   Slash-command classification and metadata (channel + web surfaces)
│   ├── storage/                #   Storage provider interface (filesystem, container FS)
│   ├── supermarket/            #   Supermarket protocol client and Package installer
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

## Development Guide

### Local Conventions

Before making changes to a directory, check whether that directory (or its nearest parent application/package directory) contains an `AGENTS.md`. If it does, read it first. Local files contain domain-specific conventions that override or extend this root guide.

Key local developer guides:
- `apps/web/AGENTS.md` — web frontend architecture, routing, page conventions, and i18n rules.
- `apps/desktop/AGENTS.md` — Electron shell, hosted-server bootstrap, tray/menu/preload rules.
- `packages/ui/AGENTS.md` — UI-owned entry point for the design contract and adjacent Web composition guidance. Read it before Web/UI work; do not duplicate its skills under `.agents/skills/`.

### README Localization

- Keep `README.md`, `README_CN.md`, and `README_JA.md` in sync when changing public README content, navigation links, install snippets, or waitlist/product announcements.
- For Japanese copy, use natural Japanese phrasing while preserving product and technical terms that Japanese users commonly read in English, such as Agent, Bot, Workspace, MCP, Browser Use, Computer Use, SaaS, Desktop, and Web UI.

Bot persona templates (not developer guides):
- `templates/workspace/AGENTS.md`
- `internal/workspace/templates/AGENTS.md`

### Prerequisites

1. Install [mise](https://mise.jdx.dev/)
2. Install toolchains and dependencies: `mise install`
3. Initialize the project: `mise run setup`
4. Start the dev environment: `mise run dev`
5. Dev web UI: `http://localhost:18082` (server: `18080`)

### Common Commands

| Command | Description |
|---------|-------------|
| `mise run dev` | Start the containerized dev environment (all services) |
| `mise run dev:selinux` | Start dev environment on SELinux systems |
| `mise run dev:down` | Stop the dev environment |
| `mise run dev:logs` | View dev environment logs |
| `mise run dev:restart` | Restart a service (e.g. `-- server`) |
| `mise run setup` | Install project dependencies and run code generation |
| `mise run sqlc-generate` | Regenerate PostgreSQL sqlc code after modifying SQL files |
| `mise run swagger-generate` | Generate Swagger documentation |
| `mise run sdk-generate` | Generate TypeScript SDK (depends on swagger-generate) |
| `mise run icons-generate` | Generate icon Vue components from SVG sources |
| `mise run db-up` | Initialize and migrate the database |
| `mise run db-down` | Drop the database |
| `mise run build-embedded-assets` | Build and stage embedded web assets |
| `mise run bridge:build` | Rebuild bridge binary in dev container |
| `mise run a11y-cli:build` | Build the Rust AT-SPI helper used by Computer Use (Linux output) |
| `mise run a11y-cli:check` | Run `cargo check` for the a11y-cli crate |
| `mise run desktop:dev` | Start Electron desktop app in dev mode (renderer reuses @memohai/web) |
| `mise run desktop:build` | Build Electron desktop app for release (electron-builder) |
| `mise run lint` | Run all linters (Go + ESLint) |
| `mise run lint:fix` | Run all linters with auto-fix |
| `mise run release` | Release new version (bumpp) |
| `mise run install-socktainer` | Install socktainer (macOS container backend) |
| `mise run dev:workspace-image` | Build and export the canonical workspace image for development (skipped when its inputs are unchanged) |
| `mise run dev:workspace-image:rebuild` | Force a rebuild of the dev workspace image (new base image, refreshed apt packages) |

### Dev Component Wall & UI Contract Guard

- The dev component wall at `apps/web/src/pages/dev/components/` is the living reference for `@felinic/ui` components and tokens. Use it to verify visual changes locally.
- `scripts/check-ui-contract.mjs` is a mechanical guard wired into `mise run lint`. It enforces the design token contract from `packages/ui/AGENTS.md` (no raw colors, no invented shadows, no off-list arbitrary radius). Run lint before committing UI changes.

### Docker Deployment

```bash
docker compose up -d        # Start all services
# Visit http://localhost:8082
```

Production deploy services are `postgres`, `pgvector`, `migrate`, `server`, `channel`, and `web`.
Optional profile: `webhook-tunnel` (cloudflared for channels behind NAT). Desktop connects to Memoh Cloud or this hosted server instead of running its own server.

### Pull Request QA Status

Most PR descriptions in this repository are written by AI agents, and an agent must never silently stand in for human verification. Every PR body must disclose its QA state:

- **Opening a PR** — if no human has verified the change yet (nobody has walked the happy path), end the description with this exact line:

  > ⚠️ **No human QA** — this PR has not been verified by a human yet. Remove this line once a human confirms the happy path.

- **After human confirmation** — when a human explicitly confirms QA (in chat, review, or a PR comment), remove the line from the description (e.g. via `gh pr edit`). Green CI, passing tests, typechecks, and the agent's own runs or screenshots never count as human QA; only an explicit human confirmation clears the line.
- **Updating the description later** — keep the line until a human confirms. If commits land after confirmation, judge whether they could break the verified happy path (typos, rebases, and comment/docs touch-ups cannot); if they could, restore the line until a human verifies the new head. The goal is disclosing the current QA state, not re-QA of every commit.

## Key Development Rules

### Database, sqlc & Migrations

1. **PostgreSQL SQL queries** are defined in `db/postgres/queries/*.sql`.
2. All Go files under `internal/db/postgres/sqlc/` are auto-generated by sqlc. **DO NOT modify them manually.**
3. After modifying any SQL files (migrations or queries), run `mise run sqlc-generate` to update generated Go code.

#### Migration Rules

PostgreSQL migrations live in `db/postgres/migrations/`:

- **PostgreSQL `0001_init.up.sql` is the canonical full PostgreSQL schema.** It always contains the complete, up-to-date PostgreSQL database definition (all tables, indexes, constraints, etc.). When adding PostgreSQL schema changes, you must **also update `db/postgres/migrations/0001_init.up.sql`** to reflect the final state.
- **Incremental PostgreSQL migration files** (`0002_`, `0003_`, ...) contain only the diff needed to upgrade an existing PostgreSQL database. They exist for environments that already have the schema and need to apply only the delta.
- **Naming**: `{NNNN}_{description}.up.sql` and `{NNNN}_{description}.down.sql`, where `{NNNN}` is a zero-padded sequential number (e.g., `0005`). Always use the next available number.
- **Paired files**: Every incremental migration **must** have both an `.up.sql` (apply) and a `.down.sql` (rollback) file.
- **Header comment**: Each file should start with a comment indicating the migration name and a brief description:
  ```sql
  -- 0005_add_feature_x
  -- Add feature_x column to bots table for ...
  ```
- **Idempotent DDL**: Use `IF NOT EXISTS` / `IF EXISTS` guards (e.g., `CREATE TABLE IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS`, `DROP TABLE IF EXISTS`) so migrations are safe to re-run.
- **Down migration must fully reverse up**: The `.down.sql` must cleanly undo everything its `.up.sql` does, in reverse order.
- **After creating or modifying migrations**, run `mise run sqlc-generate` to regenerate Go sqlc code, then validate the PostgreSQL migration path.

### API Development Workflow

1. Write handlers in `internal/handlers/` with swaggo annotations.
2. Run `mise run swagger-generate` to update the OpenAPI docs (output in `spec/`).
3. Run `mise run sdk-generate` to update the frontend TypeScript SDK (`packages/sdk/`).
4. The frontend calls APIs via the auto-generated `@memohai/sdk`.

### Agent Development

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

### Frontend Development

- Use Vue 3 Composition API with `<script setup>` style.
- Shared components belong in `packages/ui/`.
- API calls use the auto-generated `@memohai/sdk`.
- State management uses Pinia; data fetching uses Pinia Colada.
- i18n via vue-i18n.
- See `apps/web/AGENTS.md` for detailed frontend conventions.

### Desktop App

- `apps/desktop/` is an [electron-vite](https://electron-vite.github.io/) project (`@memohai/desktop`) with its own managed renderer bootstrap for Memoh Cloud or a hosted Memoh server. It reuses exported `@memohai/web` pages, layouts, stores, i18n, API setup, and design tokens, but owns the Electron shell instead of importing the full web `main.ts`.
- The desktop app boots its renderer with a memory-history router, desktop shell injection, native menu/keyboard integration, native chrome, and system tray reopen/quit behavior.
- Desktop connects to the target server through `MEMOH_DESKTOP_BASE_URL` and must not start a server, package database files, embed Qdrant, or install a companion CLI.
- Packaging is handled by `electron-builder` (config in `apps/desktop/electron-builder.yml`); output lands in `apps/desktop/dist/`.
- When desktop needs to diverge from the web experience, extend the desktop bootstrap or add explicit `@memohai/web` subpath exports plus desktop type stubs. Do **not** fork `apps/web` itself.

### Container / Workspace Management

- Each bot can have an isolated **workspace container** for file editing, command execution, MCP tool hosting, and optional headed browser/desktop display sessions.
- Container workspaces communicate with the host via a **gRPC bridge** over Unix Domain Sockets (UDS), not TCP.
- The bridge binary (`cmd/bridge/`) runs inside each container as a read-only file mount, with UDS sockets under `/run/memoh/`. Toolkit binaries, display dependencies, and runtime scripts come from the versioned workspace image contract. When display is enabled the bridge can supervise Xvnc and a headed Chrome/Chromium process with CDP on port `9222`; the web UI then exposes a Display pane backed by screenshots/WebRTC/input forwarding. Treat VNC as the container desktop transport, not as the whole browser automation feature.
- The canonical workspace image is built from `docker/Dockerfile.workspace`. Compatible custom/provider images must expose the same `/opt/memoh/workspace-contract.json`, toolkit, and script paths.
- `internal/workspace/` manages workspace lifecycle (create, start, stop, reconcile) and maintains a bridge gRPC connection pool for container runtimes.
- `internal/container/` provides the container runtime abstraction layer and adapter subpackages (`docker`, `containerd`, `apple`). Snapshot/storage semantics differ by backend; do not assume containerd-style snapshot lineage for Docker or archive-backed flows.
- SSE-based progress feedback is provided during container image pull and creation.

### Recent Major Subsystems

The codebase has grown beyond the original agent/channel/container core. When working near these areas, read the local `AGENTS.md` and treat the corresponding `internal/` package as the source of truth; do not guess tool or schema details.

- **ACP (`internal/agent/runtime/acp/`)** — runtime pool, client process manager, profiles, and OAuth integration for external ACP agents such as Claude Code and Codex. Stable user-facing ACP errors live in `internal/agent/decision/feedback/`.
- **Skill Packages (`internal/skillpackages/`, `internal/supermarket/`)** — Supermarket Package discovery and installation state. Installed Packages expand into immutable Registry Skills in the selected workspace target.
- **User input / `ask_user` (`internal/agent/decision/input/`)** — lets the in-process agent ask the user a question mid-conversation and wait for an answer.
- **Bot backup / import / export (`internal/botbackup/`)** — archive-based bot portability with preview and merge/replace/skip strategies.
- **Workspace resource limits (`internal/workspace/resource_limits.go`)** — per-bot CPU/memory/storage quotas and runtime metrics.

## Database Tables

The canonical source of truth for the full PostgreSQL schema is `db/postgres/migrations/0001_init.up.sql`. Key tables grouped by domain:

**Auth & Users**
- `users` — User accounts (username, email, role, display_name, avatar)
- `channel_identities` — Unified inbound identity subject (cross-platform)
- `user_channel_bindings` — Outbound delivery config per user/channel

**Bots & Sessions**
- `bots` — Bot definitions with model references and settings
- `bot_sessions` — Bot conversation sessions
- `bot_session_events` — Session event log
- `bot_channel_configs` — Per-bot channel configurations
- `bot_channel_routes` — Conversation route mapping (inbound thread → bot history)
- `bot_acl_rules` — Source-aware chat access control lists

**Messages & History**
- `bot_history_messages` — Unified message history under bot scope
- `bot_history_message_assets` — Message → content_hash asset links (with name and metadata)
- `bot_history_message_compacts` — Compacted message summaries

**User Input**
- `user_input_requests` — In-conversation questions posed by the `ask_user` tool, keyed by session and tool_call_id

**Providers & Models**
- `providers` — LLM provider configurations (name, base_url, api_key)
- `provider_oauth_tokens` — Provider-level OAuth tokens
- `user_provider_oauth_tokens` — Per-user provider OAuth tokens
- `models` — Model definitions (chat/embedding/speech types, modalities, reasoning, vision, tool calling)
- `model_variants` — Model variant definitions (weight, metadata)
- `search_providers` — Search engine provider configurations
- `memory_providers` — Multi-provider memory adapter configurations

**MCP**
- `mcp_connections` — MCP connection configurations per bot
- `mcp_oauth_tokens` — MCP OAuth tokens

**Skill Packages**
- `bot_skill_package_installations` — Installed Registry Package revision per bot and workspace target

**Containers**
- `containers` — Bot container instances
- `snapshots` — Container snapshots
- `container_versions` — Container version tracking
- `lifecycle_events` — Container lifecycle events
- `bot_workspace_resource_limits` — Per-bot CPU/memory/storage quotas

**Email**
- `email_providers` — Pluggable email service backends (Mailgun, generic SMTP)
- `email_oauth_tokens` — OAuth2 tokens for email providers (Gmail)
- `bot_email_bindings` — Per-bot email provider binding with permissions
- `email_outbox` — Outbound email audit log

**Scheduling & Automation**
- `schedule` — Scheduled tasks (cron)
- `schedule_logs` — Schedule execution logs
**Storage**
- `storage_providers` — Pluggable object storage backends
- `bot_storage_bindings` — Per-bot storage backend selection

## Configuration

The main configuration file is `config.toml` (copied from `conf/app.example.toml` or environment-specific templates for development), containing:

- `[log]` — Logging configuration (level, format)
- `[server]` — HTTP listen address
- `[admin]` — Admin account credentials
- `[auth]` — JWT authentication settings
- `[database]` — Database backend selection (`postgres`)
- `[container]` — Workspace container backend selection (`docker`, `containerd`, `apple`) and common workspace image/data/bridge/CNI settings
- `[containerd]` / `[docker]` / `[apple]` — Backend-specific runtime configuration
- `[postgres]` — PostgreSQL connection
- `[qdrant]` — Qdrant vector database connection
- `[sparse]` — Sparse (BM25) search service connection
- `[channel]` — Channel service HTTP/RPC listen addresses (split mode)
- `[internal_rpc]` — Server↔Channel internal RPC targets and shared secret; empty secret selects the embedded all-in-one mode
- `[web]` — Web frontend address
- `[registry]` — Provider registry (`providers_dir` pointing to `conf/providers/`)
- `[supermarket]` — Supermarket integration (base_url)

Provider YAML templates in `conf/providers/` define preset configurations for various LLM providers (OpenAI, Anthropic, GitHub Copilot, etc.).

Configuration templates available in `conf/`:
- `app.example.toml` — Default template
- `app.docker.toml` — Docker deployment
- `app.apple.toml` — macOS (Apple Virtualization backend)
- `app.windows.toml` — Windows

Development configuration in `devenv/`:
- `app.dev.toml` — Development (connects to devenv docker-compose)

## Web Design

Please refer to `./apps/web/AGENTS.md`.
