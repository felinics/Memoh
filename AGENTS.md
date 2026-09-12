# AGENTS.md

## Working Language

The primary working language for this repository is **Chinese (中文)**, including issue and PR titles and bodies, review comments, and commit/PR discussion. Other languages (e.g. English) are not rejected — quoted code, error logs, and upstream English material stay as-is — but default to Chinese whenever you author new content.

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

Top-level layout — see `docs/codebase-map.md` for the full annotated tree, container/workspace internals, the desktop bootstrap, and recent major subsystems:

- `cmd/` — Go entry points (`agent`, `channel`, `bridge`, `mcp`, `synccaps`, …)
- `internal/` — Go backend domain packages (`agent`, `channel`, `chat`, `workspace`, …)
- `apps/` — `web` (Vue 3 management UI) and `desktop` (Electron native client)
- `packages/` — shared TypeScript libraries (`ui`, `sdk`, `icons`, `config`)
- `db/postgres/` — migrations + sqlc queries; `conf/` — config templates; `docker/` — production Docker; `devenv/` — dev environment; `docs/` — reference docs

## Development Guide

### Local Conventions

Before making changes to a directory, check whether that directory (or its nearest parent application/package directory) contains an `AGENTS.md`. If it does, read it first. Local files contain domain-specific conventions that override or extend this root guide.

Key local developer guides:
- `apps/web/AGENTS.md` — web frontend architecture, routing, page conventions, and i18n rules.
- `apps/desktop/AGENTS.md` — Electron shell, hosted-server bootstrap, tray/menu/preload rules.
- `packages/ui/AGENTS.md` — UI-owned entry point for the design contract and adjacent Web composition guidance. Read it before Web/UI work; do not duplicate its skills under `.agents/skills/`.

Reference docs moved out of this file:
- `docs/development.md` — prerequisites, `mise` commands, Docker deployment
- `docs/codebase-map.md` — full directory tree and subsystem internals
- `docs/database.md` — migration rules and the database table reference
- `docs/configuration.md` — `config.toml` sections and templates
- `docs/agent-runtime.md` — agent runtime internals

### README Localization

- Keep `README.md`, `README_CN.md`, and `README_JA.md` in sync when changing public README content, navigation links, install snippets, or waitlist/product announcements.
- For Japanese copy, use natural Japanese phrasing while preserving product and technical terms that Japanese users commonly read in English, such as Agent, Bot, Workspace, MCP, Browser Use, Computer Use, SaaS, Desktop, and Web UI.

Bot persona templates (not developer guides):
- `templates/workspace/AGENTS.md`

### Dev Component Wall & UI Contract Guard

- The dev component wall at `apps/web/src/pages/dev/components/` is the living reference for `@felinic/ui` components and tokens. Use it to verify visual changes locally.
- `scripts/check-ui-contract.mjs` is a mechanical guard wired into `mise run lint`. It enforces the design token contract from `packages/ui/AGENTS.md` (no raw colors, no invented shadows, no off-list arbitrary radius). Run lint before committing UI changes.

### OSS vs Cloud: Where to Open a PR

Memoh is a commercial project split across two repositories: this OSS repo and a private Cloud repo. The two codebases are similar but not identical — some PRs belong in OSS, others in Cloud. Before opening a PR, decide which repo it targets. Cloud periodically syncs from OSS, but the sync can silently skip some changes, so verify what actually landed instead of assuming the sync covered it.

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
4. Migration conventions (canonical `0001_init.up.sql`, paired `.up`/`.down` files, idempotent DDL) and the table reference live in `docs/database.md` — read it before writing migrations.

### API Development Workflow

1. Write handlers in `internal/handlers/` with swaggo annotations.
2. Run `mise run swagger-generate` to update the OpenAPI docs (output in `spec/`).
3. Run `mise run sdk-generate` to update the frontend TypeScript SDK (`packages/sdk/`).
4. The frontend calls APIs via the auto-generated `@memohai/sdk`.

### Agent Development

- The AI agent runs **in-process** within the Go server — there is no separate agent gateway service.
- `internal/agent/` is a bounded-package namespace. It intentionally contains no root Go package.
- `internal/agent/application/` owns turn orchestration: message assembly, history, memory, compaction, decisions, persistence, and runtime dispatch.
- `internal/agent/turn/` is the pure port used by Channel and by the authenticated in-process/gRPC transports. Internal code says Thread; compatibility adapters keep external `session_id` and existing gRPC fields stable.
- Model/client types are defined in `internal/models/types.go`: `openai-completions`, `openai-responses`, `anthropic-messages`, `google-generative-ai`, `openai-codex`, `github-copilot`, `edge-speech`. Model types: `chat`, `embedding`, `speech`.
- Tools are implemented as `ToolProvider` instances in `internal/agent/tool/`, loaded via setter injection to avoid FX dependency cycles.
- **Tool usage lives with the tool, never in the static prompt.** Per-tool usage goes in `sdk.Tool.Description`; cross-tool workflow guidance goes in an optional `tools.ToolUsage` `Usage()` method that `assembleTools` injects only when that provider registers tools for the session. `internal/agent/runtime/native/prompt_test.go` guards this.
- Prompt templates are embedded from `internal/agent/runtime/native/prompts/`. Partials are prefixed with `_`; system prompting combines `system_common.md` with mode-specific prompts such as `mode_chat.md` and `mode_discuss.md`.
- `internal/chat/` owns internal chat state (`thread`, `message`, `timeline`, `view`) and does not orchestrate Agent execution. Inbound adaptation lives in `internal/channel/inbound/`; discuss-mode driving in `internal/channel/discuss/`.
- Browser Use tools (`browser_action` / `browser_observe` / `browser_remote_session`) drive the headed workspace Chrome over CDP; Computer Use (`computer_observe` / `computer_action`) drives the GUI desktop via AT-SPI + RFB. Screenshots are saved to a workspace path and must be explicitly read. Prefer Browser Use for web pages; Computer Use for native dialogs and non-browser GUI states.
- Runtime internals (Twilight `Stream()`/`Generate()`, compaction, loop detection, tag extraction, full Browser/Computer Use details): `docs/agent-runtime.md`.

### Frontend Development

- Use Vue 3 Composition API with `<script setup>` style.
- Shared components belong in `packages/ui/`; API calls use `@memohai/sdk`; state uses Pinia + Pinia Colada; i18n via vue-i18n.
- See `apps/web/AGENTS.md` for detailed frontend conventions.

### Desktop App

- `apps/desktop/` is an [electron-vite](https://electron-vite.github.io/) Electron shell (`@memohai/desktop`) that reuses `@memohai/web` modules for Memoh Cloud or a hosted server. It must not start a server, package database files, embed Qdrant, or install a companion CLI.
- When desktop needs to diverge from web, extend the desktop bootstrap or add explicit `@memohai/web` subpath exports. Do **not** fork `apps/web`.
- Bootstrap details (memory-history router, tray, packaging): `apps/desktop/AGENTS.md` and `docs/codebase-map.md`.

### Container / Workspace Management

- Each bot can have an isolated **workspace container** (file editing, exec, MCP tool hosting, optional headed browser/display); host↔container communication is a **gRPC bridge over Unix Domain Sockets**, not TCP.
- The canonical workspace image is built from `docker/Dockerfile.workspace`; per-bot managed dependencies install into `/data` via `internal/workspacedeps/`.
- `internal/container/` provides the runtime abstraction (`docker`, `containerd`, `apple` adapters). Snapshot/storage semantics differ by backend; do not assume containerd-style snapshot lineage for Docker or archive-backed flows.
- Workspace dependencies (`internal/workspacedeps/`): launcher resolution is read-only — discover existing CLIs without a warm Supermarket cache; chat and device-code login never authorize installation — a Manage-authorized action confirms a frozen recipe revision; remote dependency management does not imply remote runtime support (direct Codex/Claude Code requires a native workspace). Upgrade boundary: `docs/workspace-dependencies-upgrade.md`.
- Bridge layout, image contract, and display stack details: `docs/codebase-map.md`.

## Database Tables

The canonical source of truth for the full PostgreSQL schema is `db/postgres/migrations/0001_init.up.sql`. Table reference by domain: `docs/database.md`.

## Configuration

`config.toml` section reference and the available templates (`conf/`, `devenv/`): `docs/configuration.md`.

## Web Design

Please refer to `./apps/web/AGENTS.md`.
