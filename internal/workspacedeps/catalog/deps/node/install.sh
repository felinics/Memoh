# shellcheck shell=sh
# Install a Node.js overlay into versions/<version> on top of the image baseline, then switch `current`.
#
# The body runs inside the runner's prelude (design §5.3): `set -eu` is
# already active and dep_log / dep_result / dep_switch are provided; do not
# redefine them. Environment: MEMOH_DEP_HOME, MEMOH_DEP_VERSION (empty or
# "latest" selects the newest LTS release; "22" or "22.12" selects the newest
# matching release; "22.12.0" is taken as is), MEMOH_DEP_OS / _ARCH / _LIBC,
# MEMOH_DEP_RESULT, NODEJS_MIRROR and NODEJS_MUSL_MIRROR (§5.4, same defaults
# as docker/toolkit/install.sh). npm and npx ship inside the release archive.
# Never hard-code the workspace data mount path (WD-EXEC-001).

mirror="${NODEJS_MIRROR:-https://nodejs.org/dist}"
suffix=""
case "$MEMOH_DEP_OS" in
  linux)
    if [ "${MEMOH_DEP_LIBC:-}" = musl ]; then
      mirror="${NODEJS_MUSL_MIRROR:-https://unofficial-builds.nodejs.org/download/release}"
      suffix="-musl"
    fi
    ;;
  darwin) ;;
  *)
    dep_log "Node.js overlays are not available for OS '$MEMOH_DEP_OS'"
    exit 1
    ;;
esac
case "$MEMOH_DEP_ARCH" in
  amd64) arch=x64 ;;
  arm64) arch=arm64 ;;
  *)
    dep_log "Node.js overlays are not available for architecture '$MEMOH_DEP_ARCH'"
    exit 1
    ;;
esac

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

versions="$MEMOH_DEP_HOME/versions"
stage="$versions/.staging-$MEMOH_DEP_ID.$$"
rm -rf "$versions/.staging-$MEMOH_DEP_ID."* "$versions/"*.previous-*
mkdir -p "$stage/root"

# Resolve the request to an exact release. index.json lists one release per
# line, newest first; LTS releases carry their codename in "lts", all others
# carry false.
req="${MEMOH_DEP_VERSION:-}"
req="${req#v}"
case "$req" in
  *.*.*)
    ver="$req"
    ;;
  *)
    dep_log "Resolving Node.js ${req:-latest LTS} from $mirror/index.json"
    if ! curl -fsSL --retry 3 -o "$stage/index.json" "$mirror/index.json"; then
      dep_log "download of $mirror/index.json failed"
      rm -rf "$stage"
      exit 1
    fi
    if [ -z "$req" ] || [ "$req" = latest ]; then
      ver=$(grep '"lts":"' "$stage/index.json" | head -n 1 \
        | sed -n 's/.*"version":"v\([0-9][0-9.]*\)".*/\1/p')
    else
      ver=$(grep "\"version\":\"v$req\." "$stage/index.json" | head -n 1 \
        | sed -n 's/.*"version":"v\([0-9][0-9.]*\)".*/\1/p')
    fi
    ;;
esac
if [ -z "$ver" ]; then
  dep_log "could not resolve Node.js ${req:-latest LTS} from $mirror/index.json"
  rm -rf "$stage"
  exit 1
fi

archive="node-v$ver-$MEMOH_DEP_OS-$arch$suffix.tar.gz"
url="$mirror/v$ver/$archive"
dep_log "Downloading $url"
if ! curl -fsSL --retry 3 -o "$stage/$archive" "$url"; then
  dep_log "download of $url failed"
  rm -rf "$stage"
  exit 1
fi
dep_log "Unpacking $archive"
if ! tar -xzf "$stage/$archive" --strip-components=1 -C "$stage/root"; then
  dep_log "could not unpack $archive"
  rm -rf "$stage"
  exit 1
fi
rm -f "$stage/$archive" "$stage/index.json"

# Verify the staged tree before anything can become `current` (WD-FS-001).
for cmd in node npm npx; do
  if [ ! -x "$stage/root/bin/$cmd" ]; then
    dep_log "Node.js v$ver unpacked but bin/$cmd is missing or not executable"
    rm -rf "$stage"
    exit 1
  fi
done
if ! actual=$("$stage/root/bin/node" --version); then
  dep_log "Node.js v$ver unpacked but bin/node does not run on this platform"
  rm -rf "$stage"
  exit 1
fi
actual="${actual#v}"

commit_staged "$stage/root" "$versions/$actual"
rm -rf "$stage"
bin="$MEMOH_DEP_HOME/current/bin"
dep_result "{\"version\":\"$actual\",\"entrypoints\":{\"node\":\"$bin/node\",\"npm\":\"$bin/npm\",\"npx\":\"$bin/npx\"}}"
