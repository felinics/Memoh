#!/bin/sh
set -e

GREEN=$(printf '\033[0;32m')
YELLOW=$(printf '\033[1;33m')
PURPLE=$(printf '\033[0;35m')
RED=$(printf '\033[0;31m')
NC=$(printf '\033[0m')

GITHUB_REPO="felinics/Memoh"
REPO="https://github.com/${GITHUB_REPO}.git"
DIR="Memoh"
COMPOSE_PROJECT_NAME="memoh"
SILENT=false

# Track whether the user explicitly set environment-backed options so upgrades
# can reuse prior install values by default.
if [ "${MEMOH_INSTALL_MODE+x}" = x ]; then
  INSTALL_MODE="$MEMOH_INSTALL_MODE"
else
  INSTALL_MODE="auto"
fi
if [ "${MEMOH_DATABASE_DRIVER+x}" = x ]; then
  DATABASE_DRIVER="$MEMOH_DATABASE_DRIVER"
  DATABASE_DRIVER_SET=true
else
  DATABASE_DRIVER="postgres"
  DATABASE_DRIVER_SET=false
fi
if [ "${MEMOH_CONTAINER_BACKEND+x}" = x ]; then
  CONTAINER_BACKEND="$MEMOH_CONTAINER_BACKEND"
  CONTAINER_BACKEND_SET=true
else
  CONTAINER_BACKEND="containerd"
  CONTAINER_BACKEND_SET=false
fi
if [ "${USE_CN_MIRROR+x}" = x ]; then
  USE_CN_MIRROR_SET=true
else
  USE_CN_MIRROR_SET=false
fi
if [ "${MEMOH_WEBHOOK_TUNNEL_MODE+x}" = x ]; then
  WEBHOOK_TUNNEL_MODE_SET=true
else
  WEBHOOK_TUNNEL_MODE_SET=false
fi
if [ "${MEMOH_WEBHOOK_PUBLIC_BASE_URL+x}" = x ]; then
  WEBHOOK_PUBLIC_BASE_SET=true
else
  WEBHOOK_PUBLIC_BASE_SET=false
fi
if [ "${MEMOH_INTERNAL_RPC_SHARED_SECRET+x}" = x ] && [ -n "$MEMOH_INTERNAL_RPC_SHARED_SECRET" ]; then
  INTERNAL_RPC_SHARED_SECRET_SET=true
else
  INTERNAL_RPC_SHARED_SECRET_SET=false
fi
if [ "${MEMOH_CONNECT_IT_MODE+x}" = x ] && [ -n "$MEMOH_CONNECT_IT_MODE" ]; then
  CONNECT_IT_MODE_SET=true
else
  CONNECT_IT_MODE_SET=false
fi
NETWORK_NAME="${COMPOSE_PROJECT_NAME}_memoh-network"
PROJECT_CONTAINERS="memoh-postgres memoh-pgvector memoh-migrate memoh-server memoh-channel memoh-web memoh-webhook-tunnel memoh-connect-it"
LEGACY_PROJECT_CONTAINERS="memoh-connect-it-web"
PROJECT_VOLUMES="${COMPOSE_PROJECT_NAME}_postgres_data ${COMPOSE_PROJECT_NAME}_pgvector_data ${COMPOSE_PROJECT_NAME}_containerd_data ${COMPOSE_PROJECT_NAME}_memoh_data ${COMPOSE_PROJECT_NAME}_server_cni_state ${COMPOSE_PROJECT_NAME}_openviking_data"

EXISTING_CONFIG_SOURCE=""
EXISTING_ENV_SOURCE=""
EXISTING_INSTALL_STATE=false
EXISTING_DOCKER_STATE=false
EXISTING_DOCKER_VOLUMES=""
EXISTING_DOCKER_CONTAINERS=false
EXISTING_DOCKER_NETWORK=false
EXISTING_WORKSPACE_FILES=false
EXISTING_REPO_DIR=false

# Parse flags
while [ $# -gt 0 ]; do
  case "$1" in
    -y|--yes) SILENT=true ;;
    --version)
      shift
      MEMOH_VERSION="$1"
      ;;
    --version=*)
      MEMOH_VERSION="${1#--version=}"
      ;;
    --install-mode)
      shift
      INSTALL_MODE="$1"
      ;;
    --install-mode=*)
      INSTALL_MODE="${1#--install-mode=}"
      ;;
    --database-driver)
      shift
      DATABASE_DRIVER="$1"
      DATABASE_DRIVER_SET=true
      ;;
    --database-driver=*)
      DATABASE_DRIVER="${1#--database-driver=}"
      DATABASE_DRIVER_SET=true
      ;;
    --container-backend|--workspace-backend)
      shift
      CONTAINER_BACKEND="$1"
      CONTAINER_BACKEND_SET=true
      ;;
    --container-backend=*|--workspace-backend=*)
      CONTAINER_BACKEND="${1#*=}"
      CONTAINER_BACKEND_SET=true
      ;;
  esac
  shift
done

# Auto-silent if no TTY available
if [ "$SILENT" = false ] && ! [ -e /dev/tty ]; then
  SILENT=true
fi

echo "${PURPLE}Memoh One-Click Install${NC}"

if [ "$(id -u 2>/dev/null || printf '1')" = "0" ] && [ "${MEMOH_ALLOW_ROOT_INSTALL:-false}" != "true" ]; then
  echo "${RED}Error: Do not run this installer as root.${NC}"
  echo "Run it as your normal user instead:"
  echo "  curl -fsSL https://memoh.sh | sh"
  echo ""
  echo "The installer will use sudo for Docker commands only when Docker requires it."
  echo "To override this guard, set MEMOH_ALLOW_ROOT_INSTALL=true."
  exit 1
fi

read_env_file_value() {
  file="$1"
  key="$2"
  if [ ! -f "$file" ]; then
    return 1
  fi
  value=$(grep "^${key}=" "$file" 2>/dev/null | tail -n 1 | cut -d '=' -f 2-)
  if [ -z "$value" ]; then
    return 1
  fi
  case "$value" in
    \'*\')
      value=${value#\'}
      value=${value%\'}
      value=$(printf '%s' "$value" | sed "s/\\\\'/'/g")
      ;;
  esac
  printf '%s' "$value"
}

read_toml_value() {
  file="$1"
  section="$2"
  key="$3"
  if [ ! -f "$file" ]; then
    return 1
  fi
  value=$(awk -v target_section="[$section]" -v target_key="$key" '
    /^\[[^]]+\]/ {
      in_section = ($0 == target_section)
      next
    }
    in_section && $0 ~ "^[[:space:]]*" target_key "[[:space:]]*=" {
      value = substr($0, index($0, "=") + 1)
      sub(/^[[:space:]]*/, "", value)
      sub(/[[:space:]]*$/, "", value)
      if (value ~ /^".*"$/) {
        sub(/^"/, "", value)
        sub(/"$/, "", value)
      }
      print value
      exit
    }
  ' "$file")
  if [ -z "$value" ]; then
    return 1
  fi
  printf '%s' "$value" | sed 's/\\"/"/g; s/\\\\/\\/g'
}

normalize_database_driver() {
  driver=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
  case "$driver" in
    postgres|postgresql) printf '%s' "postgres" ;;
    *) return 1 ;;
  esac
}

normalize_database_driver_or_exit() {
  normalized_database_driver=$(normalize_database_driver "$DATABASE_DRIVER" || true)
  if [ -z "$normalized_database_driver" ]; then
    echo "${RED}Error: unsupported database driver '${DATABASE_DRIVER}'. Memoh now supports postgres only.${NC}"
    exit 1
  fi
  DATABASE_DRIVER="$normalized_database_driver"
}

normalize_container_backend() {
  backend=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
  case "$backend" in
    containerd) printf '%s' "containerd" ;;
    docker) printf '%s' "docker" ;;
    apple) printf '%s' "apple" ;;
    *) return 1 ;;
  esac
}

normalize_container_backend_or_exit() {
  normalized_container_backend=$(normalize_container_backend "$CONTAINER_BACKEND" || true)
  if [ -z "$normalized_container_backend" ]; then
    echo "${RED}Error: unsupported workspace backend '${CONTAINER_BACKEND}'. Use containerd, docker, or apple.${NC}"
    exit 1
  fi
  CONTAINER_BACKEND="$normalized_container_backend"
}

enforce_compose_container_backend() {
  if [ "$CONTAINER_BACKEND" = "containerd" ]; then
    return
  fi
  if [ "$INSTALL_MODE" = "upgrade" ] && [ "$CONTAINER_BACKEND_SET" = false ]; then
    echo "${YELLOW}ℹ Existing config uses workspace backend '${CONTAINER_BACKEND}'. The one-click Docker Compose stack is designed for containerd; reusing your config unchanged.${NC}"
    return
  fi
  echo "${RED}Error: one-click Docker Compose installs support workspace backend 'containerd' only.${NC}"
  echo "The server image starts an embedded containerd and mounts the required runtime paths."
  echo "For docker or apple backends, use a manual deployment and edit [container].backend in config.toml."
  exit 1
}

escape_toml_string() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

set_toml_string_value() {
  file="$1"
  section="$2"
  key="$3"
  value=$(escape_toml_string "$4")
  tmp="${file}.tmp.$$"
  if TOML_VALUE="$value" awk -v target_section="[$section]" -v target_key="$key" '
    BEGIN {
      target_value = ENVIRON["TOML_VALUE"]
    }
    /^\[[^]]+\]/ {
      in_section = ($0 == target_section)
    }
    in_section && $0 ~ "^[[:space:]]*" target_key "[[:space:]]*=" {
      indent = $0
      sub(/[^[:space:]].*/, "", indent)
      print indent target_key " = \"" target_value "\""
      next
    }
    { print }
  ' "$file" > "$tmp"; then
    mv "$tmp" "$file"
  else
    rm -f "$tmp"
    return 1
  fi
}

set_toml_bool_value() {
  file="$1"
  section="$2"
  key="$3"
  value="$4"
  tmp="${file}.tmp.$$"
  if TOML_VALUE="$value" awk -v target_section="[$section]" -v target_key="$key" '
    BEGIN {
      target_value = ENVIRON["TOML_VALUE"]
    }
    /^\[[^]]+\]/ {
      in_section = ($0 == target_section)
    }
    in_section && $0 ~ "^[[:space:]]*" target_key "[[:space:]]*=" {
      indent = $0
      sub(/[^[:space:]].*/, "", indent)
      print indent target_key " = " target_value
      next
    }
    { print }
  ' "$file" > "$tmp"; then
    mv "$tmp" "$file"
  else
    rm -f "$tmp"
    return 1
  fi
}

write_env_value() {
  key="$1"
  value=$(printf '%s' "$2" | sed "s/'/\\\\'/g")
  printf "%s='%s'\n" "$key" "$value" >> .env
}

fetch_latest_version() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" 2>/dev/null
  elif command -v wget >/dev/null 2>&1; then
    wget -qO- "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" 2>/dev/null
  else
    echo "${RED}Error: curl or wget is required${NC}" >&2
    exit 1
  fi
}

detect_existing_installation() {
  EXISTING_CONFIG_SOURCE=""
  EXISTING_ENV_SOURCE=""
  EXISTING_INSTALL_STATE=false
  EXISTING_DOCKER_STATE=false
  EXISTING_DOCKER_VOLUMES=""
  EXISTING_DOCKER_CONTAINERS=false
  EXISTING_DOCKER_NETWORK=false
  EXISTING_WORKSPACE_FILES=false
  EXISTING_REPO_DIR=false

  if [ -d "$WORKSPACE/$DIR" ]; then
    EXISTING_REPO_DIR=true
    EXISTING_INSTALL_STATE=true
  fi

  if [ -f "$WORKSPACE/config.toml" ]; then
    EXISTING_CONFIG_SOURCE="$WORKSPACE/config.toml"
    EXISTING_WORKSPACE_FILES=true
    EXISTING_INSTALL_STATE=true
    if [ -f "$WORKSPACE/.env" ]; then
      EXISTING_ENV_SOURCE="$WORKSPACE/.env"
    fi
  elif [ -f "$WORKSPACE/$DIR/config.toml" ]; then
    EXISTING_CONFIG_SOURCE="$WORKSPACE/$DIR/config.toml"
    EXISTING_INSTALL_STATE=true
    if [ -f "$WORKSPACE/$DIR/.env" ]; then
      EXISTING_ENV_SOURCE="$WORKSPACE/$DIR/.env"
    fi
  fi

  if [ -f "$WORKSPACE/docker-compose.yml" ] || [ -f "$WORKSPACE/.env" ]; then
    EXISTING_WORKSPACE_FILES=true
    EXISTING_INSTALL_STATE=true
    if [ -z "$EXISTING_ENV_SOURCE" ] && [ -f "$WORKSPACE/.env" ]; then
      EXISTING_ENV_SOURCE="$WORKSPACE/.env"
    fi
  fi

  for volume in $PROJECT_VOLUMES; do
    if $DOCKER volume inspect "$volume" >/dev/null 2>&1; then
      EXISTING_DOCKER_STATE=true
      EXISTING_INSTALL_STATE=true
      EXISTING_DOCKER_VOLUMES="${EXISTING_DOCKER_VOLUMES} ${volume}"
    fi
  done

  for container in $PROJECT_CONTAINERS $LEGACY_PROJECT_CONTAINERS; do
    if $DOCKER container inspect "$container" >/dev/null 2>&1; then
      EXISTING_DOCKER_STATE=true
      EXISTING_DOCKER_CONTAINERS=true
      EXISTING_INSTALL_STATE=true
      break
    fi
  done

  if $DOCKER network inspect "$NETWORK_NAME" >/dev/null 2>&1; then
    EXISTING_DOCKER_STATE=true
    EXISTING_DOCKER_NETWORK=true
    EXISTING_INSTALL_STATE=true
  fi
}

load_existing_settings() {
  if [ -n "$EXISTING_CONFIG_SOURCE" ]; then
    value=$(read_toml_value "$EXISTING_CONFIG_SOURCE" "admin" "username" || true)
    [ -n "$value" ] && ADMIN_USER="$value"

    value=$(read_toml_value "$EXISTING_CONFIG_SOURCE" "admin" "password" || true)
    [ -n "$value" ] && ADMIN_PASS="$value"

    value=$(read_toml_value "$EXISTING_CONFIG_SOURCE" "auth" "jwt_secret" || true)
    [ -n "$value" ] && JWT_SECRET="$value"

    if [ "$INTERNAL_RPC_SHARED_SECRET_SET" = false ]; then
      value=$(read_toml_value "$EXISTING_CONFIG_SOURCE" "internal_rpc" "shared_secret" || true)
      [ -n "$value" ] && MEMOH_INTERNAL_RPC_SHARED_SECRET="$value"
    fi

    if [ "$DATABASE_DRIVER_SET" = false ]; then
      value=$(read_toml_value "$EXISTING_CONFIG_SOURCE" "database" "driver" || true)
      [ -n "$value" ] && DATABASE_DRIVER="$value"
    fi

    if [ "$CONTAINER_BACKEND_SET" = false ]; then
      value=$(read_toml_value "$EXISTING_CONFIG_SOURCE" "container" "backend" || true)
      [ -n "$value" ] && CONTAINER_BACKEND="$value"
    fi

    value=$(read_toml_value "$EXISTING_CONFIG_SOURCE" "postgres" "password" || true)
    [ -n "$value" ] && PG_PASS="$value"

    if [ "$USE_CN_MIRROR_SET" = false ]; then
      value=$(read_toml_value "$EXISTING_CONFIG_SOURCE" "container" "registry" || true)
      if [ -z "$value" ]; then
        value=$(read_toml_value "$EXISTING_CONFIG_SOURCE" "workspace" "registry" || true)
      fi
      if [ "$value" = "memoh.cn" ]; then
        USE_CN_MIRROR=true
      fi
    fi
  fi

  if [ -n "$EXISTING_ENV_SOURCE" ]; then
    value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "POSTGRES_PASSWORD" || true)
    [ -n "$value" ] && PG_PASS="$value"

    if [ "$INTERNAL_RPC_SHARED_SECRET_SET" = false ]; then
      value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_INTERNAL_RPC_SHARED_SECRET" || true)
      [ -n "$value" ] && MEMOH_INTERNAL_RPC_SHARED_SECRET="$value"
    fi

    value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_DATA_DIR" || true)
    [ -n "$value" ] && MEMOH_DATA_DIR="$value"

    if [ "$DATABASE_DRIVER_SET" = false ]; then
      value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_DATABASE_DRIVER" || true)
      [ -n "$value" ] && DATABASE_DRIVER="$value"
    fi

    if [ "$CONTAINER_BACKEND_SET" = false ]; then
      value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_CONTAINER_BACKEND" || true)
      [ -n "$value" ] && CONTAINER_BACKEND="$value"
    fi

    if [ "$WEBHOOK_PUBLIC_BASE_SET" = false ]; then
      value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_WEBHOOK_PUBLIC_BASE_URL" || true)
      [ -n "$value" ] && MEMOH_WEBHOOK_PUBLIC_BASE_URL="$value"
    fi

    if [ "$WEBHOOK_TUNNEL_MODE_SET" = false ]; then
      value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_WEBHOOK_TUNNEL_MODE" || true)
      [ -n "$value" ] && MEMOH_WEBHOOK_TUNNEL_MODE="$value"
    fi

    value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_WEBHOOK_TUNNEL_LISTEN_ADDR" || true)
    [ -n "$value" ] && MEMOH_WEBHOOK_TUNNEL_LISTEN_ADDR="${MEMOH_WEBHOOK_TUNNEL_LISTEN_ADDR:-$value}"

    value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_WEBHOOK_TUNNEL_METRICS_URL" || true)
    [ -n "$value" ] && MEMOH_WEBHOOK_TUNNEL_METRICS_URL="${MEMOH_WEBHOOK_TUNNEL_METRICS_URL:-$value}"

    if [ "$CONNECT_IT_MODE_SET" = false ]; then
      value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_CONNECT_IT_MODE" || true)
      [ -n "$value" ] && MEMOH_CONNECT_IT_MODE="$value"
    fi
    value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_CONNECT_IT_ADMIN_PASSWORD" || true)
    [ -n "$value" ] && MEMOH_CONNECT_IT_ADMIN_PASSWORD="${MEMOH_CONNECT_IT_ADMIN_PASSWORD:-$value}"
    value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_CONNECT_IT_SECRET_KEY" || true)
    [ -n "$value" ] && MEMOH_CONNECT_IT_SECRET_KEY="${MEMOH_CONNECT_IT_SECRET_KEY:-$value}"
    value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_CONNECT_IT_COOKIE_SECRET" || true)
    [ -n "$value" ] && MEMOH_CONNECT_IT_COOKIE_SECRET="${MEMOH_CONNECT_IT_COOKIE_SECRET:-$value}"
    value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_CONNECT_IT_API_TOKEN" || true)
    [ -n "$value" ] && MEMOH_CONNECT_IT_API_TOKEN="${MEMOH_CONNECT_IT_API_TOKEN:-$value}"
    value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_CONNECT_IT_PUBLIC_BASE_URL" || true)
    [ -n "$value" ] && MEMOH_CONNECT_IT_PUBLIC_BASE_URL="${MEMOH_CONNECT_IT_PUBLIC_BASE_URL:-$value}"
    value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_CONNECT_IT_PORT" || true)
    [ -n "$value" ] && MEMOH_CONNECT_IT_PORT="${MEMOH_CONNECT_IT_PORT:-$value}"
    value=$(read_env_file_value "$EXISTING_ENV_SOURCE" "MEMOH_CONNECT_IT_IMAGE" || true)
    [ -n "$value" ] && MEMOH_CONNECT_IT_IMAGE="${MEMOH_CONNECT_IT_IMAGE:-$value}"
  fi
}

prompt_install_mode() {
  if [ "$SILENT" = true ]; then
    if [ "$INSTALL_MODE" = "auto" ]; then
      if [ -n "$EXISTING_CONFIG_SOURCE" ]; then
        INSTALL_MODE="upgrade"
        echo "${YELLOW}ℹ Existing Memoh installation detected. Reusing existing configuration in silent mode.${NC}"
      elif [ "$EXISTING_DOCKER_STATE" = true ]; then
        echo "${RED}Error: Existing Memoh Docker state was detected but no reusable config.toml was found.${NC}"
        echo "Run again with MEMOH_INSTALL_MODE=reinstall to wipe Docker data, or restore the previous config.toml."
        exit 1
      else
        INSTALL_MODE="fresh"
        if [ "$EXISTING_INSTALL_STATE" = true ]; then
          echo "${YELLOW}ℹ Existing Memoh files were detected, but no Docker state or reusable config.toml was found. Proceeding with a fresh install in silent mode.${NC}"
        fi
      fi
    fi
    return
  fi

  if [ "$INSTALL_MODE" != "auto" ]; then
    return
  fi

  if [ "$EXISTING_INSTALL_STATE" = false ]; then
    INSTALL_MODE="fresh"
    return
  fi

  echo "${YELLOW}Detected existing Memoh installation state:${NC}" > /dev/tty
  if [ -n "$EXISTING_CONFIG_SOURCE" ]; then
    echo "  - Config: ${EXISTING_CONFIG_SOURCE}" > /dev/tty
  fi
  if [ -n "$EXISTING_ENV_SOURCE" ]; then
    echo "  - Env: ${EXISTING_ENV_SOURCE}" > /dev/tty
  fi
  if [ "$EXISTING_REPO_DIR" = true ]; then
    echo "  - Repository checkout: ${WORKSPACE}/${DIR}" > /dev/tty
  fi
  if [ -n "$EXISTING_DOCKER_VOLUMES" ]; then
    echo "  - Docker volumes:${EXISTING_DOCKER_VOLUMES}" > /dev/tty
  fi
  if [ "$EXISTING_DOCKER_CONTAINERS" = true ]; then
    echo "  - Existing Memoh containers" > /dev/tty
  fi
  if [ "$EXISTING_DOCKER_NETWORK" = true ]; then
    echo "  - Docker network: ${NETWORK_NAME}" > /dev/tty
  fi
  echo "" > /dev/tty

  if [ -n "$EXISTING_CONFIG_SOURCE" ]; then
    echo "Choose install mode:" > /dev/tty
    echo "  1) Upgrade existing installation (recommended, reuses config and DB password)" > /dev/tty
    echo "  2) Reinstall from scratch (removes Memoh Docker data)" > /dev/tty
    echo "  3) Abort" > /dev/tty
    printf "  Install mode [1]: " > /dev/tty
    read -r input < /dev/tty || true
    case "$input" in
      2) INSTALL_MODE="reinstall" ;;
      3) INSTALL_MODE="abort" ;;
      *) INSTALL_MODE="upgrade" ;;
    esac
  elif [ "$EXISTING_DOCKER_STATE" = true ]; then
    echo "No reusable config.toml was found for a safe upgrade." > /dev/tty
    echo "Choose install mode:" > /dev/tty
    echo "  1) Reinstall from scratch (removes Memoh Docker data)" > /dev/tty
    echo "  2) Abort" > /dev/tty
    printf "  Install mode [2]: " > /dev/tty
    read -r input < /dev/tty || true
    case "$input" in
      1) INSTALL_MODE="reinstall" ;;
      *) INSTALL_MODE="abort" ;;
    esac
  else
    echo "No reusable config.toml or Docker state was found." > /dev/tty
    echo "Choose install mode:" > /dev/tty
    echo "  1) Continue fresh install (recommended)" > /dev/tty
    echo "  2) Abort" > /dev/tty
    printf "  Install mode [1]: " > /dev/tty
    read -r input < /dev/tty || true
    case "$input" in
      2) INSTALL_MODE="abort" ;;
      *) INSTALL_MODE="fresh" ;;
    esac
  fi
}

cleanup_existing_installation() {
  echo "${YELLOW}Removing existing Memoh Docker containers, volumes, and network...${NC}"
  for container in $PROJECT_CONTAINERS $LEGACY_PROJECT_CONTAINERS; do
    $DOCKER rm -f "$container" >/dev/null 2>&1 || true
  done
  for volume in $PROJECT_VOLUMES; do
    $DOCKER volume rm -f "$volume" >/dev/null 2>&1 || true
  done
  $DOCKER network rm "$NETWORK_NAME" >/dev/null 2>&1 || true
}

show_failure_logs() {
  echo ""
  echo "${RED}Startup failed. Recent database, migration, server, and channel logs:${NC}"
  log_services="postgres migrate server channel"
  if [ "${CONNECT_IT_MODE:-}" = "embedded" ]; then
    log_services="$log_services connect-it"
  fi
  $DOCKER compose $COMPOSE_FILES $COMPOSE_PROFILES logs --no-color --tail=200 $log_services || true
}

# Check Docker and determine if sudo is needed
DOCKER="docker"
if ! command -v docker >/dev/null 2>&1; then
    echo "${RED}Error: Docker is not installed${NC}"
    echo "Install Docker first: https://docs.docker.com/get-docker/"
    exit 1
fi
if ! docker info >/dev/null 2>&1; then
    if sudo docker info >/dev/null 2>&1; then
        DOCKER="sudo docker"
    else
        echo "${RED}Error: Cannot connect to Docker daemon${NC}"
        echo "Try: sudo usermod -aG docker \$USER && newgrp docker"
        exit 1
    fi
fi
if ! $DOCKER compose version >/dev/null 2>&1; then
    echo "${RED}Error: Docker Compose v2 is required${NC}"
    echo "Install: https://docs.docker.com/compose/install/"
    exit 1
fi
echo "${GREEN}✓ Docker and Docker Compose detected${NC}"

# Resolve version: use MEMOH_VERSION env if set, otherwise fetch latest release
if [ -n "$MEMOH_VERSION" ]; then
    echo "${GREEN}✓ Using specified version: ${MEMOH_VERSION}${NC}"
else
    MEMOH_VERSION=$(fetch_latest_version | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    if [ -n "$MEMOH_VERSION" ]; then
        echo "${GREEN}✓ Latest release: ${MEMOH_VERSION}${NC}"
    else
        echo "${YELLOW}Warning: Failed to fetch latest release tag, falling back to main branch${NC}"
    fi
fi

# Docker image tag: strip leading "v", fall back to "latest" only when version is unknown
if [ -n "$MEMOH_VERSION" ]; then
    MEMOH_DOCKER_VERSION=$(echo "$MEMOH_VERSION" | sed 's/^v//')
else
    MEMOH_DOCKER_VERSION="latest"
fi
echo "${GREEN}✓ Docker image version: ${MEMOH_DOCKER_VERSION}${NC}"

# Generate random JWT secret
gen_secret() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 32
  else
    head -c 32 /dev/urandom | base64 | tr -d '\n'
  fi
}

gen_hex() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
  else
    od -An -tx1 -N32 /dev/urandom | tr -d ' \n'
  fi
}

gen_password() {
  while :; do
    if command -v openssl >/dev/null 2>&1; then
      password=$(openssl rand -base64 32 | LC_ALL=C tr -dc 'A-Za-z0-9' | head -c 16)
    else
      password=$(LC_ALL=C tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 16)
    fi
    if [ "${#password}" -ne 16 ]; then
      continue
    fi
    case "$password" in
      *[ABCDEFGHIJKLMNOPQRSTUVWXYZ]*)
        case "$password" in
          *[abcdefghijklmnopqrstuvwxyz]*)
            case "$password" in
              *[0123456789]*) printf '%s' "$password"; return ;;
            esac
            ;;
        esac
        ;;
    esac
  done
}

# Configuration defaults (expand ~ for paths)
WORKSPACE_DEFAULT="${HOME:-/tmp}/memoh"
MEMOH_DATA_DIR_DEFAULT="${HOME:-/tmp}/memoh/data"
ADMIN_USER="admin"
ADMIN_PASS="$(gen_password)"
JWT_SECRET="$(gen_secret)"
MEMOH_INTERNAL_RPC_SHARED_SECRET="${MEMOH_INTERNAL_RPC_SHARED_SECRET:-$(gen_secret)}"
MEMOH_CONNECT_IT_MODE="${MEMOH_CONNECT_IT_MODE:-}"
MEMOH_CONNECT_IT_ADMIN_PASSWORD="${MEMOH_CONNECT_IT_ADMIN_PASSWORD:-}"
MEMOH_CONNECT_IT_SECRET_KEY="${MEMOH_CONNECT_IT_SECRET_KEY:-}"
MEMOH_CONNECT_IT_COOKIE_SECRET="${MEMOH_CONNECT_IT_COOKIE_SECRET:-}"
MEMOH_CONNECT_IT_API_TOKEN="${MEMOH_CONNECT_IT_API_TOKEN:-}"
MEMOH_CONNECT_IT_PUBLIC_BASE_URL="${MEMOH_CONNECT_IT_PUBLIC_BASE_URL:-}"
MEMOH_CONNECT_IT_PORT="${MEMOH_CONNECT_IT_PORT:-8421}"
MEMOH_CONNECT_IT_IMAGE="${MEMOH_CONNECT_IT_IMAGE:-}"
PG_PASS="memoh123"
WORKSPACE="$WORKSPACE_DEFAULT"
MEMOH_DATA_DIR="$MEMOH_DATA_DIR_DEFAULT"
USE_CN_MIRROR="${USE_CN_MIRROR:-false}"

if [ "$SILENT" = false ]; then
  echo "Configure Memoh (press Enter to use defaults):" > /dev/tty
  echo "" > /dev/tty

  printf "  Workspace (install and clone here) [%s]: " "~/memoh" > /dev/tty
  read -r input < /dev/tty || true
  if [ -n "$input" ]; then
    case "$input" in
      "~") WORKSPACE="${HOME:-/tmp}" ;;
      "~"/*) WORKSPACE="${HOME:-/tmp}${input#\~}" ;;
      *) WORKSPACE="$input" ;;
    esac
  fi
fi

mkdir -p "$WORKSPACE"
WORKSPACE=$(cd "$WORKSPACE" && pwd)

detect_existing_installation
load_existing_settings
# Fail fast only for an explicitly requested driver (flag or env). A driver
# inherited from an old config is judged after the install mode is known,
# so a legacy sqlite install can still choose reinstall.
if [ "$DATABASE_DRIVER_SET" = true ]; then
  normalize_database_driver_or_exit
fi
normalize_container_backend_or_exit
prompt_install_mode

case "$INSTALL_MODE" in
  auto) INSTALL_MODE="fresh" ;;
  fresh|upgrade|reinstall) ;;
  abort)
    echo "Installation aborted."
    exit 0
    ;;
  *)
    echo "${RED}Error: Unknown install mode '${INSTALL_MODE}'. Use fresh, upgrade, reinstall, or auto.${NC}"
    exit 1
    ;;
esac

# A driver inherited from an old config is validated only once the install
# mode is known: upgrade must keep the old database, so an unsupported
# legacy driver (e.g. sqlite) is fatal there, while fresh and reinstall
# discard the old state and fall back to PostgreSQL.
if [ "$DATABASE_DRIVER_SET" = false ] && ! normalize_database_driver "$DATABASE_DRIVER" >/dev/null 2>&1; then
  if [ "$INSTALL_MODE" = "upgrade" ]; then
    echo "${RED}Error: existing installation uses unsupported database driver '${DATABASE_DRIVER}'. Memoh now supports postgres only.${NC}"
    echo "Run again with MEMOH_INSTALL_MODE=reinstall to wipe the old state and start on PostgreSQL."
    exit 1
  fi
  echo "${YELLOW}ℹ Existing config uses unsupported database driver '${DATABASE_DRIVER}'; ${INSTALL_MODE} install will use PostgreSQL.${NC}"
  DATABASE_DRIVER="postgres"
fi
normalize_database_driver_or_exit

if [ "$INSTALL_MODE" = "upgrade" ] && [ -z "$EXISTING_CONFIG_SOURCE" ]; then
  echo "${RED}Error: Upgrade mode requires an existing config.toml to reuse.${NC}"
  exit 1
fi

if [ "$INSTALL_MODE" = "fresh" ] && [ "$EXISTING_DOCKER_STATE" = true ]; then
  echo "${RED}Error: Existing Memoh Docker state was detected. Use upgrade or reinstall instead of fresh.${NC}"
  exit 1
fi
enforce_compose_container_backend

if [ "$SILENT" = false ] && [ "$INSTALL_MODE" != "upgrade" ]; then
  printf "  Data directory (reserved for future bind-mount support) [%s]: " "$MEMOH_DATA_DIR" > /dev/tty
  read -r input < /dev/tty || true
  if [ -n "$input" ]; then
    case "$input" in
      "~") MEMOH_DATA_DIR="${HOME:-/tmp}" ;;
      "~"/*) MEMOH_DATA_DIR="${HOME:-/tmp}${input#\~}" ;;
      *) MEMOH_DATA_DIR="$input" ;;
    esac
  fi

  printf "  Admin username [%s]: " "$ADMIN_USER" > /dev/tty
  read -r input < /dev/tty || true
  [ -n "$input" ] && ADMIN_USER="$input"

  printf "  Admin password [%s]: " "$ADMIN_PASS" > /dev/tty
  read -r input < /dev/tty || true
  [ -n "$input" ] && ADMIN_PASS="$input"

  printf "  JWT secret [current/default value retained]: " > /dev/tty
  read -r input < /dev/tty || true
  [ -n "$input" ] && JWT_SECRET="$input"

  echo "" > /dev/tty
  echo "  Database backend: PostgreSQL" > /dev/tty
  DATABASE_DRIVER="postgres"
  normalize_database_driver_or_exit

  printf "  Postgres password [%s]: " "$PG_PASS" > /dev/tty
  read -r input < /dev/tty || true
  [ -n "$input" ] && PG_PASS="$input"

  echo "  Workspace backend: containerd (Docker Compose default; starts an embedded containerd inside memoh-server)" > /dev/tty
  echo "  Other backends such as docker and apple are configured manually in config.toml." > /dev/tty

  echo "" > /dev/tty
elif [ "$INSTALL_MODE" = "upgrade" ]; then
  echo "${GREEN}✓ Upgrade mode: reusing existing configuration and database credentials${NC}"
fi
normalize_database_driver_or_exit
normalize_container_backend_or_exit
enforce_compose_container_backend

# Enter workspace (all operations run here)
cd "$WORKSPACE"

# Clone or update
CLONED_FRESH=false
if [ -d "$DIR" ]; then
    echo "Updating existing installation in $WORKSPACE..."
    cd "$DIR"
    if [ -n "$MEMOH_VERSION" ]; then
        git fetch --depth 1 origin tag "$MEMOH_VERSION"
        git checkout "$MEMOH_VERSION"
    else
        git fetch --depth 1 origin main
        git checkout main 2>/dev/null || git checkout -b main --track origin/main
        git reset --hard origin/main
    fi
else
    echo "Cloning Memoh into $WORKSPACE..."
    if [ -n "$MEMOH_VERSION" ]; then
        git clone --depth 1 --recurse-submodules --shallow-submodules --branch "$MEMOH_VERSION" "$REPO" "$DIR"
    else
        git clone --depth 1 --recurse-submodules --shallow-submodules "$REPO" "$DIR"
    fi
    cd "$DIR"
    CLONED_FRESH=true
fi

echo "Updating git submodules..."
git submodule sync --recursive
git submodule update --init --recursive --depth 1

if [ -f .gitmodules ] && [ ! -f packages/ui/package.json ]; then
  echo "${RED}Error: packages/ui submodule is not initialized.${NC}"
  echo "Run: git submodule update --init --recursive"
  exit 1
fi

COMPOSE_FILE_NAME="docker-compose.yml"
CN_COMPOSE_FILE_NAME="docker/docker-compose.cn.yml"
if [ ! -f "$COMPOSE_FILE_NAME" ]; then
  echo "${RED}Error: ${COMPOSE_FILE_NAME} is missing in ${MEMOH_VERSION:-the selected checkout}.${NC}"
  echo "Use a newer Memoh version."
  exit 1
fi
if [ "$USE_CN_MIRROR" = true ] && [ ! -f "$CN_COMPOSE_FILE_NAME" ]; then
  echo "${RED}Error: ${CN_COMPOSE_FILE_NAME} is missing in ${MEMOH_VERSION:-the selected checkout}.${NC}"
  echo "Use a newer Memoh version or disable USE_CN_MIRROR."
  exit 1
fi

# Pin Docker image versions in the selected compose file.
if [ "$MEMOH_DOCKER_VERSION" != "latest" ]; then
    sed -i.bak "s|memohai/server:latest|memohai/server:${MEMOH_DOCKER_VERSION}|g" "$COMPOSE_FILE_NAME"
    sed -i.bak "s|memohai/agent:latest|memohai/agent:${MEMOH_DOCKER_VERSION}|g" "$COMPOSE_FILE_NAME"
    sed -i.bak "s|memohai/web:latest|memohai/web:${MEMOH_DOCKER_VERSION}|g" "$COMPOSE_FILE_NAME"
    rm -f "${COMPOSE_FILE_NAME}.bak"
    if [ "$USE_CN_MIRROR" = true ]; then
      sed -i.bak "s|memoh.cn/memohai/server:latest|memoh.cn/memohai/server:${MEMOH_DOCKER_VERSION}|g" "$CN_COMPOSE_FILE_NAME"
      sed -i.bak "s|memoh.cn/memohai/web:latest|memoh.cn/memohai/web:${MEMOH_DOCKER_VERSION}|g" "$CN_COMPOSE_FILE_NAME"
      rm -f "${CN_COMPOSE_FILE_NAME}.bak"
    fi
    echo "${GREEN}✓ Docker images pinned to ${MEMOH_DOCKER_VERSION}${NC}"
fi

if [ "$INSTALL_MODE" = "upgrade" ]; then
  if [ "$EXISTING_CONFIG_SOURCE" != "$PWD/config.toml" ]; then
    cp "$EXISTING_CONFIG_SOURCE" ./config.toml
  fi
else
  cp conf/app.docker.toml config.toml
  set_toml_string_value config.toml "admin" "username" "$ADMIN_USER"
  set_toml_string_value config.toml "admin" "password" "$ADMIN_PASS"
  set_toml_string_value config.toml "auth" "jwt_secret" "$JWT_SECRET"
  set_toml_string_value config.toml "database" "driver" "$DATABASE_DRIVER"
  set_toml_string_value config.toml "container" "backend" "$CONTAINER_BACKEND"
  set_toml_string_value config.toml "postgres" "password" "$PG_PASS"
  set_toml_string_value config.toml "pgvector" "password" "$PG_PASS"
  if [ "$DATABASE_DRIVER" = "sqlite" ]; then
    set_toml_bool_value config.toml "pgvector" "enabled" "false"
  fi
  if [ "$USE_CN_MIRROR" = true ]; then
    sed -i.bak 's|# registry = "memoh.cn"|registry = "memoh.cn"|' config.toml
  fi
  rm -f config.toml.bak
fi

INSTALL_DIR="$(pwd)"
mkdir -p "$MEMOH_DATA_DIR"
MEMOH_DATA_DIR=$(cd "$MEMOH_DATA_DIR" && pwd)
export MEMOH_CONFIG=./config.toml
export MEMOH_DATA_DIR
export POSTGRES_PASSWORD="${PG_PASS}"
export MEMOH_INTERNAL_RPC_SHARED_SECRET

COMPOSE_FILES="-f ${COMPOSE_FILE_NAME}"
COMPOSE_PROFILES=""
if [ "$USE_CN_MIRROR" = true ]; then
  COMPOSE_FILES="$COMPOSE_FILES -f ${CN_COMPOSE_FILE_NAME}"
  echo "${GREEN}✓ Using China mainland mirror (memoh.cn)${NC}"
fi
WEBHOOK_TUNNEL_MODE=$(printf '%s' "${MEMOH_WEBHOOK_TUNNEL_MODE:-}" | tr '[:upper:]' '[:lower:]')
case "$WEBHOOK_TUNNEL_MODE" in
  ""|disabled)
    WEBHOOK_TUNNEL_MODE="disabled"
    ;;
  external)
    COMPOSE_PROFILES="$COMPOSE_PROFILES --profile webhook-tunnel"
    echo "${GREEN}✓ Webhook tunnel sidecar enabled${NC}"
    if [ "$USE_CN_MIRROR" = true ]; then
      echo "${YELLOW}ℹ Webhook tunnel sidecar uses cloudflare/cloudflared from Docker Hub; memoh.cn mirror does not cover this image${NC}"
    fi
    ;;
  managed)
    WEBHOOK_TUNNEL_MODE="external"
    COMPOSE_PROFILES="$COMPOSE_PROFILES --profile webhook-tunnel"
    echo "${YELLOW}ℹ Docker install uses the Cloudflare sidecar for webhook tunnels; using external mode${NC}"
    if [ "$USE_CN_MIRROR" = true ]; then
      echo "${YELLOW}ℹ Webhook tunnel sidecar uses cloudflare/cloudflared from Docker Hub; memoh.cn mirror does not cover this image${NC}"
    fi
    ;;
  *)
    echo "${RED}Error: unsupported MEMOH_WEBHOOK_TUNNEL_MODE '${MEMOH_WEBHOOK_TUNNEL_MODE}'. Use disabled, external, or managed.${NC}"
    exit 1
    ;;
esac
if [ "$WEBHOOK_TUNNEL_MODE" = "external" ]; then
  MEMOH_WEBHOOK_TUNNEL_LISTEN_ADDR=":18734"
  MEMOH_WEBHOOK_TUNNEL_METRICS_URL="http://webhook-tunnel:18735"
fi
export MEMOH_WEBHOOK_TUNNEL_MODE="$WEBHOOK_TUNNEL_MODE"
export MEMOH_WEBHOOK_TUNNEL_LISTEN_ADDR="${MEMOH_WEBHOOK_TUNNEL_LISTEN_ADDR:-:18734}"
export MEMOH_WEBHOOK_TUNNEL_METRICS_URL="${MEMOH_WEBHOOK_TUNNEL_METRICS_URL:-http://webhook-tunnel:18735}"

# Connect-It connectors: embedded runs the co-hosted Connect-It container and
# wires Memoh to them with generated credentials; disabled leaves the feature
# off. Credentials are generated once and reused across upgrades, so toggling
# the mode later keeps existing connections working.
CONNECT_IT_MODE=$(printf '%s' "${MEMOH_CONNECT_IT_MODE:-}" | tr '[:upper:]' '[:lower:]')
case "$CONNECT_IT_MODE" in
  "")
    if [ "$INSTALL_MODE" = "upgrade" ]; then
      CONNECT_IT_MODE="disabled"
      echo "${YELLOW}ℹ Connect-It connectors stay disabled on upgrade; rerun with MEMOH_CONNECT_IT_MODE=embedded to enable them${NC}"
    else
      CONNECT_IT_MODE="embedded"
    fi
    ;;
  embedded|disabled)
    ;;
  *)
    echo "${RED}Error: unsupported MEMOH_CONNECT_IT_MODE '${MEMOH_CONNECT_IT_MODE}'. Use embedded or disabled.${NC}"
    exit 1
    ;;
esac
[ -n "$MEMOH_CONNECT_IT_ADMIN_PASSWORD" ] || MEMOH_CONNECT_IT_ADMIN_PASSWORD="$(gen_password)"
[ -n "$MEMOH_CONNECT_IT_SECRET_KEY" ] || MEMOH_CONNECT_IT_SECRET_KEY="1:$(gen_hex)"
[ -n "$MEMOH_CONNECT_IT_COOKIE_SECRET" ] || MEMOH_CONNECT_IT_COOKIE_SECRET="$(gen_secret)"
[ -n "$MEMOH_CONNECT_IT_API_TOKEN" ] || MEMOH_CONNECT_IT_API_TOKEN="cit_$(gen_hex)"
if [ "$CONNECT_IT_MODE" = "embedded" ]; then
  COMPOSE_PROFILES="$COMPOSE_PROFILES --profile connectors"
  MEMOH_CONNECT_IT_BASE_URL="http://connect-it:8421"
  echo "${GREEN}✓ Connect-It connectors enabled${NC}"
  if [ -z "$MEMOH_CONNECT_IT_PUBLIC_BASE_URL" ]; then
    echo "${YELLOW}ℹ Connector OAuth callbacks default to http://localhost:${MEMOH_CONNECT_IT_PORT}; set MEMOH_CONNECT_IT_PUBLIC_BASE_URL when Memoh is used from other machines${NC}"
  fi
  if [ "$USE_CN_MIRROR" = true ]; then
    echo "${YELLOW}ℹ Connect-It images come from ghcr.io; memoh.cn mirror does not cover them. Set MEMOH_CONNECT_IT_MODE=disabled to skip them${NC}"
  fi
else
  MEMOH_CONNECT_IT_BASE_URL=""
fi
export MEMOH_CONNECT_IT_MODE="$CONNECT_IT_MODE"
export MEMOH_CONNECT_IT_BASE_URL
export MEMOH_CONNECT_IT_API_TOKEN
export MEMOH_CONNECT_IT_ADMIN_PASSWORD
export MEMOH_CONNECT_IT_SECRET_KEY
export MEMOH_CONNECT_IT_COOKIE_SECRET
export MEMOH_CONNECT_IT_PUBLIC_BASE_URL
export MEMOH_CONNECT_IT_PORT
export MEMOH_CONNECT_IT_IMAGE

: > .env
write_env_value "POSTGRES_PASSWORD" "$PG_PASS"
write_env_value "MEMOH_INTERNAL_RPC_SHARED_SECRET" "$MEMOH_INTERNAL_RPC_SHARED_SECRET"
write_env_value "MEMOH_CONFIG" "./config.toml"
write_env_value "MEMOH_DATA_DIR" "$MEMOH_DATA_DIR"
write_env_value "MEMOH_DATABASE_DRIVER" "$DATABASE_DRIVER"
write_env_value "MEMOH_CONTAINER_BACKEND" "$CONTAINER_BACKEND"
write_env_value "MEMOH_WEBHOOK_PUBLIC_BASE_URL" "${MEMOH_WEBHOOK_PUBLIC_BASE_URL:-}"
write_env_value "MEMOH_WEBHOOK_TUNNEL_MODE" "$WEBHOOK_TUNNEL_MODE"
write_env_value "MEMOH_WEBHOOK_TUNNEL_LISTEN_ADDR" "$MEMOH_WEBHOOK_TUNNEL_LISTEN_ADDR"
write_env_value "MEMOH_WEBHOOK_TUNNEL_METRICS_URL" "$MEMOH_WEBHOOK_TUNNEL_METRICS_URL"
write_env_value "MEMOH_CONNECT_IT_MODE" "$CONNECT_IT_MODE"
write_env_value "MEMOH_CONNECT_IT_BASE_URL" "$MEMOH_CONNECT_IT_BASE_URL"
write_env_value "MEMOH_CONNECT_IT_ADMIN_PASSWORD" "$MEMOH_CONNECT_IT_ADMIN_PASSWORD"
write_env_value "MEMOH_CONNECT_IT_SECRET_KEY" "$MEMOH_CONNECT_IT_SECRET_KEY"
write_env_value "MEMOH_CONNECT_IT_COOKIE_SECRET" "$MEMOH_CONNECT_IT_COOKIE_SECRET"
write_env_value "MEMOH_CONNECT_IT_API_TOKEN" "$MEMOH_CONNECT_IT_API_TOKEN"
write_env_value "MEMOH_CONNECT_IT_PUBLIC_BASE_URL" "$MEMOH_CONNECT_IT_PUBLIC_BASE_URL"
write_env_value "MEMOH_CONNECT_IT_PORT" "$MEMOH_CONNECT_IT_PORT"
write_env_value "MEMOH_CONNECT_IT_IMAGE" "$MEMOH_CONNECT_IT_IMAGE"
echo "${GREEN}✓ Database backend: ${DATABASE_DRIVER}${NC}"
echo "${GREEN}✓ Workspace backend: ${CONTAINER_BACKEND}${NC}"

if [ "$INSTALL_MODE" = "reinstall" ]; then
  cleanup_existing_installation
fi

echo ""
echo "${GREEN}Pulling Docker images...${NC}"
$DOCKER compose $COMPOSE_FILES $COMPOSE_PROFILES pull

echo ""
echo "${GREEN}Starting services (first startup may take a few minutes)...${NC}"
if ! $DOCKER compose $COMPOSE_FILES $COMPOSE_PROFILES up -d --remove-orphans; then
  show_failure_logs
  exit 1
fi

# After fresh clone: copy minimal files to workspace and remove clone directory
if [ "$CLONED_FRESH" = true ]; then
  echo ""
  echo "${GREEN}Cleaning up clone directory...${NC}"
  cp "$COMPOSE_FILE_NAME" config.toml .env "$WORKSPACE/"
  mkdir -p "$WORKSPACE/conf"
  cp -r conf/providers "$WORKSPACE/conf/"
  if [ "$USE_CN_MIRROR" = true ]; then
    mkdir -p "$WORKSPACE/docker"
    cp "$CN_COMPOSE_FILE_NAME" "$WORKSPACE/docker/"
  fi
  cd "$WORKSPACE"
  rm -rf "$WORKSPACE/$DIR"
  INSTALL_DIR="$WORKSPACE"
  echo "${GREEN}✓ Clone directory removed, minimal install at ${INSTALL_DIR}${NC}"
fi

echo ""
echo "${GREEN}✅ Memoh is running!${NC}"
echo ""
echo "  🌐 Web UI:            http://localhost:8082"
echo "  🔌 API:               http://localhost:8080"
echo ""
echo "  🔑 Admin login:       ${ADMIN_USER} / ${ADMIN_PASS}"
echo ""
if [ "$CONNECT_IT_MODE" = "embedded" ]; then
  echo "  🔗 Connect-It admin:  ${MEMOH_CONNECT_IT_PUBLIC_BASE_URL:-http://localhost:${MEMOH_CONNECT_IT_PORT}} (admin / ${MEMOH_CONNECT_IT_ADMIN_PASSWORD})"
  echo ""
fi
COMPOSE_CMD="$DOCKER compose $COMPOSE_FILES $COMPOSE_PROFILES"
echo "📋 Commands:"
echo "  cd ${INSTALL_DIR} && ${COMPOSE_CMD} ps       # Status"
echo "  cd ${INSTALL_DIR} && ${COMPOSE_CMD} logs -f   # Logs"
echo "  cd ${INSTALL_DIR} && ${COMPOSE_CMD} down      # Stop"
if [ "$INSTALL_MODE" != "fresh" ]; then
  echo "  cd ${INSTALL_DIR} && ${COMPOSE_CMD} down -v   # Remove containers and Docker data"
fi
echo ""
echo "${YELLOW}⏳ First startup may take 1-2 minutes, please be patient.${NC}"
