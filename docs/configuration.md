# Configuration

> Moved from `AGENTS.md` to keep the always-on project rules under the context injection limit. `AGENTS.md` links here where this material is needed.

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
