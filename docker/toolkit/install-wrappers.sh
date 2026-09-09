#!/bin/sh
set -eu

toolkit_dir="${1:?Usage: install-wrappers.sh toolkit_output_dir}"
source_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)/bin"
if [ ! -d "$source_dir" ]; then
  echo "ERROR: toolkit command wrappers not found at $source_dir" >&2
  exit 1
fi

mkdir -p "$toolkit_dir"
wrapper_stage="$(mktemp -d "$toolkit_dir/.wrappers.XXXXXX")"
trap 'rm -rf "$wrapper_stage"' EXIT
trap 'exit 1' HUP INT TERM
cp -R "$source_dir"/. "$wrapper_stage/"
for wrapper in "$wrapper_stage"/*; do
  [ -e "$wrapper" ] || continue
  chmod +x "$wrapper"
done
chmod 0755 "$wrapper_stage"

# bin is generated build output. Replacing it, instead of merging into it,
# removes retired launchers even when the toolkit output directory is reused.
# Stage first so a missing/unreadable source cannot clear the previous output.
rm -rf "${toolkit_dir:?}/bin"
mv "$wrapper_stage" "$toolkit_dir/bin"
