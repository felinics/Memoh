# shellcheck shell=sh
# Compare the installed uv version with the newest release.
#
# The body runs inside the runner's prelude (design §5.3): `set -eu` is
# already active and dep_log / dep_result are provided; do not redefine them.
# Environment: MEMOH_DEP_CURRENT_VERSION (the installed version),
# MEMOH_DEP_RESULT, UV_RELEASES_URL (§5.4, the releases base URL; "latest" is
# where <UV_RELEASES_URL>/latest redirects, the GitHub releases API is the
# fallback). The exit status only says whether the check ran; whether an
# update exists is reported in the result file (WD-EXEC-003), so a lookup
# failure must exit non-zero.

releases="${UV_RELEASES_URL:-https://github.com/astral-sh/uv/releases}"

installed="${MEMOH_DEP_CURRENT_VERSION:-}"
installed="${installed#v}"

# <releases>/latest redirects to <releases>/tag/<version>. When that yields
# nothing (a mirror without the redirect) ask the GitHub releases API, which
# answers with one key per line.
latest=""
location=$(curl -fsSIL --retry 3 -o /dev/null -w '%{url_effective}' "$releases/latest") || location=""
case "$location" in
  */releases/tag/*) latest="${location##*/}" ;;
esac
if [ -z "$latest" ]; then
  latest=$(curl -fsSL --retry 3 https://api.github.com/repos/astral-sh/uv/releases/latest \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1) || latest=""
fi
latest="${latest#v}"
if [ -z "$latest" ]; then
  dep_log "could not resolve the latest uv release"
  exit 1
fi

if [ "$installed" != "$latest" ]; then available=true; else available=false; fi
dep_result "{\"installed\":\"$installed\",\"latest\":\"$latest\",\"update_available\":$available}"
