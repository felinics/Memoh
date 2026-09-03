# shellcheck shell=sh
# Install @openai/codex into a fresh versions/<version> directory, then switch `current`.
#
# The body runs inside the runner's prelude (design §5.3): `set -eu` is
# already active and dep_log / dep_result / dep_switch are provided; do not
# redefine them. Environment: MEMOH_DEP_HOME, MEMOH_DEP_VERSION (empty or
# "latest" selects the newest published release; anything else is handed to
# npm as the version spec), MEMOH_DEP_RESULT, NPM_MIRROR (§5.4).
# Never hard-code the workspace data mount path (WD-EXEC-001).

pkg="@openai/codex"
cmd="codex"
registry="${NPM_MIRROR:-https://registry.npmjs.org}"

command -v npm >/dev/null 2>&1 || {
  dep_log "npm is not available on PATH; the node dependency must be present first"
  exit 1
}

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

# Resolve the request to one exact version first so versions/<version> is
# named after what actually gets installed, also when the request is a
# dist-tag such as "latest".
req="${MEMOH_DEP_VERSION:-latest}"
dep_log "Resolving $pkg@$req against $registry"
ver=$(npm view "$pkg@$req" version --registry "$registry") || ver=""
case "$ver" in
  "" | *[[:space:]]*)
    dep_log "could not resolve $pkg@$req to a single version from $registry"
    exit 1
    ;;
esac

versions="$MEMOH_DEP_HOME/versions"
target="$versions/$ver"
stage="$versions/.staging-$MEMOH_DEP_ID.$$"
rm -rf "$versions/.staging-$MEMOH_DEP_ID."* "$versions/"*.previous-*
mkdir -p "$stage/root"

dep_log "Installing $pkg@$ver into $stage"
# npm 11 stops running a global package's install scripts unless they are
# allowed explicitly; keep the npm 10 behaviour the toolkit image had.
allow_scripts=""
case "$(npm --version 2>/dev/null | cut -d. -f1)" in
  1[1-9]|[2-9][0-9]) allow_scripts="--allow-scripts=$pkg" ;;
esac
if ! npm install -g --prefix "$stage/root" --include=optional --omit=dev --no-audit --no-fund \
  ${allow_scripts:+"$allow_scripts"} --registry "$registry" "$pkg@$ver"; then
  dep_log "npm install of $pkg@$ver failed"
  rm -rf "$stage"
  exit 1
fi

# Verify the staged tree before anything can become `current` (WD-FS-001).
if [ ! -x "$stage/root/bin/$cmd" ]; then
  dep_log "$pkg@$ver installed but bin/$cmd is missing or not executable"
  rm -rf "$stage"
  exit 1
fi

commit_staged "$stage/root" "$target"
rm -rf "$stage"
dep_result "{\"version\":\"$ver\",\"entrypoints\":{\"$cmd\":\"$MEMOH_DEP_HOME/current/bin/$cmd\"}}"
