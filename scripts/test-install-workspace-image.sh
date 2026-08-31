#!/bin/sh
set -eu

ROOT=$(CDPATH='' cd "$(dirname "$0")/.." && pwd)
TMPDIR=$(mktemp -d "${TMPDIR:-/tmp}/test-install-workspace-image.XXXXXX" 2>/dev/null || mktemp -d -t test-install-workspace-image)
trap 'rm -rf "$TMPDIR"' EXIT

FAKEBIN="$TMPDIR/bin"
mkdir -p "$FAKEBIN"

cat > "$FAKEBIN/id" <<'EOF'
#!/bin/sh
if [ "$1" = "-u" ]; then
  printf '1000\n'
  exit 0
fi
exit 1
EOF

cat > "$FAKEBIN/docker" <<'EOF'
#!/bin/sh
case "$1 ${2:-}" in
  "info "|"compose version") exit 0 ;;
  "volume inspect"|"container inspect"|"network inspect") exit 1 ;;
  "compose "*) exit 0 ;;
esac
exit 0
EOF

cat > "$FAKEBIN/git" <<'EOF'
#!/bin/sh
command=$1
shift
case "$command" in
  clone)
    for argument do
      destination=$argument
    done
    mkdir -p "$destination/conf"
    cp "$TEST_SOURCE_ROOT/docker-compose.yml" "$destination/docker-compose.yml"
    cp "$TEST_SOURCE_ROOT/conf/app.docker.toml" "$destination/conf/app.docker.toml"
    cp -R "$TEST_SOURCE_ROOT/conf/providers" "$destination/conf/providers"
    ;;
  fetch|checkout|reset|submodule) ;;
  *) echo "unexpected git command: $command" >&2; exit 1 ;;
esac
EOF

cat > "$FAKEBIN/curl" <<'EOF'
#!/bin/sh
exit 1
EOF

chmod +x "$FAKEBIN/id" "$FAKEBIN/docker" "$FAKEBIN/git" "$FAKEBIN/curl"

read_workspace_image() {
  awk '
    /^\[container\]$/ { in_container = 1; next }
    /^\[/ { in_container = 0 }
    in_container && /^[[:space:]]*default_image[[:space:]]*=/ {
      value = substr($0, index($0, "=") + 1)
      gsub(/^[[:space:]\"]+|[[:space:]\"]+$/, "", value)
      print value
      exit
    }
  ' "$1"
}

run_installer() {
  home=$1
  version=$2
  output=$3
  mkdir -p "$home"
  if ! PATH="$FAKEBIN:/usr/bin:/bin" \
    HOME="$home" \
    TEST_SOURCE_ROOT="$ROOT" \
    MEMOH_VERSION="$version" \
    MEMOH_INSTALL_MODE="${MEMOH_INSTALL_MODE:-fresh}" \
    MEMOH_CONNECT_IT_MODE=disabled \
    MEMOH_WEBHOOK_TUNNEL_MODE=disabled \
    sh "$ROOT/scripts/install.sh" --yes >"$output" 2>&1; then
    cat "$output" >&2
    return 1
  fi
}

prepare_upgrade() {
  home=$1
  image=$2
  mkdir -p "$home/memoh"
  cp "$ROOT/conf/app.docker.toml" "$home/memoh/config.toml"
  sed -i.bak "s|default_image = .*|default_image = \"$image\"|" "$home/memoh/config.toml"
  rm -f "$home/memoh/config.toml.bak"
}

FRESH_HOME="$TMPDIR/fresh"
MEMOH_INSTALL_MODE=fresh run_installer "$FRESH_HOME" v0.19.0 "$TMPDIR/fresh.out"
fresh_image=$(read_workspace_image "$FRESH_HOME/memoh/config.toml")
[ "$fresh_image" = "memohai/workspace:0.19.0-debian" ] || {
  echo "fresh install workspace image = $fresh_image" >&2
  cat "$TMPDIR/fresh.out" >&2
  exit 1
}
grep -q "memohai/server:0.19.0" "$FRESH_HOME/memoh/docker-compose.yml"
grep -q "MEMOH_INSTALLER_WORKSPACE_IMAGE='memohai/workspace:0.19.0-debian'" "$FRESH_HOME/memoh/.env"

FALLBACK_HOME="$TMPDIR/fallback"
MEMOH_INSTALL_MODE=fresh run_installer "$FALLBACK_HOME" "" "$TMPDIR/fallback.out"
fallback_image=$(read_workspace_image "$FALLBACK_HOME/memoh/config.toml")
[ "$fallback_image" = "memohai/workspace:debian-latest" ] || {
  echo "fallback workspace image = $fallback_image" >&2
  cat "$TMPDIR/fallback.out" >&2
  exit 1
}
grep -q "memohai/server:latest" "$FALLBACK_HOME/memoh/docker-compose.yml"

MANAGED_HOME="$TMPDIR/managed"
prepare_upgrade "$MANAGED_HOME" "memohai/workspace:debian"
MEMOH_INSTALL_MODE=upgrade run_installer "$MANAGED_HOME" v0.19.0 "$TMPDIR/managed-19.out"
MEMOH_INSTALL_MODE=upgrade run_installer "$MANAGED_HOME" v0.20.0 "$TMPDIR/managed-20.out"
managed_image=$(read_workspace_image "$MANAGED_HOME/memoh/config.toml")
[ "$managed_image" = "memohai/workspace:0.20.0-debian" ] || {
  echo "managed upgrade workspace image = $managed_image" >&2
  cat "$TMPDIR/managed-20.out" >&2
  exit 1
}
grep -q "MEMOH_INSTALLER_WORKSPACE_IMAGE='memohai/workspace:0.20.0-debian'" "$MANAGED_HOME/memoh/.env"

MISSING_HOME="$TMPDIR/missing"
prepare_upgrade "$MISSING_HOME" "memohai/workspace:debian"
sed -i.bak '/^[[:space:]]*default_image[[:space:]]*=/d' "$MISSING_HOME/memoh/config.toml"
rm "$MISSING_HOME/memoh/config.toml.bak"
MEMOH_INSTALL_MODE=upgrade run_installer "$MISSING_HOME" v0.20.0 "$TMPDIR/missing.out"
missing_image=$(read_workspace_image "$MISSING_HOME/memoh/config.toml")
[ "$missing_image" = "memohai/workspace:0.20.0-debian" ] || {
  echo "missing default workspace image = $missing_image" >&2
  cat "$TMPDIR/missing.out" >&2
  exit 1
}

CUSTOM_HOME="$TMPDIR/custom"
prepare_upgrade "$CUSTOM_HOME" "registry.example/workspace:gold"
printf "MEMOH_INSTALLER_WORKSPACE_IMAGE='memohai/workspace:0.19.0-debian'\n" > "$CUSTOM_HOME/memoh/.env"
MEMOH_INSTALL_MODE=upgrade run_installer "$CUSTOM_HOME" v0.20.0 "$TMPDIR/custom.out"
custom_image=$(read_workspace_image "$CUSTOM_HOME/memoh/config.toml")
[ "$custom_image" = "registry.example/workspace:gold" ] || {
  echo "custom upgrade workspace image = $custom_image" >&2
  cat "$TMPDIR/custom.out" >&2
  exit 1
}
if grep -q '^MEMOH_INSTALLER_WORKSPACE_IMAGE=' "$CUSTOM_HOME/memoh/.env"; then
  echo "custom workspace image was marked as installer-managed" >&2
  cat "$CUSTOM_HOME/memoh/.env" >&2
  exit 1
fi
