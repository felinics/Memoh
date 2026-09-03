# shellcheck shell=sh
# Install a uv overlay into versions/<version> on top of the image baseline, then switch `current`.
#
# The body runs inside the runner's prelude (design §5.3): `set -eu` is
# already active and dep_log / dep_result / dep_switch are provided; do not
# redefine them. Environment: MEMOH_DEP_HOME, MEMOH_DEP_VERSION (empty or
# "latest" selects the newest release), MEMOH_DEP_OS / _ARCH / _LIBC,
# MEMOH_DEP_RESULT, UV_RELEASES_URL (§5.4: the releases base URL, default
# https://github.com/astral-sh/uv/releases; archives are fetched from
# <UV_RELEASES_URL>/download/<version>/uv-<triple>.tar.gz and "latest" is
# where <UV_RELEASES_URL>/latest redirects). It is not UV_MIRROR, which
# docker/toolkit/install.sh uses with a different meaning. uvx ships in the
# same archive. Never hard-code the workspace data mount path (WD-EXEC-001).

releases="${UV_RELEASES_URL:-https://github.com/astral-sh/uv/releases}"
case "$MEMOH_DEP_OS/$MEMOH_DEP_ARCH" in
  linux/amd64) triple="x86_64-unknown-linux-gnu" ;;
  linux/arm64) triple="aarch64-unknown-linux-gnu" ;;
  darwin/amd64) triple="x86_64-apple-darwin" ;;
  darwin/arm64) triple="aarch64-apple-darwin" ;;
  *)
    dep_log "uv overlays are not available for $MEMOH_DEP_OS/$MEMOH_DEP_ARCH"
    exit 1
    ;;
esac
if [ "$MEMOH_DEP_OS" = linux ] && [ "${MEMOH_DEP_LIBC:-}" = musl ]; then
  triple="${triple%-gnu}-musl"
fi

# commit_staged <staged tree> <version dir>: make <version dir> the staged tree
# and switch `current` to it. When <version dir> already exists (re-install of
# the version in use) it is set aside first and deleted only after the switch,
# so `current` never points at a half-built tree (WD-FS-001).
commit_staged() {
  if [ -e "$2" ]; then
    mv "$2" "$2.previous-$$"
    mv "$1" "$2"
    dep_switch "$2"
    rm -rf "$2.previous-$$"
  else
    mv "$1" "$2"
    dep_switch "$2"
  fi
}

# Resolve the request to an exact release tag (uv tags carry no "v" prefix).
req="${MEMOH_DEP_VERSION:-}"
req="${req#v}"
if [ -z "$req" ] || [ "$req" = latest ]; then
  dep_log "Resolving the latest uv release from $releases/latest"
  # <releases>/latest redirects to <releases>/tag/<version>. When that yields
  # nothing (a mirror without the redirect) ask the GitHub releases API, which
  # answers with one key per line.
  ver=""
  location=$(curl -fsSIL --retry 3 -o /dev/null -w '%{url_effective}' "$releases/latest") || location=""
  case "$location" in
    */releases/tag/*) ver="${location##*/}" ;;
  esac
  if [ -z "$ver" ]; then
    ver=$(curl -fsSL --retry 3 https://api.github.com/repos/astral-sh/uv/releases/latest \
      | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1) || ver=""
  fi
  ver="${ver#v}"
  if [ -z "$ver" ]; then
    dep_log "could not resolve the latest uv release"
    exit 1
  fi
else
  ver="$req"
fi

versions="$MEMOH_DEP_HOME/versions"
stage="$versions/.staging-$MEMOH_DEP_ID.$$"
rm -rf "$versions/.staging-$MEMOH_DEP_ID."* "$versions/"*.previous-*
mkdir -p "$stage/root/bin"

archive="uv-$triple.tar.gz"
url="$releases/download/$ver/$archive"
dep_log "Downloading $url"
if ! curl -fsSL --retry 3 -o "$stage/$archive" "$url"; then
  dep_log "download of $url failed"
  rm -rf "$stage"
  exit 1
fi
dep_log "Unpacking $archive"
if ! tar -xzf "$stage/$archive" --strip-components=1 -C "$stage/root/bin"; then
  dep_log "could not unpack $archive"
  rm -rf "$stage"
  exit 1
fi
rm -f "$stage/$archive"

# Verify the staged tree before anything can become `current` (WD-FS-001).
for cmd in uv uvx; do
  if [ ! -x "$stage/root/bin/$cmd" ]; then
    dep_log "uv $ver unpacked but bin/$cmd is missing or not executable"
    rm -rf "$stage"
    exit 1
  fi
done
if ! output=$("$stage/root/bin/uv" --version); then
  dep_log "uv $ver unpacked but bin/uv does not run on this platform"
  rm -rf "$stage"
  exit 1
fi
# `uv --version` prints "uv 0.12.9 (<commit> <date>)" or "uv 0.12.9 (<triple>)".
actual=$(printf '%s\n' "$output" | sed -n 's/^uv \([^ ]*\).*/\1/p' | head -n 1)
[ -n "$actual" ] || actual="$ver"

commit_staged "$stage/root" "$versions/$actual"
rm -rf "$stage"
bin="$MEMOH_DEP_HOME/current/bin"
dep_result "{\"version\":\"$actual\",\"entrypoints\":{\"uv\":\"$bin/uv\",\"uvx\":\"$bin/uvx\"}}"
