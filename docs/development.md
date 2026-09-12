# Development Guide

> Moved from `AGENTS.md` to keep the always-on project rules under the context injection limit. `AGENTS.md` links here where this material is needed.

## Prerequisites

1. Install [mise](https://mise.jdx.dev/)
2. Install toolchains and dependencies: `mise install`
3. Initialize the project: `mise run setup`
4. Start the dev environment: `mise run dev`
5. Dev web UI: `http://localhost:18082` (server: `18080`)

## Common Commands

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

## Docker Deployment

```bash
docker compose up -d        # Start all services
# Visit http://localhost:8082
```

Production deploy services are `postgres`, `pgvector`, `migrate`, `server`, `channel`, and `web`.
Optional profile: `webhook-tunnel` (cloudflared for channels behind NAT). Desktop connects to Memoh Cloud or this hosted server instead of running its own server.
