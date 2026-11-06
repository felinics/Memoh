# Configuration

> Moved from `AGENTS.md` to keep the always-on project rules under the context injection limit. `AGENTS.md` links here where this material is needed.

## Configuration

The main configuration file is `config.toml` (copied from `conf/app.example.toml` or environment-specific templates for development), containing:

- `[log]` — Logging configuration (level, format)
- `[server]` — HTTP listen address and optional `public_url` for the externally reachable Web origin (for example, `https://app.example.com`). Set `MEMOH_SERVER_PUBLIC_URL` to override it. Capability tools use this base URL for credential setup links and the MCP OAuth callback at `/api/oauth/mcp/callback`. Configure it for channel conversations; without it, setup links are relative and OAuth keeps the existing local callback default. Do not include credentials, query parameters, or a fragment.
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
- `[registry]` — Provider registry (`providers_dir` pointing to `conf/providers/`; speech, transcription and video templates only)
- `[model_catalog]` — Server-owned public model metadata (`url` and `cache_path`); see [Model catalog](model-catalog.md) for cache and Cloud compatibility behavior
- `[supermarket]` — Supermarket integration (base_url)
- `[agent]` — Agent runtime limits (`tool_output_max_bytes`, `tool_output_max_lines`, `system_files_max_bytes`, `context_absolute_max_tokens`) and `on_demand_tools`: `auto` (default) keeps rarely used tool groups (schedules, browser/desktop, MCP and App management) out of each request until the model loads them with `tool_describe` and runs them with `tool_call`; `off` sends every tool definition with every request

Provider YAML templates in `conf/providers/` define preset configurations for speech, transcription and video providers only. LLM providers come from the hand-written gallery in `apps/web/src/constants/model-sources.ts`; the server fetches, validates, converts and caches their [models.dev](https://models.dev) catalogs. Importing from the provider's own `/models` endpoint remains a separate action. Cloud managed providers retain their authenticated platform catalog.

Configuration templates available in `conf/`:
- `app.example.toml` — Default template
- `app.docker.toml` — Docker deployment
- `app.apple.toml` — macOS (Apple Virtualization backend)
- `app.windows.toml` — Windows

Development configuration in `devenv/`:
- `app.dev.toml` — Development (connects to devenv docker-compose)
