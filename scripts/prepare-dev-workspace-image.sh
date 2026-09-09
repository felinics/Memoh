#!/bin/sh
set -eu

# Builds the canonical workspace image and exports it as a tar the dev server's
# nested containerd imports at boot.
#
# Both steps are expensive (the export alone writes ~1GB), and `mise run dev`
# depends on this script, so it fingerprints everything the `workspace` target
# of docker/Dockerfile.workspace actually reads and skips the whole thing when
# that fingerprint still matches the exported archive. Set
# MEMOH_DEV_WORKSPACE_IMAGE_FORCE=1 to rebuild anyway — needed when the change
# is outside the fingerprint, e.g. a new debian:bookworm-slim base or newer
# upstream packages pulled by apt.

repo_root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
image="${MEMOH_DEV_WORKSPACE_IMAGE:-memohai/workspace:debian}"
cache_dir="${MEMOH_DEV_WORKSPACE_CACHE_DIR:-$repo_root/.cache/memoh}"
archive="$cache_dir/workspace-debian.tar"
stamp="$cache_dir/workspace-debian.stamp"
tmp_dir="$cache_dir/tmp"
force="${MEMOH_DEV_WORKSPACE_IMAGE_FORCE:-0}"

# Build inputs of the `workspace` target, relative to the repo root. The
# `bridge-builder` stage copies the whole repo but only feeds
# `toolkit-acp-bridge-live`, so BuildKit never runs it for this target and the
# Go sources are deliberately absent here.
inputs="
scripts/prepare-dev-workspace-image.sh
docker/Dockerfile.workspace
docker/toolkit
scripts/desktop-install.sh
scripts/desktop-style.sh
scripts/display-apply-style.sh
scripts/display-prepare.sh
Cargo.toml
Cargo.lock
crates
"

sha256_stdin() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 | cut -d' ' -f1
  else
    openssl dgst -sha256 | sed 's/.*[= ]//'
  fi
}

compute_fingerprint() {
  cd "$repo_root"

  for input in $inputs; do
    if [ ! -e "$input" ]; then
      echo "ERROR: build input is missing: $input" >&2
      echo "       Update the input list in scripts/prepare-dev-workspace-image.sh." >&2
      exit 1
    fi
  done

  {
    # Bump the schema when the hashed stream changes shape, so existing stamps
    # are treated as stale instead of accidentally matching.
    printf 'schema=1\nimage=%s\ntarget=workspace\n' "$image"

    # shellcheck disable=SC2086 # $inputs is an intentional word-split path list.
    find $inputs -type f ! -name '.DS_Store' -print | LC_ALL=C sort | while IFS= read -r file; do
      if [ -x "$file" ]; then
        printf 'file %s mode=x\n' "$file"
      else
        printf 'file %s mode=-\n' "$file"
      fi
      cat "$file"
      printf '\n'
    done
  } | sha256_stdin
}

fingerprint="$(compute_fingerprint)"

if [ "$force" = "0" ] &&
  [ -s "$archive" ] &&
  [ -f "$stamp" ] &&
  [ "$(cat "$stamp")" = "$fingerprint" ]; then
  echo "Development workspace image is up to date: $archive"
  echo "  (set MEMOH_DEV_WORKSPACE_IMAGE_FORCE=1 to rebuild anyway)"
  exit 0
fi

mkdir -p "$cache_dir"

# Drop the stamp first: a build that dies halfway must not leave a stamp
# vouching for the previous archive.
rm -f "$stamp"

# Temp files live in their own directory so a run interrupted mid-export
# (docker save writes a ~1GB temp of its own next to the output) cannot leave
# gigabytes of orphans in the cache directory.
rm -rf "$tmp_dir"
mkdir -p "$tmp_dir"
# Orphans left by earlier revisions of this script, which exported through a
# mktemp sibling of the archive.
rm -f "$cache_dir"/workspace-debian.tar.?????? "$cache_dir"/.tmp-workspace-debian.tar.*

cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

echo "Building development workspace image: $image"
docker build \
  --progress=plain \
  --file "$repo_root/docker/Dockerfile.workspace" \
  --target workspace \
  --tag "$image" \
  "$repo_root"

archive_tmp="$tmp_dir/workspace-debian.tar"

echo "Exporting development workspace image: $archive"
docker save --output "$archive_tmp" "$image"
mv "$archive_tmp" "$archive"

printf '%s\n' "$fingerprint" > "$stamp"
