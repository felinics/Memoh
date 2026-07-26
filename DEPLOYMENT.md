# Memoh Deployment Guide

## One-Click Install

```bash
curl -fsSL https://memoh.sh | sh
```

The script prompts for configuration, generates `config.toml`, and starts all services.
Run it as your normal user. The script will use `sudo docker` internally only when Docker requires it.

The one-click Docker Compose installer uses the `containerd` workspace backend. Docker and Apple workspace backends are available for manual deployments by editing `[container].backend` in `config.toml`.

## Manual Install

```bash
git clone --recurse-submodules https://github.com/memohai/Memoh.git
cd Memoh
cp conf/app.docker.toml config.toml
nano config.toml   # Change passwords and JWT secret
```

GitHub's automatic “Source code” archives omit submodule contents. Use a recursive clone or the
complete `Memoh-<version>-source.zip` / `.tar.gz` asset attached to each release. Existing setup
checkouts can continue using `git pull`; run `mise run submodule-init` once only if the post-merge
hook was never installed.

> On macOS or if your user is in the `docker` group, `sudo` is not required.

> **Important**: You must create `config.toml` before starting. `docker-compose.yml` mounts `./config.toml` into the containers — running without it will fail.

### Standard Startup

```bash
docker compose up -d
```

Access:
- Web UI: http://localhost:8082
- API: http://localhost:8080

Default credentials: `admin` / `admin123` (change in `config.toml`)

## Docker Compose Services

The base `docker-compose.yml` contains the standard services: `postgres`,
`pgvector`, `migrate`, `server`, and `web`. The AI agent runs in-process inside
`server`. PostgreSQL-backed deployments keep the main Postgres service plain and
use the separate pgvector service for memory semantic search. SQLite deployments
keep the local graph store only and do not run vector search.

### SaaS / external providers

For Mem0, OpenViking SaaS, or a separately hosted OpenViking service, no Compose profile is needed. Configure the provider directly in the Memoh admin UI with the external `base_url` and API key.

### China Mainland Mirror

Uncomment `registry = "memoh.cn"` in `config.toml` under `[container]`, then add the CN overlay:

```bash
docker compose -f docker-compose.yml -f docker/docker-compose.cn.yml up -d
```

## Prerequisites

- Docker (with Docker Compose v2)
- Git

## Configuration

`config.toml` is generated from `conf/app.docker.toml` and should live in the project root. It is mounted into all containers at startup and is **not** tracked by git.

Recommended changes for production:
- `admin.password` — Admin password
- `auth.jwt_secret` — JWT secret (generate with `openssl rand -base64 32`)
- `database.driver` — `postgres`
- `container.backend` — `containerd` for the official Docker Compose stack; use `docker` or `apple` only for matching manual deployments
- `postgres.password` — Database password (also set `POSTGRES_PASSWORD` env var)

## Common Commands

> Prefix with `sudo` on Linux if your user is not in the `docker` group.

```bash
docker compose up -d          # Start
docker compose down           # Stop
docker compose logs -f        # View logs
docker compose ps             # Status
docker compose pull && docker compose up -d  # Update images
```

## Production

1. Change all default passwords and secrets
2. Configure HTTPS (reverse proxy or `docker-compose.override.yml` with SSL)
3. Configure firewall
4. Set resource limits
5. Regular backups

## Troubleshooting

```bash
docker compose logs server    # View service logs
docker compose config         # Check configuration
docker compose build --no-cache && docker compose up -d  # Full rebuild
```

## Security Warnings

- Main service has privileged container access — only run in trusted environments
- Must change all default passwords and secrets
- Use HTTPS in production
