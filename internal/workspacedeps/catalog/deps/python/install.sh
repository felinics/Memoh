# shellcheck shell=sh
# Install a CPython overlay into versions/<version> on top of the image baseline, then switch `current`.
#
# The body runs inside the runner's prelude (design §5.3): `set -eu` is
# already active and dep_log / dep_result / dep_switch are provided; do not
# redefine them. Environment: MEMOH_DEP_HOME, MEMOH_DEP_VERSION (empty or
# "latest" selects the newest stable CPython; "3.13" selects its newest patch
# release; "3.13.2" is exact), MEMOH_DEP_RESULT. The interpreter is downloaded
# by uv (requires: [uv], the image baseline or the remote machine's own copy),
# so UV_PYTHON_INSTALL_MIRROR and the other UV_* variables apply when the
# Server exports them. Never hard-code the workspace data mount path
# (WD-EXEC-001).

command -v uv >/dev/null 2>&1 || {
  dep_log "uv is not available on PATH; the uv dependency must be present first"
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

# `uv python list --only-downloads [REQUEST]` prints one downloadable build per
# line for this platform, newest first, e.g. `cpython-3.14.7-linux-aarch64-gnu`.
# Variant builds carry a +suffix (freethreaded, debug) and pre-releases an
# rc/a/b marker. Resolving here instead of letting `uv python install` choose
# names versions/<version> after the exact release up front and keeps
# pre-releases out of "latest" explicitly; they install only when requested by
# name.
req="${MEMOH_DEP_VERSION:-}"
if [ -z "$req" ] || [ "$req" = latest ]; then
  dep_log "Resolving the latest stable CPython release known to uv"
  ver=$(uv python list --only-downloads \
    | sed -n 's/^cpython-\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)-.*/\1/p' | head -n 1)
else
  dep_log "Resolving CPython $req against uv's download list"
  ver=$(uv python list --only-downloads "$req" \
    | sed -n 's/^cpython-\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*[0-9a-z]*\)-.*/\1/p' | head -n 1)
fi
if [ -z "$ver" ]; then
  dep_log "uv knows no downloadable CPython matching '${req:-latest}' for this platform"
  exit 1
fi

versions="$MEMOH_DEP_HOME/versions"
stage="$versions/.staging-$MEMOH_DEP_ID.$$"
rm -rf "$versions/.staging-$MEMOH_DEP_ID."* "$versions/"*.previous-*
mkdir -p "$stage/root/bin"

dep_log "Installing CPython $ver into $stage with uv"
# --no-bin keeps uv from linking python3.x into ~/.local/bin: the overlay must
# stay inside MEMOH_DEP_HOME (WD-FS-002).
if ! uv python install --install-dir "$stage/root/uv" --no-bin "$ver"; then
  dep_log "uv python install $ver failed"
  rm -rf "$stage"
  exit 1
fi

# uv lays the interpreter out as <install-dir>/cpython-<version>-<platform>/
# next to an absolute minor-version symlink. Drop the symlink (it would dangle
# once the tree moves) and collect the entry points under bin/ as relative
# links so the whole tree stays relocatable.
root=""
for dir in "$stage/root/uv/cpython-$ver-"*; do
  if [ -d "$dir" ] && [ ! -L "$dir" ]; then
    root="$dir"
    break
  fi
done
if [ -z "$root" ] || [ ! -x "$root/bin/python3" ]; then
  dep_log "uv installed CPython $ver but no cpython-$ver-* directory with bin/python3 appeared"
  rm -rf "$stage"
  exit 1
fi
for link in "$stage/root/uv"/cpython-*; do
  if [ -L "$link" ]; then rm -f "$link"; fi
done
rel="${root#"$stage/root/"}"
for file in "$root"/bin/*; do
  name="${file##*/}"
  ln -s "../$rel/bin/$name" "$stage/root/bin/$name"
done
if [ ! -e "$stage/root/bin/pip3" ]; then
  # pip is bundled with the standalone builds; fall back to `python3 -m pip`.
  # shellcheck disable=SC2016
  printf '#!/bin/sh\nexec "$(dirname -- "$0")/python3" -m pip "$@"\n' > "$stage/root/bin/pip3"
  chmod 755 "$stage/root/bin/pip3"
fi

# Verify the staged tree before anything can become `current` (WD-FS-001).
if ! output=$("$stage/root/bin/python3" --version 2>&1); then
  dep_log "CPython $ver installed but bin/python3 does not run: $output"
  rm -rf "$stage"
  exit 1
fi
# `python3 --version` prints "Python 3.14.7".
actual=$(printf '%s\n' "$output" | sed -n 's/^Python \([0-9][0-9a-z.]*\).*/\1/p' | head -n 1)
[ -n "$actual" ] || actual="$ver"
if ! "$stage/root/bin/pip3" --version >/dev/null 2>&1; then
  dep_log "CPython $ver installed but bin/pip3 does not run"
  rm -rf "$stage"
  exit 1
fi

commit_staged "$stage/root" "$versions/$actual"
rm -rf "$stage"
bin="$MEMOH_DEP_HOME/current/bin"
dep_result "{\"version\":\"$actual\",\"entrypoints\":{\"python3\":\"$bin/python3\",\"pip3\":\"$bin/pip3\"}}"
