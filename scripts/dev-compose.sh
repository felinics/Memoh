#!/bin/bash
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ "$#" -eq 0 ]]; then
  echo "usage: scripts/dev-compose.sh <compose-file> [compose-file ...]" >&2
  exit 2
fi

compose=(docker compose)
for compose_file in "$@"; do
  if [[ "${compose_file}" != /* ]]; then
    compose_file="${PROJECT_ROOT}/${compose_file}"
  fi
  compose+=(-f "${compose_file}")
done

# Dev-only bootstrap credential shared by both sides: the Connect-It container
# seeds it as an API token at startup (CONNECT_IT_BOOTSTRAP_API_TOKEN) and the
# Memoh server presents it as its bearer token. Override with
# MEMOH_CONNECT_IT_API_TOKEN to target an external Connect-It deployment.
dev_bootstrap_token="cit_1111111111111111111111111111111111111111111111111111111111111111"

export MEMOH_CONNECT_IT_BASE_URL="${MEMOH_CONNECT_IT_BASE_URL:-http://connect-it:8421}"
export MEMOH_CONNECT_IT_API_TOKEN="${MEMOH_CONNECT_IT_API_TOKEN:-${dev_bootstrap_token}}"

exec "${compose[@]}" up --build --remove-orphans
