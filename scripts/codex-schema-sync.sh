#!/bin/bash
# Refresh the vendored codex app-server schema snapshot from the local codex
# CLI and report drift. Any diff means the pinned binary and the snapshot
# disagree: review the schema diff, bump VERSION.json, rerun
# `mise run codex-protocol-generate`, and re-review the generated Go.
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCHEMA_DIR="${PROJECT_ROOT}/internal/agent/runtime/codex/protocolgen/schema"

if ! command -v codex >/dev/null 2>&1; then
  echo "codex CLI not found on PATH" >&2
  exit 1
fi

pinned="$(sed -n 's/.*"codexVersion": *"\([^"]*\)".*/\1/p' "${SCHEMA_DIR}/VERSION.json")"
current="$(codex --version | awk '{print $2}')"
if [ "${pinned}" != "${current}" ]; then
  echo "note: local codex is ${current}, snapshot is pinned to ${pinned}; syncing to ${current}"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
codex app-server generate-json-schema --out "${tmp}" >/dev/null

files=(
  codex_app_server_protocol.v2.schemas.json
  ServerRequest.json
  ClientNotification.json
  CommandExecutionRequestApprovalResponse.json
  FileChangeRequestApprovalResponse.json
  PermissionsRequestApprovalResponse.json
  ToolRequestUserInputResponse.json
  McpServerElicitationRequestResponse.json
  ChatgptAuthTokensRefreshResponse.json
)
for f in "${files[@]}"; do
  cp "${tmp}/${f}" "${SCHEMA_DIR}/${f}"
done

sed -i.bak "s/\"codexVersion\": *\"[^\"]*\"/\"codexVersion\": \"${current}\"/" "${SCHEMA_DIR}/VERSION.json"
rm -f "${SCHEMA_DIR}/VERSION.json.bak"

cd "${PROJECT_ROOT}"
if git diff --quiet -- "${SCHEMA_DIR}"; then
  echo "snapshot is in sync with codex ${current}"
else
  echo "snapshot changed:"
  git diff --stat -- "${SCHEMA_DIR}"
  echo
  echo "next: review the diff, then run 'mise run codex-protocol-generate' and fix any fallout."
fi
