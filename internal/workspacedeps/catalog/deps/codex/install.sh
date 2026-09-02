# shellcheck shell=sh
# Install @openai/codex into a fresh versions/<pin> directory, then switch `current`.
#
# The body runs inside the runner's prelude (design §5.3): `set -eu` is
# already active and dep_log / dep_result / dep_switch are provided; do not
# redefine them. Environment: MEMOH_DEP_HOME, MEMOH_DEP_VERSION (always the
# Server pin for agent dependencies), MEMOH_DEP_RESULT, NPM_MIRROR (§5.4).
# Never hard-code the workspace data mount path (WD-EXEC-001).

command -v npm >/dev/null 2>&1 || {
  dep_log "npm is not available on PATH; the node dependency must be present first"
  exit 1
}

target="$MEMOH_DEP_HOME/versions/$MEMOH_DEP_VERSION"
mkdir -p "$target"
dep_log "Installing @openai/codex@$MEMOH_DEP_VERSION into $target"
npm install -g --prefix "$target" --include=optional --omit=dev --no-audit --no-fund \
  --registry "${NPM_MIRROR:-https://registry.npmjs.org}" "@openai/codex@$MEMOH_DEP_VERSION"

# Only switch once the new tree is usable so a failed install never breaks the
# version currently linked as `current` (WD-FS-001).
if [ ! -x "$target/bin/codex" ]; then
  dep_log "@openai/codex@$MEMOH_DEP_VERSION installed but $target/bin/codex is missing or not executable"
  exit 1
fi

dep_switch "$target"
dep_result "{\"version\":\"$MEMOH_DEP_VERSION\",\"entrypoints\":{\"codex\":\"$MEMOH_DEP_HOME/current/bin/codex\"}}"
