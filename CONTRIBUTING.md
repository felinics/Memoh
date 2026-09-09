# Contributing Guide

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/) for the containerized dev environment
- [mise](https://mise.jdx.dev/) for toolchain and task management

### Install mise

```bash
# macOS / Linux
curl https://mise.run | sh
# or
brew install mise

# Windows
winget install jdx.mise
```

## Quick Start

```bash
mise install       # Install toolchains (Go, Node, pnpm, sqlc, golangci-lint)
mise run setup     # Install dependencies and run code generation
mise run dev       # Start the full containerized dev environment
```

`mise run dev` launches the development stack in Docker:

1. PostgreSQL infrastructure
2. Database migrations, run automatically on startup
3. Connect-It API and admin UI
4. Go server with the in-process AI agent and containerd workspace backend
5. Web frontend with Vite hot reload

The dev stack uses `devenv/app.dev.toml` directly and does not overwrite the repo root `config.toml`.
Default host ports are shifted away from the production compose stack: Web `18082`, API `18080`, Connect-It `18421`, Postgres `15432`.

### Connect-It

The dev Compose files pull the pinned multi-architecture image
`ghcr.io/felinics/connect-it:0.3.0`; no Connect-It source checkout is required.
The image serves the API, MCP endpoint, and admin UI from one container.
Connect-It uses the same `memoh` PostgreSQL database as Memoh, with
its independently migrated tables isolated in the `connect_it` schema. The
schema is owned, created, and migrated by the Connect-It server itself on
startup; Memoh's migrations never touch it.

`mise run dev` shares a fixed development bootstrap token between both sides:
the Connect-It container seeds it as an API token at startup
(`CONNECT_IT_BOOTSTRAP_API_TOKEN`) and the Memoh server presents it as its
bearer token. The Connectors tab is therefore available from the first
startup, with no token minting or caching involved. The admin UI remains
available at `http://localhost:18421` with the development credentials
`admin` / `admin123`. The Connect-It port binds to loopback only: the token
is public, so the deployment must not be reachable from the LAN. If the
bootstrap token was revoked through the admin UI, rotate it once with
`MEMOH_CONNECT_IT_API_TOKEN=cit_<64 hex chars> mise run dev` — the new value
is seeded as a fresh token and the revoked one stays revoked.

Override the host UI port with `MEMOH_DEV_CONNECT_IT_PORT`, the public OAuth callback
address with `MEMOH_DEV_CONNECT_IT_BASE_URL`, or point at an external Connect-It
deployment with `MEMOH_CONNECT_IT_BASE_URL` and `MEMOH_CONNECT_IT_API_TOKEN`. For
image testing, override `MEMOH_DEV_CONNECT_IT_IMAGE`.

## Daily Development

```bash
mise run dev                              # Start all services
mise run dev:selinux                      # Start all services on SELinux hosts
mise run dev:down                         # Stop the dev stack
mise run dev:logs                         # View dev logs
mise run dev:restart -- server            # Restart a specific service
mise run bridge:build                     # Rebuild the bridge binary in the dev container
```

## More Commands

| Command | Description |
| ------- | ----------- |
| `mise run setup` | Install dependencies and prepare local tooling |
| `mise run dev` | Start the containerized PostgreSQL dev environment |
| `mise run dev:down` | Stop the dev environment |
| `mise run dev:logs` | View dev logs |
| `mise run dev:restart -- server` | Restart a specific dev service |
| `mise run bridge:build` | Rebuild the workspace bridge binary in the dev container |
| `mise run db-up` | Run database migrations |
| `mise run db-down` | Roll back database migrations |
| `mise run swagger-generate` | Generate Swagger/OpenAPI documentation |
| `mise run sdk-generate` | Generate the TypeScript SDK |
| `mise run sqlc-generate` | Generate Go SQL code |
| `mise run lint` | Run Go and TypeScript linters |

## Project Layout

```
conf/       - Configuration templates (app.example.toml, app.docker.toml)
devenv/     - Dev environment (docker-compose, dev Dockerfiles, app.dev.toml, bridge-build.sh)
docker/     - Production Docker build and runtime (Dockerfiles, entrypoints)
cmd/        - Go application entry points
internal/   - Go backend core code
apps/       - Application services
  web/      - Vue 3 management UI
  desktop/  - Electron desktop shell
packages/   - Frontend monorepo packages (ui, sdk, icons, config)
db/         - Database migrations and queries
scripts/    - Utility scripts
```

## Testing

Run focused tests before opening a pull request:

```bash
go test ./internal/channel/adapters/dingtalk
go test ./internal/...
pnpm test --run
pnpm --filter @memohai/desktop typecheck
mise run lint
```

For frontend-only changes, `pnpm lint` and `pnpm test --run` are usually enough. For desktop changes, also run `pnpm --filter @memohai/desktop typecheck`.

## SQL, API, and SDK Changes

Database changes target PostgreSQL:

1. Update `db/postgres/...`.
2. Add the next PostgreSQL incremental migration pair when the schema changes.
3. Update the canonical PostgreSQL baseline migration.
4. Run `mise run sqlc-generate`.

API handler changes should update the OpenAPI spec and generated SDK:

```bash
mise run swagger-generate
mise run sdk-generate
```

Generated files under `internal/db/postgres/sqlc/` and `packages/sdk/` should be committed only when they result from the matching SQL or API source changes.

## Windows Notes

The repository supports Windows development, but many `mise` tasks use bash-style scripts. Running them from Git Bash, WSL, or a Docker-backed shell is the smoothest path. Plain PowerShell is still useful for Go package tests and file inspection, but tasks that invoke shell scripts may need a POSIX-compatible shell.

If you have local work in progress that should not be included in a pull request, stage only the intended files:

```bash
git add path/to/intended-file.go path/to/intended-test.go
git status --short
```
