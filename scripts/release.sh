#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TARGET_OS="${TARGET_OS:-$(go env GOOS)}"
TARGET_ARCH="${TARGET_ARCH:-$(go env GOARCH)}"
VERSION="${VERSION:-dev}"
COMMIT_HASH="${COMMIT_HASH:-unknown}"
BUILD_TIME="${BUILD_TIME:-$(date -u +"%Y-%m-%dT%H:%M:%SZ")}"
OUTPUT_DIR="${OUTPUT_DIR:-$ROOT_DIR/dist}"
PREPARE_ASSETS_ONLY="false"

WEB_DIR="$ROOT_DIR/internal/embedded/web"

log() {
  echo "[release] $*"
}

usage() {
  cat <<'EOF'
Usage: scripts/release.sh [options]

Options:
  --os <os>             Target OS (default: current GOOS)
  --arch <arch>         Target ARCH (default: current GOARCH)
  --version <version>   Version string injected into memoh-server
  --commit-hash <sha>   Commit hash injected into memoh-server
  --output-dir <dir>    Output directory for release artifacts
  --prepare-assets      Only prepare embedded assets, do not build archive
EOF
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --os)
        TARGET_OS="$2"
        shift 2
        ;;
      --arch)
        TARGET_ARCH="$2"
        shift 2
        ;;
      --version)
        VERSION="$2"
        shift 2
        ;;
      --commit-hash)
        COMMIT_HASH="$2"
        shift 2
        ;;
      --output-dir)
        OUTPUT_DIR="$2"
        shift 2
        ;;
      --prepare-assets)
        PREPARE_ASSETS_ONLY="true"
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        echo "Unknown arg: $1" >&2
        usage >&2
        exit 1
        ;;
    esac
  done
}

write_keep_gitignore() {
  local dir="$1"
  printf "*\n!.gitignore\n" > "$dir/.gitignore"
}

prepare_embed_dirs() {
  rm -rf "$WEB_DIR"
  mkdir -p "$WEB_DIR"
  write_keep_gitignore "$WEB_DIR"
}

prepare_assets() {
  prepare_embed_dirs

  log "building web assets"
  pnpm --dir "$ROOT_DIR" web:build
  cp -R "$ROOT_DIR/apps/web/dist/." "$WEB_DIR/"
  gzip_embedded_web_assets "$WEB_DIR"

  log "embedded assets prepared"
}

gzip_embedded_web_assets() {
  local web_dir="$1"
  log "precompressing web assets (.gz)"

  while IFS= read -r -d '' file_path; do
    if [[ "$(basename "$file_path")" == ".gitignore" ]]; then
      continue
    fi
    gzip -9 -c "$file_path" > "${file_path}.gz"
    rm -f "$file_path"
  done < <(find "$web_dir" -type f -print0)
}

build_archive() {
  mkdir -p "$OUTPUT_DIR"

  local ext=""
  if [[ "$TARGET_OS" == "windows" ]]; then
    ext=".exe"
  fi

  local server_binary_name="memoh-server${ext}"
  local channel_binary_name="memoh-channel${ext}"
  local target_dir="$OUTPUT_DIR/memoh-server_${VERSION}_${TARGET_OS}_${TARGET_ARCH}"
  mkdir -p "$target_dir"

  log "building server and channel binaries ${TARGET_OS}/${TARGET_ARCH}"
  CGO_ENABLED=0 GOOS="$TARGET_OS" GOARCH="$TARGET_ARCH" \
    go build \
    -trimpath \
    -ldflags "-s -w -X github.com/felinics/memoh/internal/version.Version=${VERSION} -X github.com/felinics/memoh/internal/version.CommitHash=${COMMIT_HASH} -X github.com/felinics/memoh/internal/version.BuildTime=${BUILD_TIME}" \
    -o "$target_dir/$server_binary_name" \
    "$ROOT_DIR/cmd/agent"

  CGO_ENABLED=0 GOOS="$TARGET_OS" GOARCH="$TARGET_ARCH" \
    go build \
    -trimpath \
    -ldflags "-s -w -X github.com/felinics/memoh/internal/version.Version=${VERSION} -X github.com/felinics/memoh/internal/version.CommitHash=${COMMIT_HASH} -X github.com/felinics/memoh/internal/version.BuildTime=${BUILD_TIME}" \
    -o "$target_dir/$channel_binary_name" \
    "$ROOT_DIR/cmd/channel"

  if [[ "$TARGET_OS" == "windows" ]]; then
    (cd "$OUTPUT_DIR" && zip -q -r "memoh-server_${VERSION}_${TARGET_OS}_${TARGET_ARCH}.zip" "memoh-server_${VERSION}_${TARGET_OS}_${TARGET_ARCH}")
  else
    tar -C "$OUTPUT_DIR" -czf "$OUTPUT_DIR/memoh-server_${VERSION}_${TARGET_OS}_${TARGET_ARCH}.tar.gz" "memoh-server_${VERSION}_${TARGET_OS}_${TARGET_ARCH}"
  fi

  log "archive created (${TARGET_OS}-${TARGET_ARCH})"
}

parse_args "$@"
prepare_assets
if [[ "$PREPARE_ASSETS_ONLY" == "true" ]]; then
  log "prepare-assets only mode completed"
  exit 0
fi

build_archive
