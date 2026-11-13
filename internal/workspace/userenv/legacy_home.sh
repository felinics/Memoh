# Adopt the workspace user's previous home into the data volume, the home the
# workspace user environment defines (internal/workspace/userenv). Run with
# sh -s -- [old_home] [new_home]; both default from the system (the passwd
# home of the current user, and /data).
#
# Rules, applied entry by entry and recursively into directories that exist
# on both sides:
#   - an entry the new home does not have is copied there, then replaced in
#     the old home by a symlink to its new place, so an absolute path that
#     names the old location keeps working;
#   - a file with identical content on both sides is replaced by a symlink;
#   - anything else that exists on both sides is kept where it is, untouched;
#   - caches are not moved: they are rebuilt on demand and the data volume
#     may be small;
#   - an entry that would not fit in the free space of the data volume
#     (keeping a reserve) is left where it is.
# The script never overwrites or deletes anything in the new home. It is safe
# to run again: entries already adopted are symlinks and are skipped, and an
# interrupted copy is completed or compared on the next run. Each decision is
# printed as one line "<action> <path>" and appended to the log in the new
# home's .memoh directory.

set -u

old_home=${1:-}
new_home=${2:-/data}
if [ -z "$old_home" ]; then
  old_home=$(getent passwd "$(id -u)" 2>/dev/null | cut -d: -f6)
fi
case "$old_home" in
  "" | / | "$new_home" | "$new_home"/*) exit 0 ;;
esac
[ -d "$old_home" ] && [ ! -L "$old_home" ] || exit 0
[ -d "$new_home" ] || exit 0

state_dir="$new_home/.memoh/legacy-home"
staging="$state_dir/staging"
log_file="$state_dir/adopt.log"
# Space to leave free on the data volume, in KiB.
reserve_kib=65536

noted_file="$state_dir/noted"

report() {
  printf '%s %s\n' "$1" "$2"
  mkdir -p "$state_dir" 2>/dev/null &&
    printf '%s %s %s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$old_home" "$1" "$2" >>"$log_file" 2>/dev/null
  return 0
}

# note reports a decision that leaves things as they are (a kept conflict,
# an entry too large for the volume) once, not on every start.
note() {
  key="$1 $old_home/$2"
  if [ -f "$noted_file" ] && grep -qxF -e "$key" "$noted_file" 2>/dev/null; then
    return 0
  fi
  report "$1" "$2"
  printf '%s\n' "$key" >>"$noted_file" 2>/dev/null
  return 0
}

is_cache() {
  case "$1" in
    .cache | .cache/* | .npm/_cacache | .npm/_cacache/* | .bun/install/cache | .bun/install/cache/* | \
      .local/share/uv/cache | .local/share/uv/cache/* | .local/share/Trash | .local/share/Trash/*)
      return 0 ;;
  esac
  return 1
}

# holds_cache names the directories a cache sits in, which are adopted entry
# by entry rather than whole so the cache stays behind.
holds_cache() {
  case "$1" in
    .npm | .bun | .bun/install | .local | .local/share | .local/share/uv) return 0 ;;
  esac
  return 1
}

fits() {
  need=$(du -sk "$1" 2>/dev/null | cut -f1)
  free=$(df -Pk "$new_home" 2>/dev/null | awk 'NR==2 {print $4}')
  [ -n "$need" ] && [ -n "$free" ] && [ $((need + reserve_kib)) -le "$free" ]
}

# A subshell body keeps each level's variables apart when adopt recurses.
adopt() (
  rel=$1
  src="$old_home/$rel"
  dst="$new_home/$rel"
  if is_cache "$rel"; then
    return 0
  fi
  if [ -L "$src" ]; then
    # A link left by an earlier run points into the new home; the user's
    # own links are copied as links, and their targets resolve as before.
    case "$(readlink "$src")" in
      "$new_home" | "$new_home"/*) return 0 ;;
    esac
    if [ ! -e "$dst" ] && [ ! -L "$dst" ]; then
      cp -P "$src" "$dst" 2>/dev/null && report adopted "$rel" || report failed "$rel"
    fi
    return 0
  fi
  if [ -d "$src" ] && [ ! -e "$dst" ] && [ ! -L "$dst" ] && holds_cache "$rel"; then
    mkdir -p "$dst" || { report failed "$rel"; return 0; }
  fi
  if [ ! -e "$dst" ] && [ ! -L "$dst" ]; then
    if ! fits "$src"; then
      note skipped_no_space "$rel"
      return 0
    fi
    rm -rf "$staging" && mkdir -p "$state_dir" || { report failed "$rel"; return 0; }
    if cp -a "$src" "$staging" 2>/dev/null && mv "$staging" "$dst"; then
      if rm -rf "$src" && ln -s "$dst" "$src"; then
        report adopted "$rel"
      else
        report copied "$rel"
      fi
    else
      rm -rf "$staging"
      report failed "$rel"
    fi
    return 0
  fi
  if [ -d "$src" ] && [ -d "$dst" ] && [ ! -L "$dst" ]; then
    for child in "$src"/* "$src"/.[!.]* "$src"/..?*; do
      [ -e "$child" ] || [ -L "$child" ] || continue
      adopt "$rel/${child##*/}"
    done
    return 0
  fi
  if [ -f "$src" ] && [ -f "$dst" ] && cmp -s "$src" "$dst"; then
    rm -f "$src" && ln -s "$dst" "$src" && report linked "$rel"
    return 0
  fi
  note kept "$rel"
)

for entry in "$old_home"/* "$old_home"/.[!.]* "$old_home"/..?*; do
  [ -e "$entry" ] || [ -L "$entry" ] || continue
  adopt "${entry##*/}"
done
exit 0
