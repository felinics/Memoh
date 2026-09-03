# shellcheck shell=sh
# Compare the installed @anthropic-ai/claude-code version with the newest release on the npm registry.
#
# The body runs inside the runner's prelude (design §5.3): `set -eu` is
# already active and dep_log / dep_result are provided; do not redefine them.
# Environment: MEMOH_DEP_CURRENT_VERSION (the installed version),
# MEMOH_DEP_RESULT, NPM_MIRROR (§5.4). The exit status only says whether the
# check ran; whether an update exists is reported in the result file
# (WD-EXEC-003), so a registry or network failure must exit non-zero.

pkg="@anthropic-ai/claude-code"
registry="${NPM_MIRROR:-https://registry.npmjs.org}"

command -v npm >/dev/null 2>&1 || {
  dep_log "npm is not available on PATH; the node dependency must be present first"
  exit 1
}

installed="${MEMOH_DEP_CURRENT_VERSION:-}"
latest=$(npm view "$pkg" version --registry "$registry") || latest=""
case "$latest" in
  "" | *[[:space:]]*)
    dep_log "could not resolve the latest $pkg version from $registry"
    exit 1
    ;;
esac

if [ "$installed" != "$latest" ]; then available=true; else available=false; fi
dep_result "{\"installed\":\"$installed\",\"latest\":\"$latest\",\"update_available\":$available}"
