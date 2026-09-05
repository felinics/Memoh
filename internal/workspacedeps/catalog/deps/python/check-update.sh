# shellcheck shell=sh
# Compare the installed CPython version with the newest stable release uv can download.
#
# The body runs inside the runner's prelude (design §5.3): `set -eu` is
# already active and dep_log / dep_result are provided; do not redefine them.
# Environment: MEMOH_DEP_CURRENT_VERSION (the installed version),
# MEMOH_DEP_RESULT. The exit status only says whether the check ran; whether
# an update exists is reported in the result file (WD-EXEC-003), so a lookup
# failure must exit non-zero. "Latest" is bounded by the download list built
# into the uv on PATH; a newer uv knows newer interpreters.

command -v uv >/dev/null 2>&1 || {
  dep_log "uv is not available on PATH; the uv dependency must be present first"
  exit 1
}

installed="${MEMOH_DEP_CURRENT_VERSION:-}"

# One downloadable build per line, newest first; variant builds carry a
# +suffix and pre-releases an rc/a/b marker, both are skipped.
latest=$(uv python list --only-downloads \
  | sed -n 's/^cpython-\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)-.*/\1/p' | head -n 1)
if [ -z "$latest" ]; then
  dep_log "uv reported no downloadable stable CPython for this platform"
  exit 1
fi

if [ "$installed" != "$latest" ]; then available=true; else available=false; fi
dep_result "{\"installed\":\"$installed\",\"latest\":\"$latest\",\"update_available\":$available}"
