# shellcheck shell=sh
# Remove every installed overlay version by deleting the dependency home; the image baseline takes over again.
#
# The body runs inside the runner's prelude (design §5.3): `set -eu` is
# already active and dep_log / dep_result are provided; do not redefine them.
# MEMOH_DEP_HOME is computed by the Server per target (WD-EXEC-001); the lock
# and the result file live outside it, so deleting it is safe. The copy the
# workspace image ships under the toolkit is untouched.

case "$MEMOH_DEP_HOME" in
  "" | / | */ )
    dep_log "refusing to remove suspicious MEMOH_DEP_HOME '$MEMOH_DEP_HOME'"
    exit 1
    ;;
esac

dep_log "Removing the $MEMOH_DEP_ID overlay from $MEMOH_DEP_HOME; the image baseline is used again"
rm -rf "$MEMOH_DEP_HOME"
dep_result '{}'
