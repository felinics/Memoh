# shellcheck shell=sh
# Compare the installed Node.js version with the newest LTS release.
#
# The body runs inside the runner's prelude (design §5.3): `set -eu` is
# already active and dep_log / dep_result are provided; do not redefine them.
# Environment: MEMOH_DEP_CURRENT_VERSION (the installed version),
# MEMOH_DEP_OS / _LIBC, MEMOH_DEP_RESULT, NODEJS_MIRROR and NODEJS_MUSL_MIRROR
# (§5.4). The exit status only says whether the check ran; whether an update
# exists is reported in the result file (WD-EXEC-003), so a download failure
# must exit non-zero.

mirror="${NODEJS_MIRROR:-https://nodejs.org/dist}"
if [ "$MEMOH_DEP_OS" = linux ] && [ "${MEMOH_DEP_LIBC:-}" = musl ]; then
  mirror="${NODEJS_MUSL_MIRROR:-https://unofficial-builds.nodejs.org/download/release}"
fi

installed="${MEMOH_DEP_CURRENT_VERSION:-}"
installed="${installed#v}"

# index.json lists one release per line, newest first; LTS releases carry
# their codename in "lts", all others carry false.
index=$(mktemp "${TMPDIR:-/tmp}/memoh-node-index.XXXXXX")
if ! curl -fsSL --retry 3 -o "$index" "$mirror/index.json"; then
  dep_log "download of $mirror/index.json failed"
  rm -f "$index"
  exit 1
fi
latest=$(grep '"lts":"' "$index" | head -n 1 | sed -n 's/.*"version":"v\([0-9][0-9.]*\)".*/\1/p')
rm -f "$index"
if [ -z "$latest" ]; then
  dep_log "no LTS release found in $mirror/index.json"
  exit 1
fi

if [ "$installed" != "$latest" ]; then available=true; else available=false; fi
dep_result "{\"installed\":\"$installed\",\"latest\":\"$latest\",\"update_available\":$available}"
