#!/bin/sh
set -eu

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
test_dir="$(mktemp -d "${TMPDIR:-/tmp}/memoh-toolkit-wrappers.XXXXXX")"
trap 'rm -rf "$test_dir"' EXIT
trap 'exit 1' HUP INT TERM

fixture="$test_dir/source"
output="$test_dir/output"
mkdir -p "$fixture/bin" "$output/bin" "$output/node-glibc"
cp "$script_dir/install-wrappers.sh" "$fixture/"
cat > "$fixture/bin/node" <<'EOF'
#!/bin/sh
printf 'updated %s\n' "$1"
EOF
printf 'runtime data\n' > "$output/node-glibc/marker"
printf 'retired\n' > "$output/bin/codex"
printf 'retired\n' > "$output/bin/claude"
printf 'retired\n' > "$output/bin/.retired"
ln -s absent "$output/bin/broken-launcher"
printf 'old\n' > "$output/bin/node"

sh "$fixture/install-wrappers.sh" "$output"
[ ! -e "$output/bin/codex" ]
[ ! -e "$output/bin/claude" ]
[ ! -e "$output/bin/.retired" ]
[ ! -L "$output/bin/broken-launcher" ]
[ "$("$output/bin/node" argument)" = 'updated argument' ]
[ -n "$(find "$output/bin" -prune -perm -005)" ]
[ "$(cat "$output/node-glibc/marker")" = 'runtime data' ]

# Repeated builds converge, including removal of wrappers retired since the
# preceding build, without affecting the separately installed runtimes.
mv "$fixture/bin/node" "$fixture/bin/next"
sh "$fixture/install-wrappers.sh" "$output"
[ ! -e "$output/bin/node" ]
[ "$("$output/bin/next" repeated)" = 'updated repeated' ]
[ -f "$output/node-glibc/marker" ]

mv "$fixture/bin" "$fixture/missing-bin"
if sh "$fixture/install-wrappers.sh" "$output" 2> "$test_dir/error"; then
  echo 'missing wrapper sources must fail the build' >&2
  exit 1
fi
[ "$("$output/bin/next" preserved)" = 'updated preserved' ]

printf 'toolkit wrapper replacement tests passed\n'
